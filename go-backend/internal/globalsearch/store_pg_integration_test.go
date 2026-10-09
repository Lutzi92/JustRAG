//go:build integration

// Integration tests for the search store.
//
// The central test, TestVisibilityAgreesWithEffectiveRole, is the parity test
// for kbaccess.VisibleKBsCTE, the SQL mirror of kbaccess.EffectiveRole: the
// SQL predicate (exercised through the search queries) and EffectiveRole run
// over the SAME fixture matrix and must agree on every (caller, topic) pair —
// on visibility and on the role. It lives here rather than in
// internal/kbaccess because this package is in CI's integration package list.
//
// Oracle: the production access path of every KB-scoped route, i.e. what
// kbaccess.Middleware.RequireKBRole does — kbaccess.PGStore.GetKBByID plus
// GetKBRole feeding kbaccess.EffectiveRole. It is independent of the code
// under test: it is the Go ladder itself, reading the rows through its own
// queries (PGStore.GetKBByID, GetKBRole), not through the CTE. No expected value in this
// file comes from the search queries themselves; the only thing search
// contributes is the observation being checked.
//
// The remaining tests pin the matching semantics (LIKE escaping, case
// folding, snippet, order, limit, and fuzzy matching: the typo table,
// fuzzy-after-literal ranking, and EXPLAIN evidence that the trigram indexes
// are usable) against expectations written from the API contract (API.md)
// and the doc comments in store_pg.go / match.go, not from query output.
//
// Require a live main Postgres; skipped when DB_* env is unset. Every row is
// created with a per-run random marker and removed in t.Cleanup, so the suite
// neither depends on nor disturbs other data in the database.
//
// HEAVY TESTS ARE OPT-IN. TestSearchQueriesUseTrigramIndexes (here, 200 000
// files) and TestMessageSearchUsesFullTextIndex (chats_integration_test.go,
// 100 000 messages) bulk-load rows, ANALYZE whole tables and assert on
// EXPLAIN text. They skip unless JUSTRAG_PLAN_TESTS=1, so CI's shared
// integration database is not loaded with them on every run:
//
//	JUSTRAG_PLAN_TESTS=1 go test -tags integration -count=1 ./internal/globalsearch/...

package globalsearch_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/globalsearch"
	"github.com/justrag/go-backend/internal/kbaccess"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("search tests require DB_* env (main Postgres)")
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

// requirePlanTests skips a heavy planner test unless JUSTRAG_PLAN_TESTS=1.
func requirePlanTests(t *testing.T) {
	t.Helper()
	if os.Getenv("JUSTRAG_PLAN_TESTS") != "1" {
		t.Skip("heavy planner test: set JUSTRAG_PLAN_TESTS=1 to run it")
	}
}

// marker returns a per-run token that no other row in the database contains.
// Hex only: no LIKE metacharacters, so it matches itself literally.
func marker(t *testing.T) string {
	t.Helper()
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "srch" + hex.EncodeToString(b)
}

func insertUser(t *testing.T, pool *pgxpool.Pool, username, sysRole string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', $2) RETURNING id::text`, username, sysRole).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

type kbSpec struct {
	name        string
	visibility  string
	published   bool
	description string
	headerText  string
}

func insertKB(t *testing.T, pool *pgxpool.Pool, s kbSpec) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility, is_published, description, header_text)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, '')) RETURNING id::text`,
		s.name, s.visibility, s.published, s.description, s.headerText).Scan(&id); err != nil {
		t.Fatalf("insert kb %s: %v", s.name, err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

func insertMember(t *testing.T, pool *pgxpool.Pool, kbID, userID, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)`,
		kbID, userID, role); err != nil {
		t.Fatalf("insert member %s on %s: %v", role, kbID, err)
	}
}

// insertFile adds a files row. It needs no cleanup of its own: it goes with
// its topic, through files_kb_id_knowledge_bases_id_fk (ON DELETE CASCADE,
// migration 0000), when insertKB's cleanup deletes the topic.
func insertFile(t *testing.T, pool *pgxpool.Pool, kbID, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO files (kb_id, name, type, status) VALUES ($1::uuid, $2, 'pdf', 'completed')
		RETURNING id::text`, kbID, name).Scan(&id); err != nil {
		t.Fatalf("insert file %s: %v", name, err)
	}
	return id
}

// ---------------------------------------------------------------------------
// The oracle test
// ---------------------------------------------------------------------------

type matrixKB struct {
	id, fileID, key string
	visibility      string
}

type matrixCaller struct {
	caller globalsearch.Caller
	label  string
}

// ladderRule names the EffectiveRole rule a (caller, KB) pair falls under. It
// is used ONLY to prove the matrix covers every rule — never to compute an
// expected value; expected values come from kbaccess alone.
func ladderRule(kb *kbaccess.KnowledgeBase, sysRole, memberRole string) string {
	switch {
	case sysRole == auth.RoleSuperAdmin:
		return "1 superadmin"
	case kbaccess.Valid(memberRole):
		return "2 member " + memberRole
	case kb.IsGlobal && sysRole == auth.RoleAdmin:
		return "3 public+sysadmin"
	case kb.IsGlobal && kb.IsPublished:
		return "4 public+published"
	default:
		return "5 invisible"
	}
}

// TestVisibilityAgreesWithEffectiveRole builds, for each of four callers (one
// per system role), 20 topics: {private, public} x {published, staged} x
// {no row, view, edit, admin, owner} on that caller. Every caller is then
// checked against all 80 topics, so each also sees the 60 topics on which it
// has no row but somebody else does (owner included). That is 320 pairs, each
// asserted twice:
//
//   - Unscoped, with the topic's own key as q. Search is fuzzy, and the keys
//     share a long per-run marker, that query also returns many
//     OTHER fixture topics as fuzzy hits (measured word_similarity 0.85-0.90
//     between two keys). So the check is over EVERY returned hit, not "exactly
//     one": each topic and each source's topic must be visible under the
//     oracle with the oracle's role, the target must be present exactly when
//     the oracle makes it visible, and no invisible topic may ever appear.
//   - Scoped to the topic via kb_id, with q = the marker: exactly that topic
//     and its file, or ErrNotFound.
func TestVisibilityAgreesWithEffectiveRole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	access := kbaccess.NewStore(pool)
	m := marker(t)

	sysRoles := []string{auth.RoleUser, auth.RoleAPIUser, auth.RoleAdmin, auth.RoleSuperAdmin}
	memberRoles := []string{"", kbaccess.RoleView, kbaccess.RoleEdit, kbaccess.RoleAdmin, kbaccess.RoleOwner}

	var callers []matrixCaller
	var kbs []matrixKB
	n := 0
	for _, sysRole := range sysRoles {
		uid := insertUser(t, pool, fmt.Sprintf("%s-%s", m, sysRole), sysRole)
		callers = append(callers, matrixCaller{
			caller: globalsearch.Caller{UserID: uid, SysRole: sysRole},
			label:  sysRole,
		})
		for _, vis := range []string{"private", "public"} {
			for _, published := range []bool{true, false} {
				for _, mr := range memberRoles {
					n++
					// Zero-padded and hyphen-terminated, so the key of k001
					// is not a substring of k0010's.
					key := fmt.Sprintf("%s-k%03d-", m, n)
					id := insertKB(t, pool, kbSpec{name: key + "topic", visibility: vis, published: published})
					if mr != "" {
						insertMember(t, pool, id, uid, mr)
					}
					kbs = append(kbs, matrixKB{
						id: id, key: key, visibility: vis,
						fileID: insertFile(t, pool, id, key+"file.pdf"),
					})
				}
			}
		}
	}

	coverage := map[string]int{}
	visible := 0
	judged, fuzzyJudged := 0, 0
	for _, c := range callers {
		// oracleRole resolves any KB the search returns — fixture or not —
		// through the oracle, cached per caller.
		cache := map[string]string{}
		oracleRole := func(kbID string) string {
			if role, ok := cache[kbID]; ok {
				return role
			}
			kb, err := access.GetKBByID(ctx, kbID)
			if err != nil || kb == nil {
				t.Fatalf("oracle GetKBByID(%s): %v, %v", kbID, kb, err)
			}
			memberRole, err := access.GetKBRole(ctx, kbID, c.caller.UserID)
			if err != nil {
				t.Fatalf("oracle GetKBRole: %v", err)
			}
			role := kbaccess.EffectiveRole(kb, c.caller.SysRole, memberRole)
			cache[kbID] = role
			return role
		}
		for _, k := range kbs {
			// --- oracle: the RequireKBRole path, verbatim ---
			kb, err := access.GetKBByID(ctx, k.id)
			if err != nil || kb == nil {
				t.Fatalf("oracle GetKBByID(%s): %v, %v", k.id, kb, err)
			}
			memberRole, err := access.GetKBRole(ctx, k.id, c.caller.UserID)
			if err != nil {
				t.Fatalf("oracle GetKBRole: %v", err)
			}
			want := kbaccess.EffectiveRole(kb, c.caller.SysRole, memberRole)
			coverage[ladderRule(kb, c.caller.SysRole, memberRole)]++
			if want != "" {
				visible++
			}

			pair := fmt.Sprintf("caller=%s kb=%s (public=%v published=%v member=%q) want role %q",
				c.label, k.key, kb.IsGlobal, kb.IsPublished, memberRole, want)

			// --- unscoped: the key finds this topic and file exactly (as a
			// prefix), plus fuzzy hits on other topics; every hit is judged ---
			got, err := store.Search(ctx, c.caller, globalsearch.Query{Text: k.key, Limit: globalsearch.MaxLimit})
			if err != nil {
				t.Fatalf("%s: unscoped Search: %v", pair, err)
			}
			assertEveryHitVisible(t, pair+" [unscoped]", got, k, want, oracleRole)
			judged += len(got.Topics) + len(got.Sources)
			for _, h := range got.Topics {
				if h.Match == globalsearch.MatchFuzzy {
					fuzzyJudged++
				}
			}
			for _, h := range got.Sources {
				if h.Match == globalsearch.MatchFuzzy {
					fuzzyJudged++
				}
			}

			// --- scoped to the topic itself, with a query matching every
			// fixture row: only the scope can narrow it down ---
			got, err = store.Search(ctx, c.caller, globalsearch.Query{Text: m, KBID: k.id, Limit: globalsearch.MaxLimit})
			if want == "" {
				if !errors.Is(err, globalsearch.ErrNotFound) {
					t.Errorf("%s [scoped]: err = %v, want ErrNotFound", pair, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s [scoped]: Search: %v", pair, err)
			}
			assertAgreement(t, pair+" [scoped]", got, k, want)
			if got.KBID == nil || *got.KBID != k.id {
				t.Errorf("%s [scoped]: response kbId = %v, want %s", pair, got.KBID, k.id)
			}
		}
	}

	// The matrix must not be vacuous: every rung of the ladder, and each
	// membership role under rule 2, has to have been exercised.
	for _, rule := range []string{
		"1 superadmin",
		"2 member view", "2 member edit", "2 member admin", "2 member owner",
		"3 public+sysadmin", "4 public+published", "5 invisible",
	} {
		if coverage[rule] == 0 {
			t.Errorf("fixture matrix never exercised ladder rule %q", rule)
		}
	}
	// The hit-by-hit check must actually have judged fuzzy hits, or it would
	// prove nothing about fuzzy matching's visibility.
	if fuzzyJudged == 0 {
		t.Error("no unscoped search returned a fuzzy hit; the every-hit check was vacuous")
	}
	t.Logf("pairs=%d visible=%d invisible=%d coverage=%v",
		len(callers)*len(kbs), visible, len(callers)*len(kbs)-visible, coverage)
	t.Logf("unscoped hits judged against the oracle: %d (fuzzy: %d)", judged, fuzzyJudged)
}

// assertEveryHitVisible checks an unscoped response hit by hit against the
// oracle: every topic carries exactly the oracle's (non-empty) role for it,
// every source lies in a topic the oracle makes visible, and the target topic
// and its file are present exactly when the oracle makes the target visible.
func assertEveryHitVisible(t *testing.T, pair string, got *globalsearch.Response, k matrixKB, want string,
	oracleRole func(string) string) {
	t.Helper()
	targetTopic, targetFile := false, false
	for _, h := range got.Topics {
		role := oracleRole(h.ID)
		if role == "" {
			t.Errorf("%s: invisible topic %s (%q) leaked, match=%s", pair, h.ID, h.Name, h.Match)
			continue
		}
		if h.Role != role {
			t.Errorf("%s: topic %q: SQL role %q disagrees with EffectiveRole %q", pair, h.Name, h.Role, role)
		}
		if h.ID == k.id {
			targetTopic = true
			if h.Match != globalsearch.MatchPrefix {
				t.Errorf("%s: target topic match = %q, want %q", pair, h.Match, globalsearch.MatchPrefix)
			}
		}
	}
	for _, h := range got.Sources {
		if oracleRole(h.KBID) == "" {
			t.Errorf("%s: file %q of invisible topic %s leaked, match=%s", pair, h.Name, h.KBID, h.Match)
		}
		if h.ID == k.fileID {
			targetFile = true
		}
	}
	if visible := want != ""; targetTopic != visible || targetFile != visible {
		t.Errorf("%s: target topic present=%v, file present=%v, want both %v",
			pair, targetTopic, targetFile, visible)
	}
}

// assertAgreement checks one search response against the oracle's verdict for
// one fixture topic: invisible -> no hit in any group; visible -> exactly that
// topic, carrying exactly the oracle's role, and exactly its file.
func assertAgreement(t *testing.T, pair string, got *globalsearch.Response, k matrixKB, want string) {
	t.Helper()
	if want == "" {
		if len(got.Topics) != 0 || len(got.Sources) != 0 {
			t.Errorf("%s: invisible topic leaked: topics=%+v sources=%+v", pair, got.Topics, got.Sources)
		}
		return
	}
	if len(got.Topics) != 1 || got.Topics[0].ID != k.id {
		t.Errorf("%s: topics = %+v, want exactly %s", pair, got.Topics, k.id)
		return
	}
	if got.Topics[0].Role != want {
		t.Errorf("%s: SQL role %q disagrees with EffectiveRole %q", pair, got.Topics[0].Role, want)
	}
	if got.Topics[0].Visibility != k.visibility {
		t.Errorf("%s: visibility = %q, want %q", pair, got.Topics[0].Visibility, k.visibility)
	}
	if len(got.Sources) != 1 || got.Sources[0].ID != k.fileID || got.Sources[0].KBID != k.id {
		t.Errorf("%s: sources = %+v, want exactly file %s in %s", pair, got.Sources, k.fileID, k.id)
		return
	}
	if want := k.key + "topic"; got.Sources[0].KBName != want {
		t.Errorf("%s: source kbName = %q, want %q", pair, got.Sources[0].KBName, want)
	}
}

// A kb_id that exists nowhere is ErrNotFound for everyone, superadmin
// included: rule 1 grants owner on every topic that exists, not on ids.
func TestScopeToMissingKBIsNotFound(t *testing.T) {
	pool := testPool(t)
	store := globalsearch.NewStore(pool)
	m := marker(t)
	uid := insertUser(t, pool, m+"-super", auth.RoleSuperAdmin)

	_, err := store.Search(context.Background(),
		globalsearch.Caller{UserID: uid, SysRole: auth.RoleSuperAdmin},
		globalsearch.Query{Text: m, KBID: uuid.NewString(), Limit: 5})
	if !errors.Is(err, globalsearch.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Matching semantics
// ---------------------------------------------------------------------------

func names(topics []globalsearch.TopicHit) []string {
	out := make([]string, len(topics))
	for i, h := range topics {
		out[i] = h.Name
	}
	return out
}

func sourceNames(sources []globalsearch.SourceHit) []string {
	out := make([]string, len(sources))
	for i, h := range sources {
		out[i] = h.Name
	}
	return out
}

// topicMatch / sourceMatch return the match tier of the hit with that name,
// or "" when the name is not among the hits.
func topicMatch(topics []globalsearch.TopicHit, name string) string {
	for _, h := range topics {
		if h.Name == name {
			return h.Match
		}
	}
	return ""
}

func sourceMatch(sources []globalsearch.SourceHit, name string) string {
	for _, h := range sources {
		if h.Name == name {
			return h.Match
		}
	}
	return ""
}

// literal reports whether a tier is one of the two LIKE-based ones.
func literal(match string) bool {
	return match == globalsearch.MatchPrefix || match == globalsearch.MatchSubstring
}

// q is a literal substring: %, _ and \ in the query match only themselves.
// Each case pairs a name the query must match with one an UNescaped pattern
// would also match, so a missing pgxutil.EscapeLike fails here (the catalog
// query in internal/kbsubs has exactly that bug).
//
// The decoys may legitimately come back as FUZZY hits — they
// differ from q by one character and share the per-run marker, measured
// word_similarity 0.87-0.96. What escaping guarantees, and what is asserted,
// is that a decoy is never a prefix or substring hit.
func TestQueryIsMatchedLiterally(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	uid := insertUser(t, pool, m+"-super", auth.RoleSuperAdmin)
	caller := globalsearch.Caller{UserID: uid, SysRole: auth.RoleSuperAdmin}

	cases := []struct {
		q, match, decoy string
	}{
		{m + " pct 100%", m + " pct 100% sicher", m + " pct 1000 sicher"},
		{m + " und a_b", m + " und a_b", m + " und axb"},
		{m + ` bs back\s`, m + ` bs back\slash`, m + " bs backslash"},
	}
	for _, c := range cases {
		kbID := insertKB(t, pool, kbSpec{name: c.match, visibility: "private"})
		insertKB(t, pool, kbSpec{name: c.decoy, visibility: "private"})
		insertFile(t, pool, kbID, c.match+".pdf")
		insertFile(t, pool, kbID, c.decoy+".pdf")

		got, err := store.Search(ctx, caller, globalsearch.Query{Text: c.q, Limit: 20})
		if err != nil {
			t.Fatalf("q=%q: %v", c.q, err)
		}
		if tier := topicMatch(got.Topics, c.match); tier != globalsearch.MatchPrefix {
			t.Errorf("q=%q: topic %q match = %q, want %q", c.q, c.match, tier, globalsearch.MatchPrefix)
		}
		if tier := sourceMatch(got.Sources, c.match+".pdf"); tier != globalsearch.MatchPrefix {
			t.Errorf("q=%q: file %q match = %q, want %q", c.q, c.match+".pdf", tier, globalsearch.MatchPrefix)
		}
		if tier := topicMatch(got.Topics, c.decoy); literal(tier) {
			t.Errorf("q=%q: decoy topic %q is a %s hit; the metacharacter was not escaped", c.q, c.decoy, tier)
		}
		if tier := sourceMatch(got.Sources, c.decoy+".pdf"); literal(tier) {
			t.Errorf("q=%q: decoy file %q is a %s hit; the metacharacter was not escaped", c.q, c.decoy+".pdf", tier)
		}
	}
}

// Matching is case-insensitive (ILIKE), for umlauts too.
func TestQueryIsCaseInsensitive(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	uid := insertUser(t, pool, m+"-super", auth.RoleSuperAdmin)
	caller := globalsearch.Caller{UserID: uid, SysRole: auth.RoleSuperAdmin}

	name := m + " Prüfungsordnung"
	fileName := strings.ToUpper(m) + "-PRÜFUNG.pdf"
	kbID := insertKB(t, pool, kbSpec{name: name, visibility: "private"})
	insertFile(t, pool, kbID, fileName)

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: strings.ToUpper(m) + " PRÜF", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if tier := topicMatch(got.Topics, name); tier != globalsearch.MatchPrefix {
		t.Errorf("topic %q match = %q, want %q (topics %q)", name, tier, globalsearch.MatchPrefix, names(got.Topics))
	}
	got, err = store.Search(ctx, caller, globalsearch.Query{Text: m + "-prüfung", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if tier := sourceMatch(got.Sources, fileName); tier != globalsearch.MatchPrefix {
		t.Errorf("file %q match = %q, want %q (sources %q)", fileName, tier, globalsearch.MatchPrefix, sourceNames(got.Sources))
	}
}

// Topics match on description and header_text too (the catalog's three
// columns) — by literal substring only, never fuzzily — and the snippet is
// description, falling back to header_text, whitespace-collapsed and cut at
// 160 characters with an ellipsis.
func TestTopicDescriptionMatchAndSnippet(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	uid := insertUser(t, pool, m+"-super", auth.RoleSuperAdmin)
	caller := globalsearch.Caller{UserID: uid, SysRole: auth.RoleSuperAdmin}

	long := strings.Repeat("abcdefghij", 20) // 200 characters
	insertKB(t, pool, kbSpec{name: m + "-desc", visibility: "private",
		description: "Ordnungen für  das\n\nStudium " + m + "-needle"})
	insertKB(t, pool, kbSpec{name: m + "-header", visibility: "private",
		description: "   ", headerText: "  Willkommen\tim " + m + "-needle  "})
	insertKB(t, pool, kbSpec{name: m + "-long", visibility: "private",
		description: m + "-needle " + long})
	insertKB(t, pool, kbSpec{name: m + "-none", visibility: "private"})
	// Description only fuzzily similar to the query ("nedle"): must NOT be
	// found through its description, because description is literal-only.
	// Its name shares nothing with the query.
	insertKB(t, pool, kbSpec{name: "zz-unrelated-" + hexOnlyDigits(m), visibility: "private",
		description: "Ordnungen " + m + "-nedle"})

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: m + "-needle", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	snippets := map[string]*string{}
	for _, h := range got.Topics {
		if h.Match == globalsearch.MatchSubstring {
			snippets[h.Name] = h.Description
		}
	}
	wantLong := (m + "-needle " + long)[:159] + "…" // ASCII, so bytes == characters
	want := map[string]string{
		m + "-desc":   "Ordnungen für das Studium " + m + "-needle",
		m + "-header": "Willkommen im " + m + "-needle",
		m + "-long":   wantLong,
	}
	if len(snippets) != len(want) {
		t.Errorf("substring hits = %v, want exactly the three KBs whose description/header contains the needle", snippets)
	}
	for name, w := range want {
		gotSnip, ok := snippets[name]
		if !ok || gotSnip == nil || *gotSnip != w {
			t.Errorf("%s: snippet = %v, want %q", name, deref(gotSnip), w)
		}
	}
	// -none shares the marker with the query, so it may be a fuzzy hit on
	// its NAME — but never a literal one.
	if tier := topicMatch(got.Topics, m+"-none"); literal(tier) {
		t.Errorf("-none is a %s hit; it contains no needle", tier)
	}
	if tier := topicMatch(got.Topics, "zz-unrelated-"+hexOnlyDigits(m)); tier != "" {
		t.Errorf("a description that is only fuzzily similar matched (%s); description must be literal-only", tier)
	}

	// A topic with neither text: matched by name, snippet null.
	got, err = store.Search(ctx, caller, globalsearch.Query{Text: m + "-none", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range got.Topics {
		if h.Name == m+"-none" {
			found = true
			if h.Match != globalsearch.MatchPrefix || h.Description != nil {
				t.Errorf("-none: match %q description %v, want prefix and null", h.Match, deref(h.Description))
			}
		}
	}
	if !found {
		t.Errorf("-none not found by its own name: %q", names(got.Topics))
	}
}

// hexOnlyDigits replaces every letter of the marker with 'q', leaving at most
// a stray all-digit trigram in common with it, so a name built from it is not
// a fuzzy hit for a query that contains the marker.
func hexOnlyDigits(m string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return 'q'
		}
		return r
	}, m)
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// Order: name starts with q, then name contains q, then description-only,
// then fuzzy-only; ties by case-folded name. Limit applies per group.
func TestOrderAndLimit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	uid := insertUser(t, pool, m+"-super", auth.RoleSuperAdmin)
	caller := globalsearch.Caller{UserID: uid, SysRole: auth.RoleSuperAdmin}

	q := "rk" + m
	order := []kbSpec{
		{name: q + " alpha", visibility: "private"},
		{name: q + " Beta", visibility: "private"},
		{name: "x " + q + " gamma", visibility: "private"},
		{name: "zz-desc-" + m, visibility: "private", description: "about " + q},
	}
	// Inserted in reverse so insertion order cannot pass for the sort.
	for i := len(order) - 1; i >= 0; i-- {
		id := insertKB(t, pool, order[i])
		insertFile(t, pool, id, order[i].name+".pdf")
	}

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: q, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{order[0].name, order[1].name, order[2].name, order[3].name}
	wantTiers := []string{globalsearch.MatchPrefix, globalsearch.MatchPrefix, globalsearch.MatchSubstring, globalsearch.MatchSubstring}
	if gotNames := names(got.Topics); strings.Join(gotNames, "|") != strings.Join(want, "|") {
		t.Errorf("topic order = %q, want %q", gotNames, want)
	} else {
		for i, h := range got.Topics {
			if h.Match != wantTiers[i] {
				t.Errorf("topic %q match = %q, want %q", h.Name, h.Match, wantTiers[i])
			}
		}
	}
	// Files: the fourth KB's file name does not contain q, so the three
	// literal hits come first, prefix before substring; anything after them
	// may only be fuzzy (the fourth file shares the marker with q).
	wantFiles := []string{order[0].name + ".pdf", order[1].name + ".pdf", order[2].name + ".pdf"}
	wantFileTiers := []string{globalsearch.MatchPrefix, globalsearch.MatchPrefix, globalsearch.MatchSubstring}
	if len(got.Sources) < 3 {
		t.Fatalf("sources = %q, want at least the three literal hits", sourceNames(got.Sources))
	}
	for i, h := range got.Sources {
		if i < 3 {
			if h.Name != wantFiles[i] || h.Match != wantFileTiers[i] {
				t.Errorf("source %d = %q (%s), want %q (%s)", i, h.Name, h.Match, wantFiles[i], wantFileTiers[i])
			}
		} else if h.Match != globalsearch.MatchFuzzy {
			t.Errorf("source %d = %q (%s), want only fuzzy hits after the literal ones", i, h.Name, h.Match)
		}
	}

	got, err = store.Search(ctx, caller, globalsearch.Query{Text: q, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Topics) != 2 || len(got.Sources) != 2 {
		t.Errorf("limit 2: %d topics, %d sources, want 2 and 2", len(got.Topics), len(got.Sources))
	}
}

// ---------------------------------------------------------------------------
// Fuzzy matching
// ---------------------------------------------------------------------------

// TestTypoOracle is the typo table. Every expected outcome below was
// written down BEFORE the query ran: the positives are typical typos (a
// swapped pair, a dropped letter — a typo must still find its word), the negatives are unrelated
// words of similar length, plus near misses that share only a prefix. None
// of them is derived from what the search returned.
//
// Each case gets its own topic and is searched scoped to it (kb_id), so the
// outcome depends only on that case's name, never on other rows in the
// database — the scope narrows the candidates, it does not change matching.
func TestTypoOracle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	uid := insertUser(t, pool, m+"-super", auth.RoleSuperAdmin)
	caller := globalsearch.Caller{UserID: uid, SysRole: auth.RoleSuperAdmin}

	const topic, file = "topic", "file"
	cases := []struct {
		group string // which group the target lives in
		q     string
		name  string
		want  string // expected match tier, "" = must not match
	}{
		// Positives: typo pairs.
		{topic, "Statsitik", "Statistik", globalsearch.MatchFuzzy},
		{topic, "Physk", "Physik", globalsearch.MatchFuzzy},
		{topic, "Rechtwissenschaft", "Rechtswissenschaft", globalsearch.MatchFuzzy},
		{file, "protkoll.pdf", "Protokoll.pdf", globalsearch.MatchFuzzy},
		// A typo inside a longer name: word similarity looks at the best
		// matching extent, not the whole string.
		{topic, "Statsitik", "Einführung in die Statistik", globalsearch.MatchFuzzy},
		// Exact still wins over fuzzy.
		{topic, "Statistik", "Statistik", globalsearch.MatchPrefix},
		{file, "protokoll", "Sitzungsprotokoll.pdf", globalsearch.MatchSubstring},
		// Negatives: unrelated words of similar length.
		{topic, "Statsitik", "Sportmedizin", ""},
		{topic, "Physk", "Chemie", ""},
		{topic, "Rechtwissenschaft", "Agrarökonomie", ""},
		{file, "protkoll.pdf", "Rechnung.pdf", ""},
		// Near misses: same first letters, different word.
		{topic, "Statsitik", "Stadtplanung", ""},
		{topic, "Physk", "Philosophie", ""},
	}
	for _, c := range cases {
		kbName := c.name
		if c.group == file {
			kbName = hexOnlyDigits(m) + "-files"
		}
		kbID := insertKB(t, pool, kbSpec{name: kbName, visibility: "private"})
		if c.group == file {
			insertFile(t, pool, kbID, c.name)
		}

		got, err := store.Search(ctx, caller, globalsearch.Query{Text: c.q, KBID: kbID, Limit: 5})
		if err != nil {
			t.Fatalf("q=%q in %q: %v", c.q, c.name, err)
		}
		var tier string
		if c.group == topic {
			tier = topicMatch(got.Topics, c.name)
		} else {
			tier = sourceMatch(got.Sources, c.name)
		}
		if tier != c.want {
			t.Errorf("q=%q vs %s %q: match = %q, want %q", c.q, c.group, c.name, tier, orNone(c.want))
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "<no match>"
	}
	return s
}

// Ranking: exact-prefix, substring and fuzzy-only hits side by side. Tier
// order is fixed by the API contract. Within the fuzzy tier the order
// must follow pg_trgm's own word_similarity, read here with a separate raw
// query — pg_trgm is the oracle for "more similar", not the search SQL.
func TestFuzzyRanksAfterLiteral(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	uid := insertUser(t, pool, m+"-super", auth.RoleSuperAdmin)
	caller := globalsearch.Caller{UserID: uid, SysRole: auth.RoleSuperAdmin}

	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-rank", visibility: "private"})
	files := map[string]string{
		"Statistik 2024.pdf":       globalsearch.MatchPrefix,
		"Angewandte Statistik.pdf": globalsearch.MatchSubstring,
		"Statsitik Übung.pdf":      globalsearch.MatchFuzzy,
		"Statstik Skript.pdf":      globalsearch.MatchFuzzy,
	}
	// Inserted in an order that is neither the expected one nor alphabetical.
	for _, name := range []string{"Statstik Skript.pdf", "Statistik 2024.pdf", "Statsitik Übung.pdf", "Angewandte Statistik.pdf"} {
		insertFile(t, pool, kbID, name)
	}

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: "Statistik", KBID: kbID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != len(files) {
		t.Fatalf("sources = %q, want all four", sourceNames(got.Sources))
	}
	if got.Sources[0].Name != "Statistik 2024.pdf" || got.Sources[1].Name != "Angewandte Statistik.pdf" {
		t.Errorf("order = %q, want the prefix hit, then the substring hit, then the fuzzy ones", sourceNames(got.Sources))
	}
	for _, h := range got.Sources {
		if files[h.Name] != h.Match {
			t.Errorf("%q: match = %q, want %q", h.Name, h.Match, files[h.Name])
		}
	}

	// Fuzzy tier: descending word_similarity, as pg_trgm computes it.
	simOf := func(name string) float64 {
		var v float64
		if err := pool.QueryRow(ctx, `SELECT word_similarity($1, $2)::float8`, "Statistik", name).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	a, b := got.Sources[2].Name, got.Sources[3].Name
	if simOf(a) < simOf(b) {
		t.Errorf("fuzzy order %q (%.3f) before %q (%.3f); want similarity descending", a, simOf(a), b, simOf(b))
	}
	if simOf(a) == simOf(b) {
		t.Errorf("fixture does not discriminate: both fuzzy hits score %.3f", simOf(a))
	}
}

// TestSearchQueriesUseTrigramIndexes plans the exact production SQL
// (exported via export_test.go) with EXPLAIN and asserts that both match arms
// are answered from the trigram GIN indexes of migration 0084.
//
// Heavy (200 000 rows, ANALYZE files): opt-in via JUSTRAG_PLAN_TESTS=1, see
// the file header.
//
//   - sources: planned NATURALLY. The fixture adds 200 000 files, and the
//     test ANALYZEs files so the planner has real statistics; enable_seqscan
//     stays on. (At 20 000 files the planner still preferred a seq scan,
//     which is the right call for a table that small.)
//   - topics: knowledge_bases is tiny in any test database, and the planner
//     rightly seq-scans a tiny table, so this half sets enable_seqscan = off
//     (transaction-local) to show that the query CAN use the indexes — i.e.
//     that every arm of its OR is index-supported, which is what the
//     description/header_text indexes are for.
func TestSearchQueriesUseTrigramIndexes(t *testing.T) {
	requirePlanTests(t)
	pool := testPool(t)
	ctx := context.Background()
	m := marker(t)
	uid := insertUser(t, pool, m+"-super", auth.RoleSuperAdmin)

	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-bulk", visibility: "private"})
	// Deleted explicitly rather than left to the ON DELETE CASCADE that
	// insertFile relies on: 200 000 rows is the one fixture where a database
	// whose schema lacks that FK would be left with a real mess. Registered
	// after insertKB, so it runs first (t.Cleanup is LIFO).
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM files WHERE kb_id = $1::uuid`, kbID) //nolint:errcheck
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO files (kb_id, name, type, status)
		SELECT $1::uuid, 'bulk-' || md5(g::text) || '.pdf', 'pdf', 'completed'
		FROM generate_series(1, 200000) g`, kbID); err != nil {
		t.Fatalf("bulk files: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE files`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	explain := func(sql string, seqscanOff bool) string {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := globalsearch.SetFuzzyThreshold(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if seqscanOff {
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
				t.Fatal(err)
			}
		}
		q := "protkoll"
		rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+sql,
			uid, auth.RoleSuperAdmin, nil, "%"+q+"%", q+"%", 5, q)
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		defer rows.Close()
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return strings.Join(plan, "\n")
	}

	sourcesPlan := explain(globalsearch.SourcesSQL, false)
	t.Logf("sources plan (enable_seqscan on):\n%s", sourcesPlan)
	if !strings.Contains(sourcesPlan, "Bitmap Index Scan on files_name_trgm_idx") {
		t.Errorf("sources: no bitmap index scan on files_name_trgm_idx with the planner left alone")
	}
	if strings.Contains(sourcesPlan, "Seq Scan on files") {
		t.Errorf("sources: files is still sequentially scanned")
	}

	topicsPlan := explain(globalsearch.TopicsSQL, true)
	t.Logf("topics plan (enable_seqscan off):\n%s", topicsPlan)
	for _, idx := range []string{
		"knowledge_bases_name_trgm_idx",
		"knowledge_bases_description_trgm_idx",
		"knowledge_bases_header_text_trgm_idx",
	} {
		if !strings.Contains(topicsPlan, "Bitmap Index Scan on "+idx) {
			t.Errorf("topics: no bitmap index scan on %s", idx)
		}
	}
	if strings.Contains(topicsPlan, "Seq Scan on knowledge_bases") {
		t.Errorf("topics: knowledge_bases is still sequentially scanned even with enable_seqscan off")
	}
}
