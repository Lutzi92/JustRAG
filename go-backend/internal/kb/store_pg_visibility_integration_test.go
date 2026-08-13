//go:build integration

// Integration tests for migration 0065: visibility as the write-side truth,
// is_global as a generated mirror, and the new subscription/category tables.
// Require a live main Postgres; skipped when DB_* env is unset. Pool/skip
// pattern follows internal/kbmembers/store_pg_integration_test.go.

package kb_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/adminglobalkbs"
	"github.com/justrag/go-backend/internal/kb"
)

func visPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("migration 0065 tests require DB_* env (main Postgres)")
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

// insertVisKB inserts a KB with the given visibility and returns its id.
func insertVisKB(t *testing.T, pool *pgxpool.Pool, name, visibility string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility)
		VALUES ($1, $2) RETURNING id::text`, name, visibility).Scan(&id)
	if err != nil {
		t.Fatalf("insert kb %s: %v", name, err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// TestIsGlobalMirrorsVisibility pins the generated column's contract: it is a
// pure function of visibility, in both directions, without any application
// write.
func TestIsGlobalMirrorsVisibility(t *testing.T) {
	pool := visPool(t)
	ctx := context.Background()
	id := insertVisKB(t, pool, "mig0065-public", "public")

	var isGlobal bool
	if err := pool.QueryRow(ctx,
		`SELECT is_global FROM knowledge_bases WHERE id = $1::uuid`, id).Scan(&isGlobal); err != nil {
		t.Fatalf("select is_global: %v", err)
	}
	if !isGlobal {
		t.Fatal("visibility='public' must yield is_global=true")
	}

	if _, err := pool.Exec(ctx,
		`UPDATE knowledge_bases SET visibility = 'private' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("update visibility: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT is_global FROM knowledge_bases WHERE id = $1::uuid`, id).Scan(&isGlobal); err != nil {
		t.Fatalf("re-select is_global: %v", err)
	}
	if isGlobal {
		t.Fatal("visibility='private' must yield is_global=false")
	}
}

// TestIsGlobalNotWritable is the guard that keeps a stale write path from
// silently diverging: any INSERT/UPDATE naming is_global must fail loudly.
func TestIsGlobalNotWritable(t *testing.T) {
	pool := visPool(t)
	ctx := context.Background()
	// Cleanup vor der Pruefung registrieren: schlaegt der Guard fehl, existiert
	// die Zeile, und ein t.Fatal danach wuerde sie sonst liegen lassen. Genau
	// das ist in der RED-Phase von Task 1 passiert.
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE name = 'mig0065-write'`) //nolint:errcheck
	})
	_, err := pool.Exec(ctx,
		`INSERT INTO knowledge_bases (name, is_global) VALUES ('mig0065-write', true)`)
	if err == nil {
		t.Fatal("writing the generated is_global column must fail")
	}
	if !strings.Contains(err.Error(), "generated") && !strings.Contains(err.Error(), "428C9") {
		t.Fatalf("expected a generated-column error, got: %v", err)
	}
}

// TestBackfillInvariant asserts what the backfill guarantees for every
// pre-existing row: is_global and visibility agree. A row where they disagree
// means the backfill missed it.
func TestBackfillInvariant(t *testing.T) {
	pool := visPool(t)
	var mismatches int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*)::int FROM knowledge_bases
		WHERE is_global <> (visibility = 'public')`).Scan(&mismatches); err != nil {
		t.Fatalf("count mismatches: %v", err)
	}
	if mismatches != 0 {
		t.Fatalf("%d rows where is_global and visibility disagree", mismatches)
	}
}

// TestSubscriptionStateConstraint pins the CHECK: only the two known states.
func TestSubscriptionStateConstraint(t *testing.T) {
	pool := visPool(t)
	ctx := context.Background()
	kbID := insertVisKB(t, pool, "mig0065-sub", "public")
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ('mig0065-sub-user', 'x-not-a-real-hash', 'user')
		RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID) //nolint:errcheck
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO kb_subscriptions (kb_id, user_id, state)
		VALUES ($1::uuid, $2::uuid, 'subscribed')`, kbID, userID); err != nil {
		t.Fatalf("insert valid subscription: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO kb_subscriptions (kb_id, user_id, state)
		VALUES ($1::uuid, $2::uuid, 'maybe')
		ON CONFLICT (kb_id, user_id) DO UPDATE SET state = 'maybe'`, kbID, userID); err == nil {
		t.Fatal("state='maybe' must violate the CHECK constraint")
	}
}

// TestCreateGlobalKBWritesVisibility pins that the admin create path writes
// the new column, not the generated one. Before this task it fails against
// the 0065 schema — which is exactly why the write path cannot lag behind.
func TestCreateGlobalKBWritesVisibility(t *testing.T) {
	pool := visPool(t)
	ctx := context.Background()

	store := adminglobalkbs.NewStore(pool)
	row, err := store.CreateGlobalKB(ctx, adminglobalkbs.GlobalKBCreate{Name: "mig0065-create"})
	if err != nil {
		t.Fatalf("CreateGlobalKB: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, row.ID) //nolint:errcheck
	})

	var visibility string
	var isGlobal bool
	if err := pool.QueryRow(ctx,
		`SELECT visibility, is_global FROM knowledge_bases WHERE id = $1::uuid`,
		row.ID).Scan(&visibility, &isGlobal); err != nil {
		t.Fatalf("select: %v", err)
	}
	if visibility != "public" || !isGlobal {
		t.Fatalf("want visibility=public/is_global=true, got %q/%v", visibility, isGlobal)
	}
}

// TestGlobalListHonoursSubscriptions covers the three ways a public KB
// reaches a non-admin's overview and the one way it does not.
func TestGlobalListHonoursSubscriptions(t *testing.T) {
	pool := visPool(t)
	ctx := context.Background()
	store := kb.NewStore(pool)

	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ('kbsub-list-user', 'x-not-a-real-hash', 'user') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID) }) //nolint:errcheck

	// (a) auto_subscribe, no row -> visible
	autoKB := insertVisKB(t, pool, "kbsub-auto", "public")
	mustExec(t, pool, `UPDATE knowledge_bases SET is_published = true, auto_subscribe = true WHERE id = $1::uuid`, autoKB)

	// (b) auto_subscribe but opted out -> hidden
	optedKB := insertVisKB(t, pool, "kbsub-opted", "public")
	mustExec(t, pool, `UPDATE knowledge_bases SET is_published = true, auto_subscribe = true WHERE id = $1::uuid`, optedKB)
	mustExec(t, pool, `INSERT INTO kb_subscriptions (kb_id, user_id, state) VALUES ($1::uuid, $2::uuid, 'opted_out')`, optedKB, userID)

	// (c) no auto_subscribe, explicitly subscribed -> visible
	subKB := insertVisKB(t, pool, "kbsub-explicit", "public")
	mustExec(t, pool, `UPDATE knowledge_bases SET is_published = true WHERE id = $1::uuid`, subKB)
	mustExec(t, pool, `INSERT INTO kb_subscriptions (kb_id, user_id, state) VALUES ($1::uuid, $2::uuid, 'subscribed')`, subKB, userID)

	// (d) no auto_subscribe, no subscription, but a curator row -> visible
	curatorKB := insertVisKB(t, pool, "kbsub-curator", "public")
	mustExec(t, pool, `UPDATE knowledge_bases SET is_published = true WHERE id = $1::uuid`, curatorKB)
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'admin')`, curatorKB, userID)

	// (e) published, nothing else -> hidden
	strangerKB := insertVisKB(t, pool, "kbsub-stranger", "public")
	mustExec(t, pool, `UPDATE knowledge_bases SET is_published = true WHERE id = $1::uuid`, strangerKB)

	rows, err := store.ListGlobalKnowledgeBases(ctx, userID, false)
	if err != nil {
		t.Fatalf("ListGlobalKnowledgeBases: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.ID] = true
	}
	for _, want := range []string{autoKB, subKB, curatorKB} {
		if !seen[want] {
			t.Errorf("KB %s should be visible but is not", want)
		}
	}
	for _, notWant := range []string{optedKB, strangerKB} {
		if seen[notWant] {
			t.Errorf("KB %s should be hidden but is visible", notWant)
		}
	}

	// Admins bypass the filter entirely.
	adminRows, err := store.ListGlobalKnowledgeBases(ctx, userID, true)
	if err != nil {
		t.Fatalf("ListGlobalKnowledgeBases(admin): %v", err)
	}
	adminSeen := map[string]bool{}
	for _, r := range adminRows {
		adminSeen[r.ID] = true
	}
	if !adminSeen[strangerKB] {
		t.Error("admins must see every public KB, including unsubscribed ones")
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
