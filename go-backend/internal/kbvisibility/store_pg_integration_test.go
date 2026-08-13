//go:build integration

// Integration tests for the publish/unpublish transactions. The load-bearing
// assertion is the owner mirror: kb_members_sync_owner_trg fires only
// WHEN (NEW.role = 'owner'), so demoting an owner on publish leaves
// knowledge_bases.user_id stale unless the transaction nulls it explicitly.

package kbvisibility_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbvisibility"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("kbvisibility tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertUser(t *testing.T, pool *pgxpool.Pool, username string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user') RETURNING id::text`, username).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

// insertOwnedKB creates a private KB with ownerID as its kb_members owner,
// mirroring what kb.CreateKnowledgeBase does in production.
func insertOwnedKB(t *testing.T, pool *pgxpool.Pool, name, ownerID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility, user_id)
		VALUES ($1, 'private', $2::uuid) RETURNING id::text`, name, ownerID).Scan(&id); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) }) //nolint:errcheck
	if _, err := pool.Exec(ctx, `
		INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'owner')`,
		id, ownerID); err != nil {
		t.Fatalf("insert owner row: %v", err)
	}
	return id
}

func TestPublishDropsOwnerAndClearsMirror(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbvisibility.NewStore(pool)

	owner := insertUser(t, pool, "kbvis-owner-1")
	kbID := insertOwnedKB(t, pool, "kbvis-publish", owner)

	if err := store.Publish(ctx, kbID); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var visibility string
	var isPublished bool
	var mirror *string
	if err := pool.QueryRow(ctx,
		`SELECT visibility, is_published, user_id::text FROM knowledge_bases WHERE id = $1::uuid`,
		kbID).Scan(&visibility, &isPublished, &mirror); err != nil {
		t.Fatalf("select kb: %v", err)
	}
	if visibility != "public" {
		t.Fatalf("visibility = %q, want public", visibility)
	}
	// Veroeffentlichen ist gestuft: oeffentlich, aber noch nicht im Katalog.
	// is_published DEFAULTet auf true, ohne den expliziten Write waere die KB
	// sofort fuer jeden angemeldeten Nutzer lesbar.
	if isPublished {
		t.Fatal("is_published = true after Publish — publishing must stage, not go live")
	}
	// Der eigentliche Punkt: der Trigger feuert bei der Herabstufung NICHT.
	if mirror != nil {
		t.Fatalf("knowledge_bases.user_id = %q, want NULL — owner mirror is stale", *mirror)
	}

	var role string
	if err := pool.QueryRow(ctx,
		`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		kbID, owner).Scan(&role); err != nil {
		t.Fatalf("select member role: %v", err)
	}
	if role != "admin" {
		t.Fatalf("ex-owner role = %q, want admin", role)
	}

	var owners int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM kb_members WHERE kb_id = $1::uuid AND role = 'owner'`,
		kbID).Scan(&owners); err != nil {
		t.Fatalf("count owners: %v", err)
	}
	if owners != 0 {
		t.Fatalf("owner rows = %d, want 0", owners)
	}
}

func TestPublishTwiceIsRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbvisibility.NewStore(pool)

	owner := insertUser(t, pool, "kbvis-owner-2")
	kbID := insertOwnedKB(t, pool, "kbvis-publish-twice", owner)

	if err := store.Publish(ctx, kbID); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	if err := store.Publish(ctx, kbID); !errors.Is(err, kbvisibility.ErrAlreadyPublic) {
		t.Fatalf("second Publish: got %v, want ErrAlreadyPublic", err)
	}
}

func TestUnpublishPromotesOwnerAndClearsSubscriptions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbvisibility.NewStore(pool)

	owner := insertUser(t, pool, "kbvis-owner-3")
	subscriber := insertUser(t, pool, "kbvis-sub-3")
	kbID := insertOwnedKB(t, pool, "kbvis-unpublish", owner)

	if err := store.Publish(ctx, kbID); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO kb_subscriptions (kb_id, user_id, state)
		VALUES ($1::uuid, $2::uuid, 'subscribed')`, kbID, subscriber); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	if err := store.Unpublish(ctx, kbID, owner); err != nil {
		t.Fatalf("Unpublish: %v", err)
	}

	var visibility string
	var autoSubscribe, isPublished bool
	var mirror *string
	if err := pool.QueryRow(ctx, `
		SELECT visibility, auto_subscribe, is_published, user_id::text
		FROM knowledge_bases WHERE id = $1::uuid`,
		kbID).Scan(&visibility, &autoSubscribe, &isPublished, &mirror); err != nil {
		t.Fatalf("select kb: %v", err)
	}
	if visibility != "private" || autoSubscribe || isPublished {
		t.Fatalf("got visibility=%q auto_subscribe=%v is_published=%v, want private/false/false",
			visibility, autoSubscribe, isPublished)
	}
	// Hier feuert der Trigger: die Promotion auf 'owner' erfuellt seine
	// WHEN-Klausel, der Spiegel zieht automatisch nach.
	if mirror == nil || *mirror != owner {
		t.Fatalf("owner mirror = %v, want %s", mirror, owner)
	}

	var subs int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM kb_subscriptions WHERE kb_id = $1::uuid`, kbID).Scan(&subs); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if subs != 0 {
		t.Fatalf("subscriptions = %d, want 0", subs)
	}
}

func TestUnpublishPrivateKBIsRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbvisibility.NewStore(pool)

	owner := insertUser(t, pool, "kbvis-owner-4")
	kbID := insertOwnedKB(t, pool, "kbvis-already-private", owner)

	if err := store.Unpublish(ctx, kbID, owner); !errors.Is(err, kbvisibility.ErrNotPublic) {
		t.Fatalf("got %v, want ErrNotPublic", err)
	}
}

func TestUnpublishImpactListsAdminsAndSubscribers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbvisibility.NewStore(pool)

	owner := insertUser(t, pool, "kbvis-owner-5")
	subscriber := insertUser(t, pool, "kbvis-sub-5")
	kbID := insertOwnedKB(t, pool, "kbvis-impact", owner)

	if err := store.Publish(ctx, kbID); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO kb_subscriptions (kb_id, user_id, state)
		VALUES ($1::uuid, $2::uuid, 'subscribed')`, kbID, subscriber); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	impact, err := store.UnpublishImpact(ctx, kbID)
	if err != nil {
		t.Fatalf("UnpublishImpact: %v", err)
	}
	if impact.Subscribers != 1 {
		t.Fatalf("subscribers = %d, want 1", impact.Subscribers)
	}
	if len(impact.Candidates) != 1 || impact.Candidates[0].UserID != owner {
		t.Fatalf("candidates = %+v, want the ex-owner (now admin)", impact.Candidates)
	}
}
