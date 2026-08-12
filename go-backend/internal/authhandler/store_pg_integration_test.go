//go:build integration

// Integration tests for PGStore.ApplyPendingInvites: promoting
// pending_kb_invites rows into real kb_members rows on first login. Require a
// live main Postgres; skipped when DB_* env is unset. Pool/skip pattern
// follows internal/kbmembers/store_pg_integration_test.go.

package authhandler_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/authhandler"
	"github.com/justrag/go-backend/internal/kbaccess"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("ApplyPendingInvites tests require DB_* env (main Postgres)")
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

// insertKB inserts a bare personal KB (no owner row needed for these tests —
// ApplyPendingInvites writes non-owner rows only) and returns its id.
func insertKB(t *testing.T, pool *pgxpool.Pool, ownerID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, is_global, user_id)
		VALUES ('kb-pending-invite-test', 'fixture', false, $1::uuid)
		RETURNING id::text`, ownerID).Scan(&id)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
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

// TestApplyPendingInvites_AdminRoleSurvivesPromotion covers the case Task 7
// exists for: an invite parked with role='admin' (only possible since
// migration 0064's rename widened pending_kb_invites' CHECK from
// view/edit to view/edit/admin) must land as a kb_members row with
// role='admin', not silently truncated or rejected.
func TestApplyPendingInvites_AdminRoleSurvivesPromotion(t *testing.T) {
	pool := testPool(t)
	store := authhandler.NewStore(pool)

	owner := insertUser(t, pool, "api-owner")
	invitee := insertUser(t, pool, "api-invitee")
	kbID := insertKB(t, pool, owner)
	mustExec(t, pool, `
		INSERT INTO pending_kb_invites (kb_id, username, role, invited_by)
		VALUES ($1, LOWER($2), 'admin', $3)`, kbID, "api-invitee", owner)

	if err := store.ApplyPendingInvites(context.Background(), invitee, "api-invitee"); err != nil {
		t.Fatalf("ApplyPendingInvites: %v", err)
	}

	var role string
	if err := mustQueryRow(t, pool, `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, invitee).Scan(&role); err != nil {
		t.Fatalf("read promoted member: %v", err)
	}
	if role != kbaccess.RoleAdmin {
		t.Fatalf("promoted role = %q, want %q", role, kbaccess.RoleAdmin)
	}

	var pendingCount int
	if err := mustQueryRow(t, pool, `SELECT COUNT(*) FROM pending_kb_invites WHERE kb_id = $1 AND LOWER(username) = LOWER($2)`,
		kbID, "api-invitee").Scan(&pendingCount); err != nil {
		t.Fatalf("count leftover invites: %v", err)
	}
	if pendingCount != 0 {
		t.Fatalf("pending invite should have been deleted, %d rows remain", pendingCount)
	}
}

// TestApplyPendingInvites_NeverDemotesOwner covers the guard the ON CONFLICT
// ... WHERE kb_members.role <> 'owner' clause exists for: if userID is
// already the owner of a KB a stray pending invite (e.g. from before they
// signed up, or a race against a bulk-invite) must never demote them.
func TestApplyPendingInvites_NeverDemotesOwner(t *testing.T) {
	pool := testPool(t)
	store := authhandler.NewStore(pool)

	owner := insertUser(t, pool, "guard-owner")
	kbID := insertKB(t, pool, owner)
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'owner')`, kbID, owner)
	mustExec(t, pool, `
		INSERT INTO pending_kb_invites (kb_id, username, role, invited_by)
		VALUES ($1, LOWER($2), 'view', $3)`, kbID, "guard-owner", owner)

	if err := store.ApplyPendingInvites(context.Background(), owner, "guard-owner"); err != nil {
		t.Fatalf("ApplyPendingInvites: %v", err)
	}

	var role string
	if err := mustQueryRow(t, pool, `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, owner).Scan(&role); err != nil {
		t.Fatalf("read member: %v", err)
	}
	if role != kbaccess.RoleOwner {
		t.Fatalf("owner role got demoted to %q, want owner (unchanged)", role)
	}
}

// TestApplyPendingInvites_PromotesViewAndEditRoles is a baseline check that
// the ordinary (non-admin) roles still promote correctly after the column
// rename — guards against a typo in the new SQL silently dropping rows.
func TestApplyPendingInvites_PromotesViewAndEditRoles(t *testing.T) {
	pool := testPool(t)
	store := authhandler.NewStore(pool)

	owner := insertUser(t, pool, "baseline-owner")
	viewer := insertUser(t, pool, "baseline-viewer")
	editor := insertUser(t, pool, "baseline-editor")
	kbID := insertKB(t, pool, owner)
	mustExec(t, pool, `
		INSERT INTO pending_kb_invites (kb_id, username, role, invited_by)
		VALUES ($1, LOWER($2), 'view', $3)`, kbID, "baseline-viewer", owner)
	mustExec(t, pool, `
		INSERT INTO pending_kb_invites (kb_id, username, role, invited_by)
		VALUES ($1, LOWER($2), 'edit', $3)`, kbID, "baseline-editor", owner)

	if err := store.ApplyPendingInvites(context.Background(), viewer, "baseline-viewer"); err != nil {
		t.Fatalf("ApplyPendingInvites(viewer): %v", err)
	}
	if err := store.ApplyPendingInvites(context.Background(), editor, "baseline-editor"); err != nil {
		t.Fatalf("ApplyPendingInvites(editor): %v", err)
	}

	var viewRole, editRole string
	mustQueryRow(t, pool, `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2`, kbID, viewer).Scan(&viewRole)
	mustQueryRow(t, pool, `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2`, kbID, editor).Scan(&editRole)
	if viewRole != kbaccess.RoleView {
		t.Fatalf("viewer role = %q, want view", viewRole)
	}
	if editRole != kbaccess.RoleEdit {
		t.Fatalf("editor role = %q, want edit", editRole)
	}
}
