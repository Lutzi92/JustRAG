//go:build integration

// Integration test for CreateKnowledgeBase's kb_members write. Exercises the
// real PGStore against a live main Postgres (unlike the mocked unit tests,
// which cannot catch a missing kb_members insert — that's exactly how the
// gap this test covers slipped through Task 4). Skipped when DB_* env is
// unset. Pool/skip pattern follows
// internal/kbmembers/store_pg_integration_test.go.

package kb_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kb"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("kb integration tests require DB_* env (main Postgres)")
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

// TestCreateKnowledgeBase_WritesOwnerKBMembersRow exercises the real
// CreateKnowledgeBase against a live DB and asserts that:
//
//  1. a kb_members row with role='owner' exists for the creator, and
//  2. ListKnowledgeBases (which now selects via EXISTS against kb_members,
//     not knowledge_bases.user_id) returns the new KB for that user.
//
// A mocked-store unit test cannot catch a missing kb_members write — the
// store is stubbed out entirely — which is exactly how this slipped through
// Task 4's review. This test calls the real PGStore.
func TestCreateKnowledgeBase_WritesOwnerKBMembersRow(t *testing.T) {
	pool := testPool(t) // skips via t.Skip when no DB is reachable

	userID := insertUser(t, pool, "kb-create-owner")
	store := kb.NewStore(pool)

	desc := "integration test fixture"
	row, err := store.CreateKnowledgeBase(context.Background(), "kb-create-test", &desc, userID, nil)
	if err != nil {
		t.Fatalf("CreateKnowledgeBase: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1::uuid`, row.ID) //nolint:errcheck
	})

	// (a) kb_members row with role='owner' exists for the creator.
	var role string
	err = pool.QueryRow(context.Background(),
		`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		row.ID, userID,
	).Scan(&role)
	if err != nil {
		t.Fatalf("query kb_members: %v", err)
	}
	if role != "owner" {
		t.Fatalf("kb_members role = %q, want owner", role)
	}

	// knowledge_bases.user_id must also be set (either from the direct
	// INSERT or the migration 0064 sync trigger — both should agree).
	if row.UserID == nil || *row.UserID != userID {
		t.Fatalf("knowledge_bases.user_id = %v, want %s", row.UserID, userID)
	}

	// (b) ListKnowledgeBases (EXISTS against kb_members) returns the new KB.
	list, err := store.ListKnowledgeBases(context.Background(), userID, 100, 0)
	if err != nil {
		t.Fatalf("ListKnowledgeBases: %v", err)
	}
	found := false
	for _, r := range list {
		if r.ID == row.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListKnowledgeBases(%s) did not include newly created KB %s", userID, row.ID)
	}
}

// insertKBForPending inserts a bare personal KB and returns its id.
func insertKBForPending(t *testing.T, pool *pgxpool.Pool, ownerID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, is_global, user_id)
		VALUES ('kb-pending-invite-store-test', 'fixture', false, $1::uuid)
		RETURNING id::text`, ownerID).Scan(&id)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// TestUpsertAndListPendingInvites_UseRoleColumn exercises the real PGStore's
// UpsertPendingInvite/ListPendingInvites against a live DB. Task 7's
// migration 0064 addendum renamed pending_kb_invites.permission to .role; a
// mocked-store unit test (http_sharing_test.go's TestBulkInvite_* /
// TestListShares) cannot catch a stale column name in the SQL text — it
// stubs ShareStore out entirely. This test calls the real PGStore so a wrong
// column reference fails loudly instead of silently.
func TestUpsertAndListPendingInvites_UseRoleColumn(t *testing.T) {
	pool := testPool(t)
	store := kb.NewStore(pool)

	owner := insertUser(t, pool, "pending-invite-owner")
	kbID := insertKBForPending(t, pool, owner)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM pending_kb_invites WHERE kb_id = $1::uuid`, kbID) //nolint:errcheck
	})

	if err := store.UpsertPendingInvite(context.Background(), kbID, "Pending-Admin", "admin", owner); err != nil {
		t.Fatalf("UpsertPendingInvite: %v", err)
	}

	invites, err := store.ListPendingInvites(context.Background(), kbID)
	if err != nil {
		t.Fatalf("ListPendingInvites: %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("got %d pending invites, want 1", len(invites))
	}
	if invites[0].Username != "pending-admin" {
		t.Fatalf("username = %q, want lowercased %q", invites[0].Username, "pending-admin")
	}
	if invites[0].Permission != "admin" {
		t.Fatalf("permission (role column) = %q, want %q", invites[0].Permission, "admin")
	}

	// Upsert again with a different role: same (kb_id, LOWER(username)) row
	// updates in place rather than duplicating.
	if err := store.UpsertPendingInvite(context.Background(), kbID, "pending-admin", "view", owner); err != nil {
		t.Fatalf("UpsertPendingInvite (update): %v", err)
	}
	invites, err = store.ListPendingInvites(context.Background(), kbID)
	if err != nil {
		t.Fatalf("ListPendingInvites (after update): %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("got %d pending invites after update, want 1", len(invites))
	}
	if invites[0].Permission != "view" {
		t.Fatalf("permission (role column) after update = %q, want %q", invites[0].Permission, "view")
	}
}
