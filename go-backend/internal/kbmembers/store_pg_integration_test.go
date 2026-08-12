//go:build integration

// Integration tests for migration 0064: the kb_members backfill priority
// order and the owner-mirror trigger. Require a live main Postgres; skipped
// when DB_* env is unset. Pool/skip pattern follows
// internal/adminkboverview/store_pg_transfer_integration_test.go.

package kbmembers_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("kb_members tests require DB_* env (main Postgres)")
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
// kb_members, matching the invariant application code (Task 4 onward)
// maintains: every KB has exactly one kb_members row with role='owner'.
func insertKB(t *testing.T, pool *pgxpool.Pool, ownerID string, isGlobal, isPublished bool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, is_global, is_published, user_id)
		VALUES ('kb-members-test', 'fixture', $1, $2, $3::uuid)
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

// mustQueryRow runs a query and returns the row for the caller to Scan.
func mustQueryRow(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) pgx.Row {
	t.Helper()
	return pool.QueryRow(context.Background(), sql, args...)
}

// runBackfill re-runs the three backfill INSERTs from migration 0064
// verbatim. They are idempotent by construction (ON CONFLICT DO NOTHING),
// so re-running them against a live DB in a test is safe.
func runBackfill(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	mustExec(t, pool, `
		INSERT INTO kb_members (kb_id, user_id, role)
		SELECT id, user_id, 'owner' FROM knowledge_bases WHERE user_id IS NOT NULL
		ON CONFLICT (kb_id, user_id) DO NOTHING`)
	mustExec(t, pool, `
		INSERT INTO kb_members (kb_id, user_id, role)
		SELECT kb_id, user_id, permission FROM knowledge_base_shares
		WHERE permission IN ('view','edit')
		ON CONFLICT (kb_id, user_id) DO NOTHING`)
	mustExec(t, pool, `
		INSERT INTO kb_members (kb_id, user_id, role)
		SELECT kb_id, user_id, 'admin' FROM global_kb_editors
		ON CONFLICT (kb_id, user_id) DO NOTHING`)
}

// TestBackfill_OwnerWinsOverShare legt eine KB an, deren Owner zusaetzlich eine
// knowledge_base_shares-Zeile mit 'view' besitzt, laesst den Backfill-Block der
// Migration erneut laufen und prueft, dass genau eine Zeile mit role='owner'
// uebrig bleibt — der partielle Unique-Index darf nicht verletzt werden und
// die Share-Zeile darf den Owner nicht ueberschreiben.
func TestBackfill_OwnerWinsOverShare(t *testing.T) {
	pool := testPool(t) // skippt via t.Skip wenn keine DB erreichbar ist

	userID := insertUser(t, pool, "owner-wins")
	kbID := insertKB(t, pool, userID, false, false)
	mustExec(t, pool, `INSERT INTO knowledge_base_shares (kb_id, user_id, permission)
                       VALUES ($1, $2, 'view')`, kbID, userID)

	runBackfill(t, pool) // die drei INSERT ... ON CONFLICT DO NOTHING

	var role string
	var count int
	mustQueryRow(t, pool, `SELECT role, COUNT(*) OVER () FROM kb_members
                           WHERE kb_id = $1 AND user_id = $2`, kbID, userID).Scan(&role, &count)
	if role != "owner" || count != 1 {
		t.Fatalf("got role=%q count=%d, want role=owner count=1", role, count)
	}
}

// TestBackfill_GlobalEditorBecomesAdmin prueft, dass eine global_kb_editors-Zeile
// als role='admin' landet.
func TestBackfill_GlobalEditorBecomesAdmin(t *testing.T) {
	pool := testPool(t)

	owner := insertUser(t, pool, "ge-owner")
	editor := insertUser(t, pool, "ge-editor")
	kbID := insertKB(t, pool, owner, true /*isGlobal*/, true /*isPublished*/)
	mustExec(t, pool, `INSERT INTO global_kb_editors (kb_id, user_id) VALUES ($1, $2)`, kbID, editor)

	runBackfill(t, pool)

	var role string
	mustQueryRow(t, pool, `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, editor).Scan(&role)
	if role != "admin" {
		t.Fatalf("global editor got role %q, want admin", role)
	}
}

// TestOwnerMirrorTrigger prueft, dass ein INSERT bzw. UPDATE einer Owner-Zeile
// knowledge_bases.user_id nachzieht.
func TestOwnerMirrorTrigger(t *testing.T) {
	pool := testPool(t)
	a := insertUser(t, pool, "mirror-a")
	b := insertUser(t, pool, "mirror-b")
	kbID := insertKB(t, pool, a, false, false)

	mustExec(t, pool, `UPDATE kb_members SET user_id = $2 WHERE kb_id = $1 AND role = 'owner'`, kbID, b)

	var mirrored string
	mustQueryRow(t, pool, `SELECT user_id FROM knowledge_bases WHERE id = $1`, kbID).Scan(&mirrored)
	if mirrored != b {
		t.Fatalf("mirror got %s, want %s", mirrored, b)
	}
}
