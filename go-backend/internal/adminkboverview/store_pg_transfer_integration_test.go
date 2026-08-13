//go:build integration

// Transaction-level tests for the superadmin owner transfer. Require a live
// main Postgres; skipped when DB_* env is unset.
//
// Task 7 rewrite: TransferKBOwner now delegates to kbmembers.Store.TransferOwner
// (Task 5) instead of writing knowledge_bases.user_id and
// knowledge_base_shares directly, so these assertions moved from
// knowledge_base_shares to kb_members, and "previous owner kept as editor"
// became "previous owner demoted to admin" — see TransferKBOwner's doc
// comment in store_pg.go for why.

package adminkboverview_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/adminkboverview"
	"github.com/justrag/go-backend/internal/kbaccess"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("KB owner-transfer tests require DB_* env (main Postgres)")
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

// seedUser inserts a throwaway user and returns its id.
func seedUser(t *testing.T, pool *pgxpool.Pool, username string) string {
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

// seedPersonalKB inserts a personal KB owned by ownerID ("" for ownerless)
// and, when owned, its matching kb_members owner row — TransferKBOwner now
// reads/writes kb_members exclusively, so the fixture has to seed that table
// the way application code does, not just knowledge_bases.user_id.
func seedPersonalKB(t *testing.T, pool *pgxpool.Pool, ownerID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	var owner any
	if ownerID != "" {
		owner = ownerID
	}
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, visibility, user_id)
		VALUES ('kb-transfer-test', 'fixture', 'private', $1::uuid)
		RETURNING id::text`, owner).Scan(&id)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	if ownerID != "" {
		mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'owner')`, id, ownerID)
	}
	return id
}

// mustExec runs a statement and fails the test on error.
func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// readMemberRole returns the kb_members role of (kbID, userID), or "" when
// absent.
func readMemberRole(t *testing.T, pool *pgxpool.Pool, kbID, userID string) string {
	t.Helper()
	var role string
	err := pool.QueryRow(context.Background(),
		`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		kbID, userID).Scan(&role)
	if err != nil {
		return ""
	}
	return role
}

// countMembers returns how many kb_members rows exist for kbID.
func countMembers(t *testing.T, pool *pgxpool.Pool, kbID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*)::int FROM kb_members WHERE kb_id = $1::uuid`, kbID).Scan(&n); err != nil {
		t.Fatalf("count members: %v", err)
	}
	return n
}

// readOwner returns knowledge_bases.user_id as text, or "" when NULL.
func readOwner(t *testing.T, pool *pgxpool.Pool, kbID string) string {
	t.Helper()
	var owner *string
	if err := pool.QueryRow(context.Background(),
		`SELECT user_id::text FROM knowledge_bases WHERE id = $1::uuid`, kbID).Scan(&owner); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if owner == nil {
		return ""
	}
	return *owner
}

// TestTransferKBOwner_DemotesPreviousOwnerToAdmin replaces the pre-Task-7
// "...ToEditor" test: the previous owner is now demoted to kb_members
// role='admin' (matching the four-role rights matrix), not kept as an
// 'edit' row in the retired knowledge_base_shares table.
func TestTransferKBOwner_DemotesPreviousOwnerToAdmin(t *testing.T) {
	pool := openMainPool(t)
	store := adminkboverview.NewStore(pool)

	oldOwner := seedUser(t, pool, "transfer-old-owner")
	newOwner := seedUser(t, pool, "transfer-new-owner")
	kbID := seedPersonalKB(t, pool, oldOwner)

	if err := store.TransferKBOwner(context.Background(), kbID, newOwner, &oldOwner); err != nil {
		t.Fatalf("TransferKBOwner: %v", err)
	}

	if got := readOwner(t, pool, kbID); got != newOwner {
		t.Errorf("owner = %q, want %q", got, newOwner)
	}
	if got := readMemberRole(t, pool, kbID, newOwner); got != kbaccess.RoleOwner {
		t.Errorf("new owner kb_members role = %q, want %q", got, kbaccess.RoleOwner)
	}
	if got := readMemberRole(t, pool, kbID, oldOwner); got != kbaccess.RoleAdmin {
		t.Errorf("previous owner kb_members role = %q, want %q (behaviour change: was 'edit' pre-Task-7)", got, kbaccess.RoleAdmin)
	}
}

// TestTransferKBOwner_PromotesExistingMemberToOwner replaces the pre-Task-7
// "...DropsNewOwnerStaleShare" test: a new owner who already held a lesser
// kb_members role is promoted to owner outright — ownership supersedes
// whatever role preceded it, there is no separate "drop the stale share"
// step because knowledge_base_shares is no longer involved.
func TestTransferKBOwner_PromotesExistingMemberToOwner(t *testing.T) {
	pool := openMainPool(t)
	store := adminkboverview.NewStore(pool)

	oldOwner := seedUser(t, pool, "transfer-stale-old")
	newOwner := seedUser(t, pool, "transfer-stale-new")
	kbID := seedPersonalKB(t, pool, oldOwner)

	// The new owner already had a view role before the transfer.
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'view')`, kbID, newOwner)

	if err := store.TransferKBOwner(context.Background(), kbID, newOwner, &oldOwner); err != nil {
		t.Fatalf("TransferKBOwner: %v", err)
	}

	if got := readMemberRole(t, pool, kbID, newOwner); got != kbaccess.RoleOwner {
		t.Errorf("new owner kb_members role = %q, want %q (ownership supersedes the prior view role)", got, kbaccess.RoleOwner)
	}
}

// TestTransferKBOwner_OwnerlessKBCreatesNoExtraMember replaces the pre-Task-7
// "...CreatesNoShare" test: transferring an ownerless KB creates exactly one
// kb_members row (the new owner) — there is no previous-owner row to demote,
// so no stray admin row should appear either.
func TestTransferKBOwner_OwnerlessKBCreatesNoExtraMember(t *testing.T) {
	pool := openMainPool(t)
	store := adminkboverview.NewStore(pool)

	newOwner := seedUser(t, pool, "transfer-orphan-new")
	kbID := seedPersonalKB(t, pool, "")

	if err := store.TransferKBOwner(context.Background(), kbID, newOwner, nil); err != nil {
		t.Fatalf("TransferKBOwner: %v", err)
	}

	if got := readOwner(t, pool, kbID); got != newOwner {
		t.Errorf("owner = %q, want %q", got, newOwner)
	}
	if got := readMemberRole(t, pool, kbID, newOwner); got != kbaccess.RoleOwner {
		t.Errorf("new owner kb_members role = %q, want %q", got, kbaccess.RoleOwner)
	}
	if n := countMembers(t, pool, kbID); n != 1 {
		t.Errorf("member count = %d, want 1 (only the new owner)", n)
	}
}

// TestTransferKBOwner_MissingKBReturnsErrKBNotFound is the regression test
// for the bug Task 7 exists to fix: before this task, TransferKBOwner wrote
// knowledge_bases.user_id directly and its own RowsAffected()==0 check
// caught a missing KB. Delegating to kbmembers.Store.TransferOwner changes
// how "the KB does not exist" surfaces (a kb_members_kb_id_fkey violation on
// the INSERT, not a zero-rows UPDATE) — this test pins that ErrKBNotFound
// still comes out the other end for a kbID that was never a knowledge_bases
// row.
func TestTransferKBOwner_MissingKBReturnsErrKBNotFound(t *testing.T) {
	pool := openMainPool(t)
	store := adminkboverview.NewStore(pool)

	newOwner := seedUser(t, pool, "transfer-missing-kb-target")
	missingKBID := uuid.NewString()

	err := store.TransferKBOwner(context.Background(), missingKBID, newOwner, nil)
	if !errors.Is(err, adminkboverview.ErrKBNotFound) {
		t.Fatalf("TransferKBOwner(missing kb) err = %v, want ErrKBNotFound", err)
	}
}
