//go:build integration

// Integration tests for the chats and messages groups.
//
// The central test, TestChatsAndMessagesAreOwnChatsOnly, is the privacy
// oracle. Its expected set is computed from two facts that do not come from
// the search SQL: who owns each chat (the fixture inserted it) and whether
// the caller may open the chat's topic (kbaccess.PGStore +
// kbaccess.EffectiveRole, the same independent oracle as
// TestVisibilityAgreesWithEffectiveRole).
// Expected = own chats in topics the oracle makes visible; the result must be
// exactly that set. A second user's chat, in a topic every caller shares and
// containing the exact query text, must never appear.
//
// The other tests pin the message semantics against expectations written
// before the query ran: one hit per chat (the best-ranked), a snippet that is
// safe plain text with our delimiters only, a tsquery that cannot be steered
// by operator syntax in the input, prefix matching, fuzzy chat titles, and
// EXPLAIN evidence that messages_content_fts_idx (migration 0085) is used.
//
// Fixtures write the values production writes: AI messages have
// role = 'ai' (internal/chat), academic research chats
// type = 'academic-research' (internal/academic).
//
// Helpers (testPool, marker, insertUser, insertKB, insertMember, insertFile,
// hexOnlyDigits, requirePlanTests) live in store_pg_integration_test.go. Chats and messages
// are removed with their topic (chats_kb_id → knowledge_bases and
// messages_chat_id → chats, both ON DELETE CASCADE, migration 0000).

package globalsearch_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/globalsearch"
	"github.com/justrag/go-backend/internal/kbaccess"
)

func insertChat(t *testing.T, pool *pgxpool.Pool, kbID, userID, title, chatType string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO chats (kb_id, user_id, title, type) VALUES ($1::uuid, $2::uuid, $3, $4)
		RETURNING id::text`, kbID, userID, title, chatType).Scan(&id); err != nil {
		t.Fatalf("insert chat %q: %v", title, err)
	}
	return id
}

func insertMessage(t *testing.T, pool *pgxpool.Pool, chatID, role, content string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO messages (chat_id, role, content) VALUES ($1::uuid, $2, $3)
		RETURNING id::text`, chatID, role, content).Scan(&id); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	return id
}

func chatIDs(hits []globalsearch.ChatHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ID)
	}
	sort.Strings(out)
	return out
}

func messageChatIDs(hits []globalsearch.MessageHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ChatID)
	}
	sort.Strings(out)
	return out
}

func superCaller(t *testing.T, pool *pgxpool.Pool, m string) globalsearch.Caller {
	t.Helper()
	return globalsearch.Caller{UserID: insertUser(t, pool, m+"-super", auth.RoleSuperAdmin), SysRole: auth.RoleSuperAdmin}
}

// ---------------------------------------------------------------------------
// Privacy oracle
// ---------------------------------------------------------------------------

type fixtureChat struct {
	id, owner, kbID, kbTag string
}

// TestChatsAndMessagesAreOwnChatsOnly: four topics, four users, every user
// owns one chat in every topic, every chat's title and messages contain the
// query text. The topics span the ladder for the callers:
//
//	shared    public + published, the OTHER user and the plain user are members
//	member    private, every user holds a view membership
//	gone      private, nobody is a member (e.g. a membership that was removed —
//	          each user still owns a chat there)
//	staged    public, unpublished, no members
//
// Callers: user, admin, superadmin, and "other" — whose chats are the ones
// that must never show up for anybody else.
func TestChatsAndMessagesAreOwnChatsOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	access := kbaccess.NewStore(pool)
	m := marker(t)

	type user struct {
		label  string
		caller globalsearch.Caller
	}
	users := []user{}
	for _, u := range []struct{ label, sysRole string }{
		{"user", auth.RoleUser}, {"admin", auth.RoleAdmin},
		{"super", auth.RoleSuperAdmin}, {"other", auth.RoleUser},
	} {
		users = append(users, user{u.label, globalsearch.Caller{
			UserID: insertUser(t, pool, m+"-"+u.label, u.sysRole), SysRole: u.sysRole}})
	}
	byLabel := map[string]globalsearch.Caller{}
	for _, u := range users {
		byLabel[u.label] = u.caller
	}

	kbs := map[string]string{
		"shared": insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-shared", visibility: "public", published: true}),
		"member": insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-member", visibility: "private"}),
		"gone":   insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-gone", visibility: "private"}),
		"staged": insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-staged", visibility: "public", published: false}),
	}
	insertMember(t, pool, kbs["shared"], byLabel["other"].UserID, kbaccess.RoleEdit)
	insertMember(t, pool, kbs["shared"], byLabel["user"].UserID, kbaccess.RoleView)
	for _, u := range users {
		insertMember(t, pool, kbs["member"], u.caller.UserID, kbaccess.RoleView)
	}

	// The exact text every chat carries — the other user's included.
	secret := "Haushalt " + m + " vertraulich"
	var chats []fixtureChat
	for _, u := range users {
		for tag, kbID := range kbs {
			id := insertChat(t, pool, kbID, u.caller.UserID, secret+" "+u.label+" "+tag, "chat")
			insertMessage(t, pool, id, "user", "Frage: "+secret)
			insertMessage(t, pool, id, "ai", "Antwort zum "+secret+" für "+u.label)
			chats = append(chats, fixtureChat{id: id, owner: u.label, kbID: kbID, kbTag: tag})
		}
	}

	excludedByVisibility := 0
	for _, u := range users {
		// Expected: own chats in topics the oracle lets this caller open.
		var want []string
		for _, c := range chats {
			if c.owner != u.label {
				continue
			}
			kb, err := access.GetKBByID(ctx, c.kbID)
			if err != nil || kb == nil {
				t.Fatalf("oracle GetKBByID: %v %v", kb, err)
			}
			memberRole, err := access.GetKBRole(ctx, c.kbID, u.caller.UserID)
			if err != nil {
				t.Fatalf("oracle GetKBRole: %v", err)
			}
			if kbaccess.EffectiveRole(kb, u.caller.SysRole, memberRole) != "" {
				want = append(want, c.id)
			} else {
				excludedByVisibility++
			}
		}
		sort.Strings(want)

		for _, q := range []string{secret, m} {
			got, err := store.Search(ctx, u.caller, globalsearch.Query{Text: q, Limit: globalsearch.MaxLimit})
			if err != nil {
				t.Fatalf("%s q=%q: %v", u.label, q, err)
			}
			if g := chatIDs(got.Chats); strings.Join(g, ",") != strings.Join(want, ",") {
				t.Errorf("%s q=%q: chats = %v, want exactly own visible chats %v", u.label, q, g, want)
			}
			if g := messageChatIDs(got.Messages); strings.Join(g, ",") != strings.Join(want, ",") {
				t.Errorf("%s q=%q: message hits in chats %v, want one per own visible chat %v", u.label, q, g, want)
			}
		}

		// Scoped to each topic: the same rule, restricted to that topic; an
		// invisible topic is ErrNotFound, as for topics and sources.
		for tag, kbID := range kbs {
			got, err := store.Search(ctx, u.caller, globalsearch.Query{Text: secret, KBID: kbID, Limit: globalsearch.MaxLimit})
			var wantScoped []string
			for _, c := range chats {
				if c.owner == u.label && c.kbID == kbID && contains(want, c.id) {
					wantScoped = append(wantScoped, c.id)
				}
			}
			if len(wantScoped) == 0 {
				if !errors.Is(err, globalsearch.ErrNotFound) {
					t.Errorf("%s scoped to %s: err = %v, want ErrNotFound", u.label, tag, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s scoped to %s: %v", u.label, tag, err)
			}
			if g := chatIDs(got.Chats); strings.Join(g, ",") != strings.Join(wantScoped, ",") {
				t.Errorf("%s scoped to %s: chats = %v, want %v", u.label, tag, g, wantScoped)
			}
			if g := messageChatIDs(got.Messages); strings.Join(g, ",") != strings.Join(wantScoped, ",") {
				t.Errorf("%s scoped to %s: message chats = %v, want %v", u.label, tag, g, wantScoped)
			}
		}
	}

	// Not vacuous: the other user's chats DO match the query (their owner
	// finds them), and visibility excluded at least one of a caller's own
	// chats (the "gone" topic), so both halves of the rule were exercised.
	if got, err := store.Search(ctx, byLabel["other"], globalsearch.Query{Text: secret, Limit: 20}); err != nil || len(got.Chats) == 0 || len(got.Messages) == 0 {
		t.Fatalf("the other user's own chats do not match the query (%v): the privacy check proves nothing", err)
	}
	if excludedByVisibility == 0 {
		t.Fatal("no own chat was excluded by topic visibility; the fixture does not exercise that half")
	}
	t.Logf("own chats excluded by topic visibility: %d", excludedByVisibility)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Message semantics
// ---------------------------------------------------------------------------

// One hit per chat, and it is the best-ranked message of that chat. Expected
// outcome decided up front: the message that repeats the term three times
// outranks the one that mentions it once, and the message without it is
// never a hit. pg's own ts_rank, read with a separate raw query, confirms
// the fixture really orders that way.
func TestMessageHitIsBestPerChat(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	caller := superCaller(t, pool, m)
	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-best", visibility: "private"})

	term := "zq" + hexOnlyDigits(m) // a token no other row contains
	long := insertChat(t, pool, kbID, caller.UserID, "Lange Unterhaltung", "chat")
	once := insertMessage(t, pool, long, "user", "Erste Frage zu "+term+" bitte")
	best := insertMessage(t, pool, long, "ai", term+" "+term+" "+term+" ist das Thema")
	insertMessage(t, pool, long, "user", "Danke, das war alles")
	insertMessage(t, pool, long, "user", "Noch einmal "+term)
	other := insertChat(t, pool, kbID, caller.UserID, "Andere", "research")
	otherMsg := insertMessage(t, pool, other, "ai", "Bericht: "+term)

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: term, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %+v, want exactly one hit per chat (2)", got.Messages)
	}
	byChat := map[string]globalsearch.MessageHit{}
	for _, h := range got.Messages {
		byChat[h.ChatID] = h
	}
	if byChat[long].ID != best {
		t.Errorf("long chat's hit = %s, want the best-ranked message %s", byChat[long].ID, best)
	}
	if byChat[other].ID != otherMsg || byChat[other].ChatType != "research" || byChat[other].Role != "ai" {
		t.Errorf("other chat's hit = %+v, want message %s (research, ai)", byChat[other], otherMsg)
	}

	var rBest, rOnce float64
	for id, dst := range map[string]*float64{best: &rBest, once: &rOnce} {
		if err := pool.QueryRow(ctx, `SELECT ts_rank(to_tsvector('simple', content), to_tsquery('simple', $2))
			FROM messages WHERE id = $1::uuid`, id, term+":*").Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if rBest <= rOnce {
		t.Fatalf("fixture premise wrong: ts_rank(best)=%.4f <= ts_rank(once)=%.4f", rBest, rOnce)
	}
}

// The snippet is plain text: nothing is HTML-escaped (the client renders
// text nodes), whitespace is collapsed, and the ONLY delimiters in it are
// ts_headline's, around the matched word in its original case. Delimiter
// characters typed into the content are stripped before the snippet is cut.
//
// First run corrected an expectation: I expected markup to come back
// verbatim, but ts_headline drops HTML/XML tag tokens (observed on Postgres
// 18.6: "<script>alert(1)</script>" becomes "alert(1)"). The text between
// tags is kept. That is fine for a snippet and changes nothing for the
// client: other '<' characters (e.g. "a < b") can still occur, so it must
// render text, never HTML.
func TestMessageSnippetIsSafeText(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	caller := superCaller(t, pool, m)
	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-snip", visibility: "private"})

	term := "Zq" + hexOnlyDigits(m)
	chat := insertChat(t, pool, kbID, caller.UserID, "Snippet", "chat")
	insertMessage(t, pool, chat, "ai",
		"Vorher <script>alert(1)</script>\n\n  "+globalsearch.SnippetStart+"fake"+globalsearch.SnippetEnd+
			" und "+term+" danach <b>fett</b>")

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: strings.ToLower(term), Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %+v, want one", got.Messages)
	}
	s := got.Messages[0].Snippet
	t.Logf("snippet: %q", s)
	if n := strings.Count(s, globalsearch.SnippetStart); n != 1 || strings.Count(s, globalsearch.SnippetEnd) != 1 {
		t.Errorf("snippet has %d start / %d end delimiters, want exactly one pair (the injected ones must be stripped)",
			n, strings.Count(s, globalsearch.SnippetEnd))
	}
	if !strings.Contains(s, globalsearch.SnippetStart+term+globalsearch.SnippetEnd) {
		t.Errorf("snippet does not mark the matched word %q in its original case", term)
	}
	if strings.Contains(s, "<script>") || strings.Contains(s, "<b>") {
		t.Errorf("tag tokens came back; ts_headline was expected to drop them")
	}
	if !strings.Contains(s, "alert(1)") || !strings.Contains(s, "fett") {
		t.Errorf("text between tags must be kept")
	}
	if !strings.Contains(s, "fake") {
		t.Errorf("text around the stripped delimiter characters must be kept")
	}
	if strings.ContainsAny(s, "\n\t") || strings.Contains(s, "  ") {
		t.Errorf("whitespace not collapsed")
	}
}

// The query text never reaches tsquery operator syntax, and each term is a
// prefix. Expected outcomes decided up front.
func TestMessageQueryIsNotTsquerySyntax(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	caller := superCaller(t, pool, m)
	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-tsq", visibility: "private"})
	chat := func(content string) string {
		id := insertChat(t, pool, kbID, caller.UserID, "tsq", "chat")
		insertMessage(t, pool, id, "user", content)
		return id
	}
	abc := chat("aaa bbb ccc Einleitung") // contains all three terms
	ac := chat("aaa ccc Einleitung")      // lacks bbb
	brien := chat("Herr O'Brien meinte")  // quote in the content
	stat := chat("Statistik für Anfänger")

	cases := []struct {
		q     string
		wantC []string // expected chat ids with a message hit
	}{
		// Operators are not evaluated: "!bbb" does NOT exclude bbb, "|" does
		// not mean OR — all three terms are required.
		{"aaa & !bbb | (ccc)", []string{abc}},
		// Prefix: as-you-type finds the whole word.
		{"Statis", []string{stat}},
		// Quotes and backslashes are data, not syntax. ("o" is dropped as a
		// short lexeme; "brien" is what is searched.)
		{`O'Brien\`, []string{brien}},
		// Injection-shaped input is just words that match nothing here.
		{`'); DROP TABLE messages; --`, nil},
		// No lexeme at all: nothing, and no error.
		{"!!", nil},
	}
	for _, c := range cases {
		got, err := store.Search(ctx, caller, globalsearch.Query{Text: c.q, KBID: kbID, Limit: 20})
		if err != nil {
			t.Fatalf("q=%q: %v", c.q, err)
		}
		want := append([]string(nil), c.wantC...)
		sort.Strings(want)
		if g := messageChatIDs(got.Messages); strings.Join(g, ",") != strings.Join(want, ",") {
			t.Errorf("q=%q: message hits in chats %v, want %v", c.q, g, want)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE chat_id = ANY($1::uuid[])`,
		[]string{abc, ac, brien, stat}).Scan(&n); err != nil || n != 4 {
		t.Fatalf("messages table state after the injection-shaped query: %d, %v", n, err)
	}
}

// Chat titles match like topic and file names: substring and fuzzy, with the
// tier reported. Expected outcomes decided up front.
func TestChatTitleMatching(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	caller := superCaller(t, pool, m)
	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-titles", visibility: "private"})

	prefix := insertChat(t, pool, kbID, caller.UserID, "Statistik Klausur", "chat")
	substr := insertChat(t, pool, kbID, caller.UserID, "Fragen zur Statistik", "academic-research")
	fuzzy := insertChat(t, pool, kbID, caller.UserID, "Statsitik Übung", "chat")
	insertChat(t, pool, kbID, caller.UserID, "Sportmedizin", "chat")

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: "Statistik", KBID: kbID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ id, match, typ string }{
		{prefix, globalsearch.MatchPrefix, "chat"},
		{substr, globalsearch.MatchSubstring, "academic-research"},
		{fuzzy, globalsearch.MatchFuzzy, "chat"},
	}
	if len(got.Chats) != len(want) {
		t.Fatalf("chats = %+v, want exactly prefix, substring, fuzzy (Sportmedizin must not match)", got.Chats)
	}
	for i, w := range want {
		h := got.Chats[i]
		if h.ID != w.id || h.Match != w.match || h.Type != w.typ || h.KBID != kbID {
			t.Errorf("chat %d = %+v, want id %s match %s type %s", i, h, w.id, w.match, w.typ)
		}
	}
}

// ---------------------------------------------------------------------------
// Index evidence
// ---------------------------------------------------------------------------

// TestMessageSearchUsesFullTextIndex plans the exact production messagesSQL
// with EXPLAIN and asserts a bitmap index scan on messages_content_fts_idx —
// which also proves the query repeats the index expression exactly.
// Planned NATURALLY (no enable_* overrides): the fixture gives the caller
// 400 chats with 250 messages each (100 000 rows), then ANALYZEs messages
// and chats, so the planner has real statistics and a selective term.
//
// Heavy (100 000 rows, ANALYZE messages and chats): opt-in via
// JUSTRAG_PLAN_TESTS=1, see the header of store_pg_integration_test.go.
func TestMessageSearchUsesFullTextIndex(t *testing.T) {
	requirePlanTests(t)
	pool := testPool(t)
	ctx := context.Background()
	m := marker(t)
	caller := superCaller(t, pool, m)
	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-bulk", visibility: "private"})

	if _, err := pool.Exec(ctx, `
		WITH c AS (
			INSERT INTO chats (kb_id, user_id, title)
			SELECT $1::uuid, $2::uuid, 'bulk ' || g FROM generate_series(1, 400) g
			RETURNING id
		)
		INSERT INTO messages (chat_id, role, content)
		SELECT c.id, 'user', 'bulk ' || md5(c.id::text || g::text) || ' ' || md5(g::text)
		FROM c, generate_series(1, 250) g`, kbID, caller.UserID); err != nil {
		t.Fatalf("bulk: %v", err)
	}
	for _, tbl := range []string{"messages", "chats"} {
		if _, err := pool.Exec(ctx, "ANALYZE "+tbl); err != nil {
			t.Fatalf("analyze %s: %v", tbl, err)
		}
	}

	rows, err := pool.Query(ctx, "EXPLAIN (COSTS OFF) "+globalsearch.MessagesSQL,
		caller.UserID, caller.SysRole, nil, "protkoll", 5, globalsearch.SnippetOptions)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan, "\n")
	t.Logf("messages plan (planner left alone):\n%s", text)
	if !strings.Contains(text, "Bitmap Index Scan on messages_content_fts_idx") {
		t.Errorf("no bitmap index scan on messages_content_fts_idx")
	}
	if strings.Contains(text, "Seq Scan on messages") {
		t.Errorf("messages is still sequentially scanned")
	}
}

// An oversize message must still be writable with migration 0085's index in
// place. Without the left(content, 100000) guard, to_tsvector on this
// content raises "string is too long for tsvector" (measured on Postgres
// 18.6: 30 000 distinct 32-character tokens already exceed the 1 MB cap) and
// the INSERT itself would fail — i.e. the chat write path, not just search.
// The first part of the content stays searchable.
func TestOversizeMessageStillInserts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	caller := superCaller(t, pool, m)
	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-big", visibility: "private"})
	chat := insertChat(t, pool, kbID, caller.UserID, "big", "chat")

	term := "zq" + hexOnlyDigits(m)
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO messages (chat_id, role, content)
		SELECT $1::uuid, 'ai', $2 || ' ' || string_agg(md5(g::text), ' ')
		FROM generate_series(1, 40000) g
		RETURNING id::text`, chat, term).Scan(&id); err != nil {
		t.Fatalf("oversize message could not be inserted: %v", err)
	}
	// Premise check: the unguarded expression really would have failed.
	var n int
	err := pool.QueryRow(ctx, `SELECT length(to_tsvector('simple', content)) FROM messages WHERE id = $1::uuid`, id).Scan(&n)
	if err == nil || !strings.Contains(err.Error(), "too long for tsvector") {
		t.Fatalf("fixture premise wrong: unguarded to_tsvector gave %d, %v", n, err)
	}

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: term, KBID: kbID, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 || got.Messages[0].ID != id {
		t.Fatalf("messages = %+v, want the oversize message found by a term in its first 100 000 characters", got.Messages)
	}
}

// ---------------------------------------------------------------------------
// Query-term and snippet cost guards
// ---------------------------------------------------------------------------

// Lexemes shorter than three characters are dropped from the prefix query:
// 'a':* or 'ab':* would expand to a huge share of the GIN index for a term
// that narrows next to nothing. Expected values written up front, and shown
// twice: the tsquery text the production fragment builds, and the search
// outcome.
func TestShortLexemesAreDropped(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	caller := superCaller(t, pool, m)
	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-short", visibility: "private"})

	// The tsquery prefixTSQuery builds, read straight from Postgres.
	for _, c := range []struct {
		q    string
		want *string // nil = NULL (matches nothing)
	}{
		{"a b", nil},
		{"ab cd", nil},
		{"x", nil},
		{"abc d", strPtr("'abc':*")},
		{"abc de", strPtr("'abc':*")},
		{"abc def", strPtr("'abc':* & 'def':*")},
	} {
		var got *string
		if err := pool.QueryRow(ctx, `SELECT `+globalsearch.PrefixTSQuery("$1")+`::text`, c.q).Scan(&got); err != nil {
			t.Fatalf("q=%q: build tsquery: %v", c.q, err)
		}
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("q=%q: tsquery = %v, want %v", c.q, deref(got), deref(c.want))
		}
	}

	chat := func(content string) string {
		id := insertChat(t, pool, kbID, caller.UserID, "short", "chat")
		insertMessage(t, pool, id, "user", content)
		return id
	}
	both := chat("ab cd abx cdx")      // matches 'ab':* & 'cd':*
	abeAndCe := chat("abend ohne cee") // matches abe:* and has a ce-word
	abeNoCe := chat("abend und mehr")  // matches abe:*, no word starting with ce
	chat("xy ce")                      // only a ce-word

	// Premise: the unfiltered two-character query WOULD match `both`, so its
	// absence below is the filter's doing, not the fixture's.
	var premise bool
	if err := pool.QueryRow(ctx, `SELECT to_tsvector('simple', content) @@ to_tsquery('simple', 'ab:* & cd:*')
		FROM messages WHERE chat_id = $1::uuid`, both).Scan(&premise); err != nil || !premise {
		t.Fatalf("fixture premise wrong: 'ab:* & cd:*' does not match the ab-cd message (%v)", err)
	}

	for _, c := range []struct {
		q     string
		wantC []string
	}{
		// Nothing left after dropping: no hits, no error.
		{"ab cd", nil},
		// Only abe:* is searched: "abend und mehr" has no ce-word and is
		// still found, so the ce term was really dropped; "xy ce" is not.
		{"abe ce", []string{abeAndCe, abeNoCe}},
	} {
		got, err := store.Search(ctx, caller, globalsearch.Query{Text: c.q, KBID: kbID, Limit: 20})
		if err != nil {
			t.Fatalf("q=%q: %v", c.q, err)
		}
		want := append([]string(nil), c.wantC...)
		sort.Strings(want)
		if g := messageChatIDs(got.Messages); strings.Join(g, ",") != strings.Join(want, ",") {
			t.Errorf("q=%q: message hits in chats %v, want %v", c.q, g, want)
		}
	}
}

func strPtr(s string) *string { return &s }

// ts_headline must be evaluated only on the final, limited rows —
// structurally, not by planner heuristic. EXPLAIN (VERBOSE) lists each node's
// Output; ts_headline must appear only in nodes ABOVE the Limit, never in
// the Limit node or anywhere in its subtree.
func TestSnippetIsComputedAboveTheLimit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	m := marker(t)
	caller := superCaller(t, pool, m)

	rows, err := pool.Query(ctx, "EXPLAIN (VERBOSE, COSTS OFF) "+globalsearch.MessagesSQL,
		caller.UserID, caller.SysRole, nil, "statistik", 5, globalsearch.SnippetOptions)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("plan:\n%s", strings.Join(plan, "\n"))

	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	limitAt := -1
	for i, line := range plan {
		if trimmed := strings.TrimLeft(line, " "); strings.HasPrefix(trimmed, "->  Limit") || strings.HasPrefix(trimmed, "Limit") {
			limitAt = i
			break
		}
	}
	if limitAt < 0 {
		t.Fatal("no Limit node in the plan")
	}
	above := strings.Join(plan[:limitAt], "\n")
	if !strings.Contains(above, "ts_headline") {
		t.Errorf("ts_headline is not computed in any node above the Limit")
	}
	// The Limit node itself and every line indented deeper than it, up to the
	// next line at the same or a shallower indentation, is its subtree.
	base := indent(plan[limitAt])
	for i := limitAt; i < len(plan); i++ {
		if i > limitAt && indent(plan[i]) <= base {
			break
		}
		if strings.Contains(plan[i], "ts_headline") {
			t.Errorf("ts_headline evaluated at or below the Limit: %q", strings.TrimSpace(plan[i]))
		}
	}
}

// TestLibraryChatsNeverMatch pins that a library chat (type 'library',
// kb_id NULL — user file library phase 3, migration 0081) never appears,
// not even for its own owner and not even for a superadmin: its messages
// answer from private library files, and both groups reach chats only
// through the visible-KB join, which a NULL kb_id never satisfies. A KB
// chat with the same title and message text is the positive control.
func TestLibraryChatsNeverMatch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := globalsearch.NewStore(pool)
	m := marker(t)
	caller := superCaller(t, pool, m)
	kbID := insertKB(t, pool, kbSpec{name: hexOnlyDigits(m) + "-library", visibility: "private"})

	const text = "Zwiebelkuchenrezept Bibliotheksnotiz"
	control := insertChat(t, pool, kbID, caller.UserID, text, "chat")
	insertMessage(t, pool, control, "user", text)

	var library string
	if err := pool.QueryRow(ctx, `
		INSERT INTO chats (kb_id, user_id, title, type) VALUES (NULL, $1::uuid, $2, 'library')
		RETURNING id::text`, caller.UserID, text).Scan(&library); err != nil {
		t.Fatalf("insert library chat: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM chats WHERE id = $1::uuid`, library)
	})
	insertMessage(t, pool, library, "user", text)
	insertMessage(t, pool, library, "ai", text)

	got, err := store.Search(ctx, caller, globalsearch.Query{Text: "Zwiebelkuchenrezept", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if ids := chatIDs(got.Chats); !contains(ids, control) || contains(ids, library) {
		t.Errorf("chat hits = %v, want the KB chat %s and never the library chat %s", ids, control, library)
	}
	if ids := messageChatIDs(got.Messages); !contains(ids, control) || contains(ids, library) {
		t.Errorf("message hits = %v, want the KB chat %s and never the library chat %s", ids, control, library)
	}
}
