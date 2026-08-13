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

// TestCatalog_ListsOnlyPublishedPublicKBs pins the WHERE clause: the catalog is
// the discovery surface for KBs that are live, so a private KB and a public one
// still staged (is_published = false, the state kbvisibility.Publish leaves
// behind) must both stay out of it.
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
