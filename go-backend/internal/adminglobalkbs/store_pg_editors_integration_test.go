//go:build integration

// Integration test for the global-KB editor surface after it was repointed
// from global_kb_editors onto kb_members (role='admin').
//
// This is the regression test for a live authority split: EffectiveRole reads
// kb_members alone, so while these three operations still touched
// global_kb_editors, AddGlobalKBEditor was a silent no-op grant and
// RemoveGlobalKBEditor could not revoke the admin row migration 0064 had
// backfilled — an invisible, un-revokable KB-admin privilege. A mocked unit
// test cannot catch that: it stubs out the very SQL that was wrong. Hence a
// real PGStore against a live main Postgres, skipped when DB_* env is unset.
// Pool/skip pattern follows internal/kbmembers/store_pg_integration_test.go.

package adminglobalkbs_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/adminglobalkbs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbmembers"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("adminglobalkbs integration tests require DB_* env (main Postgres)")
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

// insertGlobalKB inserts a published global KB with no owner — the shape
// CreateGlobalKB produces (visibility = 'public', user_id = NULL), so no owner row
// exists in kb_members and the editor operations are the only writers.
func insertGlobalKB(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, visibility, is_published, user_id)
		VALUES ('adminglobalkbs-editor-test', 'fixture', 'public', true, NULL)
		RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatalf("insert global kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// memberRole returns the kb_members role for (kbID, userID), or "" when there
// is no row.
func memberRole(t *testing.T, pool *pgxpool.Pool, kbID, userID string) string {
	t.Helper()
	var role string
	err := pool.QueryRow(context.Background(),
		`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		kbID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("query kb_members role: %v", err)
	}
	return role
}

// TestGlobalKBEditors_RoundTripThroughKBMembers asserts the whole add → list →
// remove → list cycle lands in kb_members, which is what EffectiveRole reads:
//
//	add    → a kb_members row with role='admin' (a real grant, not a no-op)
//	list   → reflects the grant
//	remove → deletes that row (a real revocation)
//	list   → reflects the revocation
func TestGlobalKBEditors_RoundTripThroughKBMembers(t *testing.T) {
	pool := testPool(t) // skips via t.Skip when no DB is reachable
	ctx := context.Background()

	store := adminglobalkbs.NewStore(pool)
	kbID := insertGlobalKB(t, pool)
	editor := insertUser(t, pool, "agkb-editor")
	operator := insertUser(t, pool, "agkb-operator")

	// --- add ---------------------------------------------------------------
	if err := store.AddGlobalKBEditor(ctx, kbID, editor, operator); err != nil {
		t.Fatalf("AddGlobalKBEditor: %v", err)
	}
	if got := memberRole(t, pool, kbID, editor); got != kbaccess.RoleAdmin {
		t.Fatalf("after add: kb_members role = %q, want %q — the grant did not reach the authority table",
			got, kbaccess.RoleAdmin)
	}
	// created_by must carry the acting operator, not NULL.
	var createdBy *string
	if err := pool.QueryRow(ctx,
		`SELECT created_by::text FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		kbID, editor).Scan(&createdBy); err != nil {
		t.Fatalf("query created_by: %v", err)
	}
	if createdBy == nil || *createdBy != operator {
		t.Errorf("created_by = %v, want %s", createdBy, operator)
	}

	// --- list reflects the grant -------------------------------------------
	editors, err := store.ListGlobalKBEditors(ctx, kbID)
	if err != nil {
		t.Fatalf("ListGlobalKBEditors: %v", err)
	}
	if len(editors) != 1 {
		t.Fatalf("ListGlobalKBEditors returned %d rows, want 1: %+v", len(editors), editors)
	}
	if editors[0].ID != editor {
		t.Errorf("editor row ID = %q, want the user id %q", editors[0].ID, editor)
	}
	if editors[0].Username != "agkb-editor" {
		t.Errorf("editor row username = %q, want agkb-editor", editors[0].Username)
	}

	// --- remove ------------------------------------------------------------
	if err := store.RemoveGlobalKBEditor(ctx, kbID, editor); err != nil {
		t.Fatalf("RemoveGlobalKBEditor: %v", err)
	}
	if got := memberRole(t, pool, kbID, editor); got != "" {
		t.Fatalf("after remove: kb_members role = %q, want no row — the revocation did not take effect", got)
	}

	// --- list reflects the revocation --------------------------------------
	editors, err = store.ListGlobalKBEditors(ctx, kbID)
	if err != nil {
		t.Fatalf("ListGlobalKBEditors after remove: %v", err)
	}
	if len(editors) != 0 {
		t.Fatalf("ListGlobalKBEditors returned %d rows after removal, want 0: %+v", len(editors), editors)
	}
}

// TestGlobalKBEditors_ListIgnoresNonAdminMembers asserts the listing is
// role-scoped: view and edit members of a global KB are not curators and must
// not show up as editors (nor be removable through this surface as if they
// were).
func TestGlobalKBEditors_ListIgnoresNonAdminMembers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	store := adminglobalkbs.NewStore(pool)
	members := kbmembers.NewStore(pool)
	kbID := insertGlobalKB(t, pool)

	viewer := insertUser(t, pool, "agkb-viewer")
	editorRole := insertUser(t, pool, "agkb-edit")
	curator := insertUser(t, pool, "agkb-curator")

	if err := members.SetRole(ctx, kbID, viewer, kbaccess.RoleView, ""); err != nil {
		t.Fatalf("SetRole view: %v", err)
	}
	if err := members.SetRole(ctx, kbID, editorRole, kbaccess.RoleEdit, ""); err != nil {
		t.Fatalf("SetRole edit: %v", err)
	}
	if err := store.AddGlobalKBEditor(ctx, kbID, curator, ""); err != nil {
		t.Fatalf("AddGlobalKBEditor: %v", err)
	}

	editors, err := store.ListGlobalKBEditors(ctx, kbID)
	if err != nil {
		t.Fatalf("ListGlobalKBEditors: %v", err)
	}
	if len(editors) != 1 || editors[0].ID != curator {
		t.Fatalf("ListGlobalKBEditors = %+v, want exactly the role=admin member %s", editors, curator)
	}
}

// TestGlobalKBEditors_RemoveUnknownIsNotFound asserts a revoke that matches no
// row reports ErrNotFound rather than succeeding silently. The old
// global_kb_editors DELETE returned nil for a row it never touched, which is
// precisely how the failed revocation went unnoticed.
func TestGlobalKBEditors_RemoveUnknownIsNotFound(t *testing.T) {
	pool := testPool(t)

	store := adminglobalkbs.NewStore(pool)
	kbID := insertGlobalKB(t, pool)
	stranger := insertUser(t, pool, "agkb-stranger")

	err := store.RemoveGlobalKBEditor(context.Background(), kbID, stranger)
	if !errors.Is(err, kbmembers.ErrNotFound) {
		t.Fatalf("RemoveGlobalKBEditor on a non-member = %v, want kbmembers.ErrNotFound", err)
	}
}
