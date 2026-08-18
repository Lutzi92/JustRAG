//go:build integration

// Integration tests for kb_invite_links (migration 0067). Require a live
// main Postgres; skipped when DB_* env is unset. Pool/skip pattern follows
// internal/kbmembers/store_pg_integration_test.go.

package kbinvites_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbinvites"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("kb_invite_links tests require DB_* env (main Postgres)")
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

// insertUser inserts a throwaway user and returns its id.
func insertUser(t *testing.T, pool *pgxpool.Pool, username string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user')
		RETURNING id::text`, username).Scan(&id)
	if err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// insertKB inserts a KB owned by ownerID and writes its owner row into
// kb_members, matching the invariant application code maintains: every KB
// has exactly one kb_members row with role='owner'.
func insertKB(t *testing.T, pool *pgxpool.Pool, ownerID string, isGlobal, isPublished bool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, visibility, is_published, user_id)
		VALUES ('kb-invites-test', 'fixture', CASE WHEN $1 THEN 'public' ELSE 'private' END, $2, $3::uuid)
		RETURNING id::text`, isGlobal, isPublished, ownerID).Scan(&id)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'owner')`, id, ownerID)
	return id
}

// mustExec runs a statement and fails the test on error.
func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func TestCreateListDelete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	ownerID := insertUser(t, pool, "invites-owner")
	kbID := insertKB(t, pool, ownerID, false, false)

	tok, err := kbinvites.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	label := "WS26 Studierende"
	created, err := store.Create(ctx, kbID, tok, "edit", &label, ownerID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Token != tok || created.Role != "edit" {
		t.Fatalf("Create returned %+v, want token %q role edit", created, tok)
	}
	if created.CreatedByName == nil || *created.CreatedByName != "invites-owner" {
		t.Fatalf("CreatedByName = %v, want invites-owner", created.CreatedByName)
	}
	if created.RedemptionCount != 0 {
		t.Fatalf("RedemptionCount = %d, want 0", created.RedemptionCount)
	}

	links, err := store.List(ctx, kbID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(links) != 1 || links[0].ID != created.ID {
		t.Fatalf("List returned %d links, want the one just created", len(links))
	}

	if err := store.Delete(ctx, kbID, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	links, err = store.List(ctx, kbID)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("List after delete returned %d links, want 0", len(links))
	}
}

// Delete must be scoped to the KB in the URL. Without the kb_id predicate an
// admin of KB A could revoke a link belonging to KB B by guessing its id.
func TestDeleteRejectsForeignKB(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	ownerID := insertUser(t, pool, "invites-foreign-owner")
	kbA := insertKB(t, pool, ownerID, false, false)
	kbB := insertKB(t, pool, ownerID, false, false)

	tok, _ := kbinvites.NewToken()
	link, err := store.Create(ctx, kbA, tok, "view", nil, ownerID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = store.Delete(ctx, kbB, link.ID)
	if !errors.Is(err, kbinvites.ErrNotFound) {
		t.Fatalf("Delete across KBs returned %v, want ErrNotFound", err)
	}

	links, _ := store.List(ctx, kbA)
	if len(links) != 1 {
		t.Fatalf("link was deleted through the wrong KB")
	}
}

// The role CHECK is the backstop behind kbaccess.Assignable: even a direct
// store call must not be able to mint an owner-granting link.
func TestCreateRejectsOwnerRole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	ownerID := insertUser(t, pool, "invites-owner-role")
	kbID := insertKB(t, pool, ownerID, false, false)

	tok, _ := kbinvites.NewToken()
	if _, err := store.Create(ctx, kbID, tok, "owner", nil, ownerID); err == nil {
		t.Fatal("Create with role=owner succeeded, want a CHECK-constraint error")
	}
}
