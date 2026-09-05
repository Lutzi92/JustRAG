package vector

import (
	"strings"
	"testing"
)

// fixedKeywordInput returns the canonical input TestBuildKeywordSQL_TsRankIsByteStable
// pins and every other builder test in this file derives from — a plain
// natural-language query (no quoted phrases, no emails, not
// entity-asking) against the legacy 1536-dim (bare "document_chunks")
// table.
func fixedKeywordInput() keywordSQLInput {
	return keywordSQLInput{
		TableName: GetVectorTableName(1536),
		Query:     "quick brown fox",
		KbID:      "11111111-1111-1111-1111-111111111111",
		PgConfig:  PgTextSearchConfig("de"),
		Limit:     15,
		Mode:      KeywordScoringTsRank,
	}
}

// TestBuildKeywordSQL_TsRankIsByteStable pins buildKeywordSQL's ts_rank
// (default mode) output for a fixed input to a literal golden copy. This
// is the refactor's own gate (Step 1 of the Task 6 brief): extracting
// runKeywordSearch's inline SQL into buildKeywordSQL must not change a
// single byte of the generated SQL for the default mode. Must PASS before
// any BM25 code exists AND after — the byte-identical guarantee holds
// forever, not just at extraction time.
//
// Mutation: change any literal in the SQL template below (e.g. drop the
// COALESCE around contextual_prefix, or reorder the SELECT list) and this
// test fails immediately — it is a straight string equality, not a
// substring check.
func TestBuildKeywordSQL_TsRankIsByteStable(t *testing.T) {
	t.Parallel()
	const want = `
			SELECT id::text, content, COALESCE(contextual_prefix, ''), metadata::text, file_id::text,
			       (ts_rank(vector_index, ((websearch_to_tsquery($2::regconfig, $3) || to_tsquery($2::regconfig, $4)))) * 1) AS score,
			       COALESCE(parent_chunk_id::text, ''),
			       COALESCE(node_kind, 'leaf'),
			       COALESCE(tree_level, 0)
			FROM "document_chunks"
			WHERE kb_id = $1::uuid AND node_kind <> 'community_summary' AND vector_index @@ ((websearch_to_tsquery($2::regconfig, $3) || to_tsquery($2::regconfig, $4)))
			ORDER BY score DESC
			LIMIT 15
		`

	sql, args, ok := buildKeywordSQL(fixedKeywordInput())
	if !ok {
		t.Fatal("buildKeywordSQL returned ok=false for a non-empty query")
	}
	if sql != want {
		t.Fatalf("ts_rank SQL is not byte-stable.\n--- got ---\n%s\n--- want ---\n%s", sql, want)
	}
	wantArgs := []any{"11111111-1111-1111-1111-111111111111", "german", "quick brown fox", "quick | brown | fox"}
	if len(args) != len(wantArgs) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(wantArgs), args)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Errorf("args[%d] = %#v, want %#v", i, args[i], wantArgs[i])
		}
	}
}

// TestBuildKeywordSQL_TsRank_SingleArm replaces the old hand-written
// TestKeywordSQLShape replica (which built its own local SQL string and
// never called production code) with a call into the real builder.
func TestBuildKeywordSQL_TsRank_SingleArm(t *testing.T) {
	t.Parallel()
	sql, _, ok := buildKeywordSQL(fixedKeywordInput())
	if !ok {
		t.Fatal("expected ok=true")
	}
	for _, want := range []string{
		`"document_chunks"`,
		"ts_rank(vector_index,",
		"websearch_to_tsquery($2::regconfig, $3)",
		"to_tsquery($2::regconfig, $4)",
		" || ",
		"vector_index @@",
		"LIMIT 15",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("ts_rank single-arm SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "vector_index_simple") {
		t.Errorf("single-arm SQL must not reference vector_index_simple:\n%s", sql)
	}
}

// TestBuildKeywordSQL_TsRank_DualArm replaces the old TestKeywordSQLShape_WithPhrase
// replica. Exercises SimpleArm=true AND a quoted phrase together (remainder
// union AND-joined with one phrase), asserting both the dual-arm ts_rank(...)
// terms and the phrase clause appear in the real builder's output.
func TestBuildKeywordSQL_TsRank_DualArm(t *testing.T) {
	t.Parallel()
	in := fixedKeywordInput()
	in.Query = `"exact phrase" remainder text`
	in.SimpleArm = true

	sql, _, ok := buildKeywordSQL(in)
	if !ok {
		t.Fatal("expected ok=true")
	}
	for _, want := range []string{
		"ts_rank(vector_index,",
		"ts_rank(vector_index_simple,",
		"vector_index_simple @@",
		"phraseto_tsquery($2::regconfig,",
		" && ",
		" || ",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("ts_rank dual-arm SQL missing %q:\n%s", want, sql)
		}
	}
}

// ---------------------------------------------------------------------------
// BM25 scoring mode
// ---------------------------------------------------------------------------

func bm25KeywordInput() keywordSQLInput {
	in := fixedKeywordInput()
	in.Mode = KeywordScoringBM25
	in.Dim = 4096
	in.K1 = 1.2
	in.B = 0.75
	return in
}

// TestBuildKeywordSQL_BM25_SingleArmShape asserts the BM25 CTE shape the
// brief specifies: scoring lexemes tokenized from the full query text,
// unnest over the candidate's tsvector, the dim-keyed stats tables, the
// document-length expression, the clamped-and-formatted k1/b literals, and
// a plain ORDER BY score DESC / LIMIT tail.
//
// Mutation (per the brief): point the simple-arm CTE at vector_index
// instead of vector_index_simple — see TestBuildKeywordSQL_BM25_SimpleArmMutation.
func TestBuildKeywordSQL_BM25_SingleArmShape(t *testing.T) {
	t.Parallel()
	sql, args, ok := buildKeywordSQL(bm25KeywordInput())
	if !ok {
		t.Fatal("expected ok=true")
	}
	for _, want := range []string{
		"tsvector_to_array(to_tsvector($2::regconfig, $5))",
		"unnest(c.vector_index)",
		`"bm25_term_stats_4096"`,
		`"bm25_kb_stats_4096"`,
		"length(c.vector_index)",
		"1.2",
		"0.75",
		"ORDER BY score DESC",
		"LIMIT 15",
		"arm = 'lang'",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("bm25 single-arm SQL missing %q:\n%s", want, sql)
		}
	}
	// The `cand` CTE always projects vector_index_simple defensively (it
	// doesn't know in advance whether a later dual-arm CTE will need it),
	// but no qlex_simple/kb_simple/tf_simple/sc_simple CTE should exist
	// for a single-arm query.
	for _, notWant := range []string{"qlex_simple", "kb_simple", "tf_simple", "sc_simple"} {
		if strings.Contains(sql, notWant) {
			t.Errorf("single-arm bm25 SQL must not contain %q:\n%s", notWant, sql)
		}
	}
	// The scoring-lexemes query text is bound as the LAST argument, after
	// every arg the candidate clause already bound.
	if len(args) == 0 || args[len(args)-1] != "quick brown fox" {
		t.Errorf("expected last bound arg to be the full query text, got %#v", args)
	}
}

// TestBuildKeywordSQL_BM25_DualArmShape asserts the simple-arm CTE trio
// (suffix "_simple") is rendered via the SAME bm25ArmCTE helper (W2-R12) —
// not a second hand-pasted copy — by checking its distinguishing tokens:
// vector_index_simple, arm = 'simple', and the sc_simple contribution
// folded additively into the final score expression.
func TestBuildKeywordSQL_BM25_DualArmShape(t *testing.T) {
	t.Parallel()
	in := bm25KeywordInput()
	in.SimpleArm = true

	sql, _, ok := buildKeywordSQL(in)
	if !ok {
		t.Fatal("expected ok=true")
	}
	for _, want := range []string{
		"unnest(c.vector_index_simple)",
		"length(c.vector_index_simple)",
		"arm = 'simple'",
		"qlex_simple",
		"kb_simple",
		"tf_simple",
		"sc_simple",
		"COALESCE(sc_simple.s, 0)",
		"LEFT JOIN sc_simple ON sc_simple.id = c.id",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("bm25 dual-arm SQL missing %q:\n%s", want, sql)
		}
	}
}

// TestBuildKeywordSQL_BM25_SimpleArmMutation is the named mutation from the
// brief: point the simple-arm CTE at vector_index (the language column)
// instead of vector_index_simple. Verifies the test above actually catches
// it by re-deriving the dual-arm SQL with the mutation applied inline and
// checking the resulting string no longer satisfies the real assertion —
// i.e. this test documents WHY TestBuildKeywordSQL_BM25_DualArmShape's
// "unnest(c.vector_index_simple)" / "length(c.vector_index_simple)"
// assertions are load-bearing, not incidental.
func TestBuildKeywordSQL_BM25_SimpleArmMutation(t *testing.T) {
	t.Parallel()
	mutated := bm25ArmCTE("_simple", bm25ArmSimple, "vector_index" /* BUG: should be vector_index_simple */, "$3", "$6", "bm25_kb_stats_4096", "bm25_term_stats_4096", "1.2", "0.75")
	if strings.Contains(mutated, "vector_index_simple") {
		t.Fatal("test setup error: mutated CTE unexpectedly still references vector_index_simple")
	}
	if !strings.Contains(mutated, "unnest(c.vector_index)") {
		t.Fatal("test setup error: mutated CTE should fall back to the language column")
	}
}

// TestBuildKeywordSQL_ModesShareCandidateSet is the brief's Step 2
// requirement: the WHERE-clause candidate set must be identical between
// ts_rank and bm25 for the same input — only the scoring expression
// differs with the mode. Verified by calling the real shared helper
// (buildKeywordCandidateClause, which both scoring builders consume) and
// checking its whereClause text appears verbatim in both generated SQL
// strings — not by re-deriving the clause independently, which would risk
// silently testing a hand-written replica instead of production code.
func TestBuildKeywordSQL_ModesShareCandidateSet(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   keywordSQLInput
	}{
		{"single-arm", fixedKeywordInput()},
		{"dual-arm", func() keywordSQLInput { in := fixedKeywordInput(); in.SimpleArm = true; return in }()},
		{"with-phrase", func() keywordSQLInput {
			in := fixedKeywordInput()
			in.Query = `"exact phrase" remainder text`
			return in
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cc, ok := buildKeywordCandidateClause(tc.in)
			if !ok {
				t.Fatal("expected ok=true")
			}

			tsIn := tc.in
			tsIn.Mode = KeywordScoringTsRank
			tsSQL, _, ok := buildKeywordSQL(tsIn)
			if !ok {
				t.Fatal("expected ok=true (ts_rank)")
			}

			bmIn := tc.in
			bmIn.Mode = KeywordScoringBM25
			bmIn.Dim = 4096
			bmIn.K1, bmIn.B = 1.2, 0.75
			bmSQL, _, ok := buildKeywordSQL(bmIn)
			if !ok {
				t.Fatal("expected ok=true (bm25)")
			}

			if !strings.Contains(tsSQL, cc.whereClause) {
				t.Errorf("ts_rank SQL does not contain the shared WHERE clause %q:\n%s", cc.whereClause, tsSQL)
			}
			if !strings.Contains(bmSQL, cc.whereClause) {
				t.Errorf("bm25 SQL does not contain the shared WHERE clause %q:\n%s", cc.whereClause, bmSQL)
			}
		})
	}
}

// TestBuildKeywordSQL_TieredBoostAppliesInBothModes is W2-R7: the tiered
// boost multiplier applies identically regardless of scoring mode (only
// the column reference it tests against differs: bare vector_index for
// ts_rank's unaliased FROM, c.vector_index for bm25's aliased `cand c`).
func TestBuildKeywordSQL_TieredBoostAppliesInBothModes(t *testing.T) {
	t.Parallel()

	tsIn := fixedKeywordInput()
	tsIn.TieredBoost = true
	tsSQL, _, ok := buildKeywordSQL(tsIn)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(tsSQL, "CASE WHEN vector_index @@") {
		t.Errorf("ts_rank tiered-boost CASE missing:\n%s", tsSQL)
	}

	bmIn := bm25KeywordInput()
	bmIn.TieredBoost = true
	bmSQL, _, ok := buildKeywordSQL(bmIn)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(bmSQL, "CASE WHEN c.vector_index @@") {
		t.Errorf("bm25 tiered-boost CASE missing:\n%s", bmSQL)
	}
}

// TestBuildKeywordSQL_EmptyQueryReturnsNotOK mirrors the pre-Task-6 early
// return: a query with neither a remainder nor phrases means nothing to
// search for.
func TestBuildKeywordSQL_EmptyQueryReturnsNotOK(t *testing.T) {
	t.Parallel()
	in := fixedKeywordInput()
	in.Query = "   "
	_, _, ok := buildKeywordSQL(in)
	if ok {
		t.Fatal("expected ok=false for a query with no remainder and no phrases")
	}
}

// TestClampBM25Params guards the "never user-controlled, but clamp anyway"
// invariant. Mutation: remove either branch and an out-of-range k1/b would
// format straight into the SQL text.
func TestClampBM25Params(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		k1, b         float64
		wantK1, wantB float64
	}{
		{"in range", 1.2, 0.75, 1.2, 0.75},
		{"k1 too low", 0.1, 0.75, bm25K1Min, 0.75},
		{"k1 too high", 10, 0.75, bm25K1Max, 0.75},
		{"b too low", 1.2, -1, 1.2, bm25BMin},
		{"b too high", 1.2, 5, 1.2, bm25BMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotK1, gotB := clampBM25Params(tc.k1, tc.b)
			if gotK1 != tc.wantK1 || gotB != tc.wantB {
				t.Errorf("clampBM25Params(%v, %v) = (%v, %v), want (%v, %v)", tc.k1, tc.b, gotK1, gotB, tc.wantK1, tc.wantB)
			}
		})
	}
}
