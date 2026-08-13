//go:build integration

// Integration test for CreateGlobalKB's creator enrolment.
//
// A public KB is ownerless by design, so nothing but a kb_members row ties a
// freshly created global KB to a person. That became load-bearing when the
// Favoriten list started applying the subscription filter to system admins as
// well (kb.Handler.ListGlobalKnowledgeBases passes isAdmin=false): a brand-new
// public KB has no subscribers and auto_subscribe defaults to false, so
// without the membership the creating admin would not see the KB they just
// made. Only the membership arm of the overview query bypasses that, which is
// why this is asserted against real SQL rather than a mock.
//
// Pool/skip pattern follows store_pg_editors_integration_test.go in this
// package, whose helpers (testPool, insertUser, memberRole) this file reuses.

package adminglobalkbs_test

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/adminglobalkbs"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// TestCreateGlobalKBEnrollsCreatorAsAdmin pins that the creating admin gets a
// kb_members row with role='admin' — not 'owner', which would break the
// ownerless invariant of a public KB and fire the owner-mirror trigger.
func TestCreateGlobalKBEnrollsCreatorAsAdmin(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := adminglobalkbs.NewStore(pool)

	creator := insertUser(t, pool, "adminglobalkbs-create-admin")

	kb, err := store.CreateGlobalKB(ctx, adminglobalkbs.GlobalKBCreate{
		Name:      "adminglobalkbs-create-test",
		Language:  "de",
		CreatedBy: creator,
	})
	if err != nil {
		t.Fatalf("CreateGlobalKB: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kb.ID) //nolint:errcheck
	})

	if got := memberRole(t, pool, kb.ID, creator); got != kbaccess.RoleAdmin {
		t.Fatalf("creator kb_members role = %q, want %q — the new KB is invisible to the admin who created it",
			got, kbaccess.RoleAdmin)
	}

	// The KB must stay ownerless: knowledge_bases.user_id is a
	// trigger-maintained mirror of the 'owner' kb_members row, and an admin
	// membership must not populate it.
	var ownerID *string
	if err := pool.QueryRow(ctx,
		`SELECT user_id::text FROM knowledge_bases WHERE id = $1::uuid`, kb.ID).Scan(&ownerID); err != nil {
		t.Fatalf("query user_id: %v", err)
	}
	if ownerID != nil {
		t.Fatalf("knowledge_bases.user_id = %q, want NULL — a public KB has no owner", *ownerID)
	}
}

// TestCreateGlobalKBWithoutCreatorWritesNoMembership pins the tolerated
// empty-CreatedBy path: non-HTTP callers keep working and no bogus membership
// row (e.g. one keyed on the nil UUID) is written.
func TestCreateGlobalKBWithoutCreatorWritesNoMembership(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := adminglobalkbs.NewStore(pool)

	kb, err := store.CreateGlobalKB(ctx, adminglobalkbs.GlobalKBCreate{
		Name:     "adminglobalkbs-create-nocreator",
		Language: "de",
	})
	if err != nil {
		t.Fatalf("CreateGlobalKB: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kb.ID) //nolint:errcheck
	})

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM kb_members WHERE kb_id = $1::uuid`, kb.ID).Scan(&count); err != nil {
		t.Fatalf("count kb_members: %v", err)
	}
	if count != 0 {
		t.Fatalf("kb_members rows = %d, want 0", count)
	}
}
