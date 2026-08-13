//go:build integration

// Integration tests for migration 0064: the kb_members backfill priority
// order and the owner-mirror trigger. Require a live main Postgres; skipped
// when DB_* env is unset. Pool/skip pattern follows
// internal/adminkboverview/store_pg_transfer_integration_test.go.

package kbmembers_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbmembers"
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
		INSERT INTO knowledge_bases (name, description, visibility, is_published, user_id)
		VALUES ('kb-members-test', 'fixture', CASE WHEN $1 THEN 'public' ELSE 'private' END, $2, $3::uuid)
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

// runBackfill re-runs the three backfill INSERTs from migration 0064 against
// one KB. They are idempotent by construction (ON CONFLICT DO NOTHING), so
// re-running them against a live DB in a test is safe.
//
// Each statement is scoped to kbID. The migration itself runs them unscoped,
// but a test must not: CI executes cascade, kb, adminkboverview, adminglobalkbs,
// authhandler and kbmembers in a single `go test` invocation against one shared
// database, so those packages run concurrently. An unscoped
// `SELECT ... FROM knowledge_bases` picks up another package's fixture KB
// mid-flight — internal/cascade builds exactly this shape and then deletes the
// parent KB in a transaction — and one side takes an FK violation on
// kb_members_kb_id_fkey. What these tests assert is backfill *priority* (owner
// beats share beats global editor), not backfill *reach*, so the kbID scope
// preserves their intent completely.
func runBackfill(t *testing.T, pool *pgxpool.Pool, kbID string) {
	t.Helper()
	mustExec(t, pool, `
		INSERT INTO kb_members (kb_id, user_id, role)
		SELECT id, user_id, 'owner' FROM knowledge_bases
		WHERE user_id IS NOT NULL AND id = $1::uuid
		ON CONFLICT (kb_id, user_id) DO NOTHING`, kbID)
	mustExec(t, pool, `
		INSERT INTO kb_members (kb_id, user_id, role)
		SELECT kb_id, user_id, permission FROM knowledge_base_shares
		WHERE permission IN ('view','edit') AND kb_id = $1::uuid
		ON CONFLICT (kb_id, user_id) DO NOTHING`, kbID)
	mustExec(t, pool, `
		INSERT INTO kb_members (kb_id, user_id, role)
		SELECT kb_id, user_id, 'admin' FROM global_kb_editors
		WHERE kb_id = $1::uuid
		ON CONFLICT (kb_id, user_id) DO NOTHING`, kbID)
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

	runBackfill(t, pool, kbID) // die drei INSERT ... ON CONFLICT DO NOTHING

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

	runBackfill(t, pool, kbID)

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

// insertChat inserts a throwaway chat owned by userID in kbID and returns its id.
func insertChat(t *testing.T, pool *pgxpool.Pool, kbID, userID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO chats (kb_id, user_id, title) VALUES ($1::uuid, $2::uuid, 'fixture chat')
		RETURNING id::text`, kbID, userID).Scan(&id)
	if err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM chats WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// TestSetRole_RefusesOwner stellt sicher, dass die Owner-Rolle nicht ueber
// SetRole vergeben werden kann — sie gehoert ausschliesslich dem Transfer.
func TestSetRole_RefusesOwner(t *testing.T) {
	pool := testPool(t)
	store := kbmembers.NewStore(pool)

	owner := insertUser(t, pool, "setrole-owner")
	target := insertUser(t, pool, "setrole-target")
	kbID := insertKB(t, pool, owner, false, false)

	err := store.SetRole(context.Background(), kbID, target, kbaccess.RoleOwner, owner)
	if !errors.Is(err, kbmembers.ErrOwnerImmutable) {
		t.Fatalf("SetRole(owner) err = %v, want ErrOwnerImmutable", err)
	}

	var count int
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, target).Scan(&count)
	if count != 0 {
		t.Fatalf("target should not have been inserted, got %d rows", count)
	}
}

// TestSetRole_RefusesDemotingOwner stellt sicher, dass ein Admin den Owner
// nicht herabstufen kann.
func TestSetRole_RefusesDemotingOwner(t *testing.T) {
	pool := testPool(t)
	store := kbmembers.NewStore(pool)

	owner := insertUser(t, pool, "demote-owner")
	admin := insertUser(t, pool, "demote-admin")
	kbID := insertKB(t, pool, owner, false, false)

	err := store.SetRole(context.Background(), kbID, owner, kbaccess.RoleEdit, admin)
	if !errors.Is(err, kbmembers.ErrOwnerImmutable) {
		t.Fatalf("SetRole(demote owner) err = %v, want ErrOwnerImmutable", err)
	}

	var role string
	mustQueryRow(t, pool, `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, owner).Scan(&role)
	if role != kbaccess.RoleOwner {
		t.Fatalf("owner role got %q, want owner (unchanged)", role)
	}
}

// TestRemoveMember_RefusesOwner stellt sicher, dass der Owner nicht entfernbar ist.
func TestRemoveMember_RefusesOwner(t *testing.T) {
	pool := testPool(t)
	store := kbmembers.NewStore(pool)

	owner := insertUser(t, pool, "removemember-owner")
	kbID := insertKB(t, pool, owner, false, false)

	err := store.RemoveMember(context.Background(), kbID, owner)
	if !errors.Is(err, kbmembers.ErrOwnerImmutable) {
		t.Fatalf("RemoveMember(owner) err = %v, want ErrOwnerImmutable", err)
	}

	var count int
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, owner).Scan(&count)
	if count != 1 {
		t.Fatalf("owner row should remain, got %d rows", count)
	}
}

// TestTransferOwner_SwapsRolesAndMirror prueft: neuer Owner hat role='owner',
// alter Owner hat role='admin', knowledge_bases.user_id zeigt auf den neuen.
func TestTransferOwner_SwapsRolesAndMirror(t *testing.T) {
	pool := testPool(t)
	store := kbmembers.NewStore(pool)

	oldOwner := insertUser(t, pool, "transfer-old")
	newOwner := insertUser(t, pool, "transfer-new")
	kbID := insertKB(t, pool, oldOwner, false, false)
	// newOwner is already a member (e.g. an existing editor) before the transfer.
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'edit')`, kbID, newOwner)

	if err := store.TransferOwner(context.Background(), kbID, newOwner); err != nil {
		t.Fatalf("TransferOwner: %v", err)
	}

	var newRole, oldRole string
	mustQueryRow(t, pool, `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, newOwner).Scan(&newRole)
	mustQueryRow(t, pool, `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, oldOwner).Scan(&oldRole)
	if newRole != kbaccess.RoleOwner {
		t.Fatalf("new owner role got %q, want owner", newRole)
	}
	if oldRole != kbaccess.RoleAdmin {
		t.Fatalf("old owner role got %q, want admin", oldRole)
	}

	var mirrored string
	mustQueryRow(t, pool, `SELECT user_id FROM knowledge_bases WHERE id = $1`, kbID).Scan(&mirrored)
	if mirrored != newOwner {
		t.Fatalf("mirror got %s, want %s", mirrored, newOwner)
	}
}

// TestLeaveKB_DeletesOwnChatsOnly legt zwei Chats von zwei Nutzern in derselben
// KB an und prueft, dass Leave nur die des Ausscheidenden loescht.
func TestLeaveKB_DeletesOwnChatsOnly(t *testing.T) {
	pool := testPool(t)
	store := kbmembers.NewStore(pool)

	owner := insertUser(t, pool, "leave-owner")
	leaver := insertUser(t, pool, "leave-leaver")
	stayer := insertUser(t, pool, "leave-stayer")
	kbID := insertKB(t, pool, owner, false, false)
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'edit')`, kbID, leaver)
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'edit')`, kbID, stayer)

	leaverChat := insertChat(t, pool, kbID, leaver)
	stayerChat := insertChat(t, pool, kbID, stayer)

	deleted, err := store.LeaveKB(context.Background(), kbID, leaver)
	if err != nil {
		t.Fatalf("LeaveKB: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deletedChats = %d, want 1", deleted)
	}

	var leaverChatCount, stayerChatCount, memberCount int
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM chats WHERE id = $1`, leaverChat).Scan(&leaverChatCount)
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM chats WHERE id = $1`, stayerChat).Scan(&stayerChatCount)
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, leaver).Scan(&memberCount)
	if leaverChatCount != 0 {
		t.Fatalf("leaver's chat should be deleted, got %d rows", leaverChatCount)
	}
	if stayerChatCount != 1 {
		t.Fatalf("stayer's chat should survive, got %d rows", stayerChatCount)
	}
	if memberCount != 0 {
		t.Fatalf("leaver's membership should be deleted, got %d rows", memberCount)
	}
}

// insertPublicKB inserts an ownerless published public KB — the shape a KB has
// after kbvisibility.Publish — with the given auto_subscribe flag.
func insertPublicKB(t *testing.T, pool *pgxpool.Pool, name string, autoSubscribe bool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility, is_published, auto_subscribe)
		VALUES ($1, 'public', true, $2)
		RETURNING id::text`, name, autoSubscribe).Scan(&id)
	if err != nil {
		t.Fatalf("insert public kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// TestLeaveKB_RecordsOptOut pins the second half of the spec's "Entfernen
// entfernt beides in einer Transaktion": leaving must ALSO store an
// opted_out subscription row. Deleting the row (or writing none) is not
// equivalent — see TestLeaveKB_TileDoesNotReturnOnAutoSubscribe.
func TestLeaveKB_RecordsOptOut(t *testing.T) {
	pool := testPool(t)
	store := kbmembers.NewStore(pool)

	curator := insertUser(t, pool, "leave-optout-curator")
	kbID := insertPublicKB(t, pool, "leave-optout-kb", true)
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'admin')`, kbID, curator)
	chatID := insertChat(t, pool, kbID, curator)

	deleted, err := store.LeaveKB(context.Background(), kbID, curator)
	if err != nil {
		t.Fatalf("LeaveKB: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deletedChats = %d, want 1", deleted)
	}

	var memberCount, chatCount int
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, curator).Scan(&memberCount)
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM chats WHERE id = $1`, chatID).Scan(&chatCount)
	if memberCount != 0 {
		t.Errorf("membership should be gone, got %d rows", memberCount)
	}
	if chatCount != 0 {
		t.Errorf("chat should be gone, got %d rows", chatCount)
	}

	var state string
	if err := mustQueryRow(t, pool,
		`SELECT state FROM kb_subscriptions WHERE kb_id = $1 AND user_id = $2`,
		kbID, curator).Scan(&state); err != nil {
		t.Fatalf("no kb_subscriptions row after LeaveKB: %v", err)
	}
	if state != "opted_out" {
		t.Fatalf("subscription state = %q, want opted_out", state)
	}
}

// TestLeaveKB_TileDoesNotReturnOnAutoSubscribe is the regression test for the
// reported bug: a curator (kb_members role='admin', the backfilled shape of
// every pre-Phase-2 global-KB editor) removes a published public KB with
// auto_subscribe = true from their overview. Before the opt-out write, the
// membership and their chats were destroyed while arm 3 of
// ListGlobalKnowledgeBases' three-way OR still matched — so the tile came
// straight back and the role was gone irrecoverably.
func TestLeaveKB_TileDoesNotReturnOnAutoSubscribe(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbmembers.NewStore(pool)
	kbStore := kb.NewStore(pool)

	curator := insertUser(t, pool, "leave-tile-curator")
	kbID := insertPublicKB(t, pool, "leave-tile-kb", true /*auto_subscribe*/)
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, 'admin')`, kbID, curator)

	// Vorbedingung: die Kachel ist sichtbar.
	before, err := kbStore.ListGlobalKnowledgeBases(ctx, curator, false)
	if err != nil {
		t.Fatalf("ListGlobalKnowledgeBases (before): %v", err)
	}
	if !containsKB(before, kbID) {
		t.Fatalf("precondition failed: KB %s is not in the overview before leaving", kbID)
	}

	if _, err := store.LeaveKB(ctx, kbID, curator); err != nil {
		t.Fatalf("LeaveKB: %v", err)
	}

	after, err := kbStore.ListGlobalKnowledgeBases(ctx, curator, false)
	if err != nil {
		t.Fatalf("ListGlobalKnowledgeBases (after): %v", err)
	}
	if containsKB(after, kbID) {
		t.Fatal("KB reappeared in the overview after leaving — auto_subscribe arm still matches, " +
			"so the chats and the role were destroyed for nothing")
	}
}

func containsKB(rows []kb.KBRow, id string) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TestLeaveKB_OwnerRefused stellt sicher, dass der Owner nicht verlassen kann.
func TestLeaveKB_OwnerRefused(t *testing.T) {
	pool := testPool(t)
	store := kbmembers.NewStore(pool)

	owner := insertUser(t, pool, "leave-refused-owner")
	kbID := insertKB(t, pool, owner, false, false)
	chatID := insertChat(t, pool, kbID, owner)

	deleted, err := store.LeaveKB(context.Background(), kbID, owner)
	if !errors.Is(err, kbmembers.ErrOwnerImmutable) {
		t.Fatalf("LeaveKB(owner) err = %v, want ErrOwnerImmutable", err)
	}
	if deleted != 0 {
		t.Fatalf("deletedChats = %d, want 0", deleted)
	}

	var chatCount, memberCount, subCount int
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM chats WHERE id = $1`, chatID).Scan(&chatCount)
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM kb_members WHERE kb_id = $1 AND user_id = $2`,
		kbID, owner).Scan(&memberCount)
	mustQueryRow(t, pool, `SELECT COUNT(*) FROM kb_subscriptions WHERE kb_id = $1 AND user_id = $2`,
		kbID, owner).Scan(&subCount)
	if chatCount != 1 {
		t.Fatalf("owner's chat should survive, got %d rows", chatCount)
	}
	if memberCount != 1 {
		t.Fatalf("owner's membership should survive, got %d rows", memberCount)
	}
	// Ein abgelehntes Verlassen darf auch keinen Opt-out schreiben.
	if subCount != 0 {
		t.Fatalf("refused leave wrote %d kb_subscriptions rows, want 0", subCount)
	}
}
