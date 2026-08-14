//go:build integration

// Integration tests for the catalog query — the most intricate new SQL of the
// visibility phase and, until this file, the only one nothing ever executed:
// three correlated EXISTS subqueries, an array_agg scanned into a []string, and
// a []string bound as uuid[]. The unit tests next door drive a fake store and
// therefore prove nothing about any of that.
//
// Require a live main Postgres; skipped when DB_* env is unset. Pool/skip
// pattern follows internal/kbvisibility/store_pg_integration_test.go.

package kbsubs_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbsubs"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("kbsubs tests require DB_* env (main Postgres)")
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

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func insertUser(t *testing.T, pool *pgxpool.Pool, username string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user') RETURNING id::text`, username).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

// insertKB creates a KB with an explicit visibility / is_published /
// auto_subscribe triple and an optional description.
func insertKB(t *testing.T, pool *pgxpool.Pool, name, description, visibility string, published, autoSubscribe bool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, visibility, is_published, auto_subscribe)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
		name, description, visibility, published, autoSubscribe).Scan(&id); err != nil {
		t.Fatalf("insert kb %s: %v", name, err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

func insertCategory(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO kb_categories (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatalf("insert category %s: %v", name, err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM kb_categories WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

// byID indexes a catalog result so assertions read as "this KB, that field".
func byID(entries []kbsubs.CatalogEntry) map[string]kbsubs.CatalogEntry {
	m := make(map[string]kbsubs.CatalogEntry, len(entries))
	for _, e := range entries {
		m[e.ID] = e
	}
	return m
}

// insertMember gives the user a kb_members row on the KB.
func insertMember(t *testing.T, pool *pgxpool.Pool, kbID, userID, role string) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO kb_members (kb_id, user_id, role)
	                   VALUES ($1::uuid, $2::uuid, $3)`, kbID, userID, role)
}

// TestCatalog_ListsOnlyPublishedPublicKBs pins the WHERE clause: the catalog is
// the discovery surface for KBs that are live, so a private KB and a public one
// still staged (is_published = false, the state kbvisibility.Publish leaves
// behind) must both stay out of it — for a caller with no membership on them.
func TestCatalog_ListsOnlyPublishedPublicKBs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbsubs.NewStore(pool)

	user := insertUser(t, pool, "kbsubs-cat-user-1")
	published := insertKB(t, pool, "kbsubs-cat-published", "", "public", true, false)
	staged := insertKB(t, pool, "kbsubs-cat-staged", "", "public", false, false)
	private := insertKB(t, pool, "kbsubs-cat-private", "", "private", true, false)

	entries, err := store.Catalog(ctx, user, "kbsubs-cat-", nil)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	got := byID(entries)
	if _, ok := got[published]; !ok {
		t.Error("a published public KB must appear in the catalog")
	}
	if _, ok := got[staged]; ok {
		t.Error("an unpublished public KB must not appear in the catalog")
	}
	if _, ok := got[private]; ok {
		t.Error("a private KB must not appear in the catalog")
	}
}

// TestCatalog_SubscribedMirrorsTheOverviewRule exercises all three arms of the
// subscribed expression against real rows — the part the fake-store unit tests
// cannot reach.
func TestCatalog_SubscribedMirrorsTheOverviewRule(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbsubs.NewStore(pool)

	user := insertUser(t, pool, "kbsubs-sub-user")

	// (a) explizites Abo, kein auto_subscribe -> true
	explicitKB := insertKB(t, pool, "kbsubs-sub-explicit", "", "public", true, false)
	mustExec(t, pool, `INSERT INTO kb_subscriptions (kb_id, user_id, state)
	                   VALUES ($1::uuid, $2::uuid, 'subscribed')`, explicitKB, user)

	// (b) auto_subscribe ohne Zeile -> true
	autoKB := insertKB(t, pool, "kbsubs-sub-auto", "", "public", true, true)

	// (c) auto_subscribe, aber ausgetragen -> false
	optedKB := insertKB(t, pool, "kbsubs-sub-optedout", "", "public", true, true)
	mustExec(t, pool, `INSERT INTO kb_subscriptions (kb_id, user_id, state)
	                   VALUES ($1::uuid, $2::uuid, 'opted_out')`, optedKB, user)

	// (d) weder noch -> false
	plainKB := insertKB(t, pool, "kbsubs-sub-plain", "", "public", true, false)

	entries, err := store.Catalog(ctx, user, "kbsubs-sub-", nil)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	got := byID(entries)
	for _, tc := range []struct {
		name string
		id   string
		want bool
	}{
		{"explicit subscription", explicitKB, true},
		{"auto_subscribe, no row", autoKB, true},
		{"auto_subscribe, opted out", optedKB, false},
		{"no auto_subscribe, no row", plainKB, false},
	} {
		entry, ok := got[tc.id]
		if !ok {
			t.Errorf("%s: KB %s missing from the catalog", tc.name, tc.id)
			continue
		}
		if entry.Subscribed != tc.want {
			t.Errorf("%s: subscribed = %v, want %v", tc.name, entry.Subscribed, tc.want)
		}
	}
}

// TestCatalog_TextFilter covers both ILIKE arms plus the empty-query escape.
func TestCatalog_TextFilter(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbsubs.NewStore(pool)

	user := insertUser(t, pool, "kbsubs-filter-user")
	byName := insertKB(t, pool, "kbsubs-filter-Zeppelin", "nothing special", "public", true, false)
	byDesc := insertKB(t, pool, "kbsubs-filter-other", "all about Zeppelin flight", "public", true, false)
	neither := insertKB(t, pool, "kbsubs-filter-third", "unrelated", "public", true, false)

	hits, err := store.Catalog(ctx, user, "zeppelin", nil)
	if err != nil {
		t.Fatalf("Catalog(zeppelin): %v", err)
	}
	got := byID(hits)
	if _, ok := got[byName]; !ok {
		t.Error("the name match is missing")
	}
	if _, ok := got[byDesc]; !ok {
		t.Error("the description match is missing")
	}
	if _, ok := got[neither]; ok {
		t.Error("a non-matching KB leaked through the text filter")
	}

	all, err := store.Catalog(ctx, user, "", nil)
	if err != nil {
		t.Fatalf("Catalog(empty): %v", err)
	}
	unfiltered := byID(all)
	for _, id := range []string{byName, byDesc, neither} {
		if _, ok := unfiltered[id]; !ok {
			t.Errorf("an empty query must filter nothing, but KB %s is missing", id)
		}
	}
}

// TestCatalog_CategoryFilterAndIDs covers the []string -> uuid[] bind, the
// cardinality escape hatch, and the array_agg -> []string scan on the way back.
func TestCatalog_CategoryFilterAndIDs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbsubs.NewStore(pool)

	user := insertUser(t, pool, "kbsubs-cats-user")
	catA := insertCategory(t, pool, "kbsubs-cat-A")
	catB := insertCategory(t, pool, "kbsubs-cat-B")

	taggedKB := insertKB(t, pool, "kbsubs-cats-tagged", "", "public", true, false)
	mustExec(t, pool, `INSERT INTO kb_category_links (kb_id, category_id) VALUES ($1::uuid, $2::uuid)`, taggedKB, catA)
	mustExec(t, pool, `INSERT INTO kb_category_links (kb_id, category_id) VALUES ($1::uuid, $2::uuid)`, taggedKB, catB)

	untaggedKB := insertKB(t, pool, "kbsubs-cats-untagged", "", "public", true, false)

	// Kein Kategorienfilter: beide KBs kommen zurueck, und die getaggte traegt
	// ihre beiden Kategorie-IDs als echtes []string.
	all, err := store.Catalog(ctx, user, "kbsubs-cats-", nil)
	if err != nil {
		t.Fatalf("Catalog(no categories): %v", err)
	}
	got := byID(all)
	if len(got) != 2 {
		t.Fatalf("an empty category list must filter nothing, got %d entries", len(got))
	}
	tagged := got[taggedKB]
	if len(tagged.CategoryIDs) != 2 {
		t.Fatalf("categoryIds = %v, want the two linked categories", tagged.CategoryIDs)
	}
	seen := map[string]bool{}
	for _, id := range tagged.CategoryIDs {
		seen[id] = true
	}
	if !seen[catA] || !seen[catB] {
		t.Fatalf("categoryIds = %v, want %s and %s", tagged.CategoryIDs, catA, catB)
	}
	// COALESCE muss das leere Array liefern, nicht NULL — sonst scheitert der Scan.
	if untagged := got[untaggedKB]; untagged.CategoryIDs == nil || len(untagged.CategoryIDs) != 0 {
		t.Fatalf("an unlinked KB must yield an empty (not nil) categoryIds, got %#v", untagged.CategoryIDs)
	}

	// Mit Filter: nur die getaggte KB.
	filtered, err := store.Catalog(ctx, user, "kbsubs-cats-", []string{catA})
	if err != nil {
		t.Fatalf("Catalog(catA): %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != taggedKB {
		t.Fatalf("category filter returned %+v, want only %s", filtered, taggedKB)
	}
}

// TestCatalog_StagedKBIsVisibleToItsMembers pins the second WHERE arm. The
// Favoriten star writes an opt-out and nothing else, so a curator who takes
// their own KB out of Favoriten needs it back here — and a staged KB (public,
// not yet published) has no other surface at all. Without this arm that click
// would be irreversible from the UI.
func TestCatalog_StagedKBIsVisibleToItsMembers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbsubs.NewStore(pool)

	member := insertUser(t, pool, "kbsubs-staged-member")
	stranger := insertUser(t, pool, "kbsubs-staged-stranger")
	staged := insertKB(t, pool, "kbsubs-staged-kb", "", "public", false, false)
	insertMember(t, pool, staged, member, "admin")

	forMember, err := store.Catalog(ctx, member, "kbsubs-staged-", nil)
	if err != nil {
		t.Fatalf("Catalog(member): %v", err)
	}
	entry, ok := byID(forMember)[staged]
	if !ok {
		t.Fatal("a staged public KB must appear in its own member's catalog")
	}
	// Ohne Abo-Zeile steht die KB fuer ein Mitglied in den Favoriten — der
	// Stern muss also gefuellt sein, sonst widerspraeche der Katalog der
	// Uebersicht.
	if !entry.Subscribed {
		t.Error("subscribed = false, want true (a member sees the KB in their overview)")
	}

	forStranger, err := store.Catalog(ctx, stranger, "kbsubs-staged-", nil)
	if err != nil {
		t.Fatalf("Catalog(stranger): %v", err)
	}
	if _, ok := byID(forStranger)[staged]; ok {
		t.Error("a staged public KB must stay out of a non-member's catalog")
	}
}

// TestCatalog_MemberOptOutFlipsTheStar is the round trip the Favoriten star
// depends on: an opt-out beats membership in both the overview query and here,
// so the KB leaves Favoriten, stays listed in the catalog, and comes back on
// the next click. Membership (and with it the caller's chats) is untouched.
func TestCatalog_MemberOptOutFlipsTheStar(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbsubs.NewStore(pool)

	user := insertUser(t, pool, "kbsubs-optout-member")
	kbID := insertKB(t, pool, "kbsubs-optout-kb", "", "public", true, false)
	insertMember(t, pool, kbID, user, "admin")

	if err := store.SetState(ctx, kbID, user, kbsubs.StateOptedOut); err != nil {
		t.Fatalf("SetState(opted_out): %v", err)
	}
	entries, err := store.Catalog(ctx, user, "kbsubs-optout-", nil)
	if err != nil {
		t.Fatalf("Catalog after opt-out: %v", err)
	}
	entry, ok := byID(entries)[kbID]
	if !ok {
		t.Fatal("an un-favorited KB must stay in the catalog")
	}
	if entry.Subscribed {
		t.Error("subscribed = true after opting out, want false")
	}

	if err := store.SetState(ctx, kbID, user, kbsubs.StateSubscribed); err != nil {
		t.Fatalf("SetState(subscribed): %v", err)
	}
	entries, err = store.Catalog(ctx, user, "kbsubs-optout-", nil)
	if err != nil {
		t.Fatalf("Catalog after re-subscribe: %v", err)
	}
	if !byID(entries)[kbID].Subscribed {
		t.Error("subscribed = false after re-subscribing, want true")
	}

	var members int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		kbID, user).Scan(&members); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if members != 1 {
		t.Errorf("kb_members rows = %d, want 1 — the star must not touch membership", members)
	}
}

// TestCatalog_HeaderTextIsSearchedAndDisplayed covers the third ILIKE arm and
// the description fallback. knowledge_bases.description has no editor in the
// UI; header_text is the blurb an admin actually writes, so a catalog that
// ignored it looked like a broken search over the very text on the cards.
func TestCatalog_HeaderTextIsSearchedAndDisplayed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kbsubs.NewStore(pool)

	user := insertUser(t, pool, "kbsubs-header-user")
	headerOnly := insertKB(t, pool, "kbsubs-header-one", "", "public", true, false)
	mustExec(t, pool, `UPDATE knowledge_bases SET header_text = $2 WHERE id = $1::uuid`,
		headerOnly, "alles zum Thema Luftschiff")
	// description gewinnt, wenn beide gesetzt sind.
	both := insertKB(t, pool, "kbsubs-header-two", "aus der Beschreibung", "public", true, false)
	mustExec(t, pool, `UPDATE knowledge_bases SET header_text = $2 WHERE id = $1::uuid`,
		both, "aus dem Kopftext")

	hits, err := store.Catalog(ctx, user, "luftschiff", nil)
	if err != nil {
		t.Fatalf("Catalog(luftschiff): %v", err)
	}
	entry, ok := byID(hits)[headerOnly]
	if !ok {
		t.Fatal("a header_text match is missing from the search results")
	}
	if entry.Description == nil || *entry.Description != "alles zum Thema Luftschiff" {
		t.Errorf("description = %v, want the header_text fallback", entry.Description)
	}

	all, err := store.Catalog(ctx, user, "kbsubs-header-", nil)
	if err != nil {
		t.Fatalf("Catalog(all): %v", err)
	}
	if d := byID(all)[both].Description; d == nil || *d != "aus der Beschreibung" {
		t.Errorf("description = %v, want the explicit description to win over header_text", d)
	}
}
