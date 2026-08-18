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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbinvites"
)

// testRunSuffix is fixed once per process, not once per test: t.Name() alone
// is deterministic across separate invocations of `go test`, so it cannot by
// itself prevent the aborted-run collision below — the whole point is that
// the SAME test, run twice, must NOT try to insert the same username twice.
var testRunSuffix = fmt.Sprintf("%d", time.Now().UnixNano())

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

// uniqueUsername builds base-{testName}-{testRunSuffix}. An aborted run
// (panic, ctrl-C, CI timeout) leaves its fixture rows behind — there is no
// TRUNCATE between local runs against the throwaway DB — so the next run's
// insertUser would collide on the users.username unique constraint unless
// the username carries something that changes between runs. testRunSuffix
// supplies that; t.Name() is folded in purely so a stray leftover row in the
// DB is traceable back to the test that created it.
func uniqueUsername(t *testing.T, base string) string {
	t.Helper()
	name := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	return base + "-" + name + "-" + testRunSuffix
}

// insertUser inserts a throwaway user (username suffixed via uniqueUsername)
// and returns its id.
func insertUser(t *testing.T, pool *pgxpool.Pool, username string) string {
	t.Helper()
	ctx := context.Background()
	username = uniqueUsername(t, username)
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
	wantOwnerName := uniqueUsername(t, "invites-owner")
	if created.CreatedByName == nil || *created.CreatedByName != wantOwnerName {
		t.Fatalf("CreatedByName = %v, want %s", created.CreatedByName, wantOwnerName)
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

// A brand-new member gets exactly the link's role.
func TestRedeemGrantsRole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	ownerID := insertUser(t, pool, "redeem-owner")
	joinerID := insertUser(t, pool, "redeem-joiner")
	kbID := insertKB(t, pool, ownerID, false, false)

	tok, _ := kbinvites.NewToken()
	link, err := store.Create(ctx, kbID, tok, "edit", nil, ownerID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, err := store.Redeem(ctx, tok, joinerID)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if res.Role != "edit" || res.AlreadyMember {
		t.Fatalf("Redeem = %+v, want role edit and AlreadyMember false", res)
	}
	if res.KBID != kbID {
		t.Fatalf("Redeem KBID = %q, want %q", res.KBID, kbID)
	}

	if got := memberRole(t, pool, kbID, joinerID); got != "edit" {
		t.Fatalf("kb_members role = %q, want edit", got)
	}

	links, _ := store.List(ctx, kbID)
	if links[0].ID != link.ID || links[0].RedemptionCount != 1 {
		t.Fatalf("RedemptionCount = %d, want 1", links[0].RedemptionCount)
	}
	if links[0].LastUsedAt == nil {
		t.Fatal("LastUsedAt is nil after a redemption")
	}
}

// The core rule: a link may raise a role, never lower it.
func TestRedeemNeverDowngrades(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	ownerID := insertUser(t, pool, "downgrade-owner")
	taID := insertUser(t, pool, "downgrade-ta")
	kbID := insertKB(t, pool, ownerID, false, false)
	setMemberRole(t, pool, kbID, taID, "edit")

	tok, _ := kbinvites.NewToken()
	if _, err := store.Create(ctx, kbID, tok, "view", nil, ownerID); err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, err := store.Redeem(ctx, tok, taID)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if res.Role != "edit" || !res.AlreadyMember {
		t.Fatalf("Redeem = %+v, want role edit and AlreadyMember true", res)
	}
	if got := memberRole(t, pool, kbID, taID); got != "edit" {
		t.Fatalf("kb_members role = %q, want edit — the view link downgraded a member", got)
	}
}

// ... and it may raise one. Also the home for the already-member counter
// assertion: RedemptionCount/LastUsedAt were previously only checked in
// TestRedeemGrantsRole's brand-new-member path, leaving the AlreadyMember=true
// path — an existing member redeeming a stronger-role link — untested for the
// one behaviour the task singles out: the counter measures link usage, not
// net new members, so it must still increment here.
func TestRedeemUpgrades(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	ownerID := insertUser(t, pool, "upgrade-owner")
	memberID := insertUser(t, pool, "upgrade-member")
	kbID := insertKB(t, pool, ownerID, false, false)
	setMemberRole(t, pool, kbID, memberID, "view")

	tok, _ := kbinvites.NewToken()
	link, err := store.Create(ctx, kbID, tok, "admin", nil, ownerID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, err := store.Redeem(ctx, tok, memberID)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if res.Role != "admin" || !res.AlreadyMember {
		t.Fatalf("Redeem = %+v, want role admin and AlreadyMember true", res)
	}
	if got := memberRole(t, pool, kbID, memberID); got != "admin" {
		t.Fatalf("kb_members role = %q, want admin", got)
	}

	links, _ := store.List(ctx, kbID)
	if links[0].ID != link.ID || links[0].RedemptionCount != 1 {
		t.Fatalf("RedemptionCount = %d, want 1 — an already-member redemption must still count as link usage", links[0].RedemptionCount)
	}
	if links[0].LastUsedAt == nil {
		t.Fatal("LastUsedAt is nil after an already-member redemption")
	}
}

// The owner row is immutable outside the explicit transfer endpoint. This
// guarantee — redeeming a link never demotes an owner — genuinely holds and
// this test genuinely proves it. What it does NOT isolate is which SQL
// clause is doing the work: today the never-downgrade rank comparison alone
// already refuses the write ('owner' outranks every role a link can carry),
// so deleting the redundant `kb_members.role <> 'owner'` conjunct by itself
// will NOT turn this test red. That is expected, not a coverage gap — see
// the comment on that conjunct in Redeem.
func TestRedeemLeavesOwnerUntouched(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	ownerID := insertUser(t, pool, "owner-untouched")
	kbID := insertKB(t, pool, ownerID, false, false)

	tok, _ := kbinvites.NewToken()
	if _, err := store.Create(ctx, kbID, tok, "view", nil, ownerID); err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, err := store.Redeem(ctx, tok, ownerID)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if res.Role != "owner" || !res.AlreadyMember {
		t.Fatalf("Redeem = %+v, want role owner and AlreadyMember true", res)
	}
	if got := memberRole(t, pool, kbID, ownerID); got != "owner" {
		t.Fatalf("kb_members role = %q, want owner — the owner was demoted", got)
	}
}

// Opt-out beats membership in the overview queries, so a stale opted_out row
// would leave the joiner a member of a KB they cannot see anywhere.
func TestRedeemClearsOptOut(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	ownerID := insertUser(t, pool, "optout-owner")
	joinerID := insertUser(t, pool, "optout-joiner")
	kbID := insertKB(t, pool, ownerID, true, true) // public + published
	if _, err := pool.Exec(ctx, `
		INSERT INTO kb_subscriptions (kb_id, user_id, state)
		VALUES ($1::uuid, $2::uuid, 'opted_out')`, kbID, joinerID); err != nil {
		t.Fatalf("insert opt-out: %v", err)
	}

	tok, _ := kbinvites.NewToken()
	if _, err := store.Create(ctx, kbID, tok, "edit", nil, ownerID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Redeem(ctx, tok, joinerID); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM kb_subscriptions
		WHERE kb_id = $1::uuid AND user_id = $2::uuid AND state = 'opted_out'`,
		kbID, joinerID).Scan(&count); err != nil {
		t.Fatalf("count opt-out: %v", err)
	}
	if count != 0 {
		t.Fatal("opted_out subscription survived the redemption — the KB stays invisible")
	}
}

func TestRedeemUnknownToken(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbinvites.NewStore(pool)

	userID := insertUser(t, pool, "unknown-token-user")
	if _, err := store.Redeem(ctx, "this-token-does-not-exist", userID); !errors.Is(err, kbinvites.ErrNotFound) {
		t.Fatalf("Redeem with unknown token returned %v, want ErrNotFound", err)
	}
}

// memberRole reads a user's kb_members role, or "" when there is no row.
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
		t.Fatalf("memberRole: %v", err)
	}
	return role
}

// setMemberRole seeds an existing membership.
func setMemberRole(t *testing.T, pool *pgxpool.Pool, kbID, userID, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO kb_members (kb_id, user_id, role)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (kb_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		kbID, userID, role); err != nil {
		t.Fatalf("setMemberRole: %v", err)
	}
}
