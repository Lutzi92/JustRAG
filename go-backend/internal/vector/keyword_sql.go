package vector

import (
	"fmt"
	"strconv"
	"strings"
)

// KeywordScoringMode selects how the keyword arm scores candidate chunks
// once the WHERE-clause candidate set has been established. The candidate
// set itself (which chunks even qualify) is identical across modes — only
// the score expression differs. See buildKeywordSQL.
type KeywordScoringMode string

const (
	// KeywordScoringTsRank is today's scoring: Postgres's built-in
	// ts_rank(), term-frequency only, no corpus-wide IDF, no document-
	// length normalisation. Default — byte-identical to the pre-Task-6
	// SQL (pinned by TestBuildKeywordSQL_TsRankIsByteStable).
	KeywordScoringTsRank KeywordScoringMode = "ts_rank"
	// KeywordScoringBM25 scores with real BM25 (Robertson/Sparck-Jones
	// IDF + document-length normalisation) using the per-KB corpus
	// statistics Task 5's refresher maintains in the dim-keyed
	// bm25_kb_stats_<dim> / bm25_term_stats_<dim> tables. Falls back to
	// KeywordScoringTsRank per-query when those stats aren't available
	// yet for a KB (see SearchService.bm25ArmAvailability and
	// bm25ModeDecision).
	KeywordScoringBM25 KeywordScoringMode = "bm25"
)

// bm25ModeDecision resolves the effective keyword-arm scoring mode (and,
// when it falls back, the rag_bm25_mode_fallback_total reason label) from
// the configured mode, whether this search wants the simple arm, and the
// two arms' stats-availability facts (SearchService.bm25ArmAvailability).
// Pure — no I/O — so the fallback state machine is unit-tested directly
// (TestBM25ModeDecision) without a DB.
//
// Fix round 1: the original gate only checked the 'lang' arm, so a KB
// with fresh 'lang' stats but a missing/zero 'simple' row would silently
// score the simple arm against an empty corpus while reporting mode=bm25.
// Distinguishing "no_stats" (lang missing — bm25 unusable at all) from
// "no_simple_stats" (lang fine, simple requested but unavailable — falls
// back to ts_rank rather than running bm25 with one arm silently
// contributing nothing) keeps both the metric and the actual query
// behaviour honest.
func bm25ModeDecision(configured KeywordScoringMode, simpleArm, langAvailable, simpleAvailable bool) (mode KeywordScoringMode, fallbackReason string) {
	if configured != KeywordScoringBM25 {
		return configured, ""
	}
	if !langAvailable {
		return KeywordScoringTsRank, "no_stats"
	}
	if simpleArm && !simpleAvailable {
		return KeywordScoringTsRank, "no_simple_stats"
	}
	return KeywordScoringBM25, ""
}

// bm25K1Min, bm25K1Max, bm25BMin, bm25BMax bound the BM25 free parameters.
// k1 controls term-frequency saturation (higher = TF keeps mattering longer
// before saturating); b controls document-length normalisation strength (0 =
// none, 1 = full). Values are never user-controlled end-to-end (they come
// from an admin-set site_config float, clamped again here defensively) but
// the SQL formats them with strconv.FormatFloat, so an out-of-range value
// would otherwise silently produce a nonsensical-but-syntactically-valid
// ranking rather than an error.
const (
	bm25K1Min = 0.5
	bm25K1Max = 3.0
	bm25BMin  = 0.0
	bm25BMax  = 1.0
)

// clampBM25Params clamps k1/b to their documented ranges.
func clampBM25Params(k1, b float64) (float64, float64) {
	switch {
	case k1 < bm25K1Min:
		k1 = bm25K1Min
	case k1 > bm25K1Max:
		k1 = bm25K1Max
	}
	switch {
	case b < bm25BMin:
		b = bm25BMin
	case b > bm25BMax:
		b = bm25BMax
	}
	return k1, b
}

// keywordSQLInput bundles every value buildKeywordSQL needs to render the
// keyword-arm SQL for one search. TableName/KbID/PgConfig/FileIDs/Limit/
// SimpleArm/NodeKindFilter carry the same meaning as the former
// runKeywordSearch parameters of the same name. Mode/Dim/K1/B are Task 6
// additions consumed only when Mode == KeywordScoringBM25.
type keywordSQLInput struct {
	TableName, Query, KbID, PgConfig string
	FileIDs                          []string
	Limit                            int
	SimpleArm                        bool
	NodeKindFilter                   string
	Mode                             KeywordScoringMode
	Dim                              int
	K1, B                            float64
}

// keywordCandidateClause is the mode-agnostic half of the keyword-arm SQL:
// which chunks even qualify as candidates. Built once by
// buildKeywordCandidateClause and consumed by both the ts_rank and bm25
// scoring builders below, so the two modes can never disagree on the
// candidate set for the same input — the WHERE text embedded in either
// SQL string is the literal whereClause field, byte-for-byte.
type keywordCandidateClause struct {
	// whereClause is "<filterClause> AND <matchClause>", the full text
	// that follows the WHERE keyword in both the ts_rank direct SELECT
	// and the bm25 variant's `cand` CTE.
	whereClause string
	// composed is the language-arm tsquery expression (points every
	// regconfig at $2, the caller's PgConfig placeholder).
	composed string
	// composedSimple is the same expression with every regconfig
	// pointed at the simple-arm placeholder; "" when SimpleArm is off.
	composedSimple string
	// simpleConfigParam is the 1-based placeholder index holding the
	// literal "simple" when SimpleArm is on; 0 when it's off.
	simpleConfigParam int
	// args holds every positional argument bound so far, in placeholder
	// order ($1, $2, ...). Callers append further args (e.g. the bm25
	// scoring-lexemes query text) after this slice.
	args []any
}

// keywordArmSettings bundles the per-search BM25 keyword-arm settings that
// Search() resolves exactly once and every downstream BM25 fan-out
// (runPrimarySearches, runMultiQueryBM25Searches, runKeywordSearch, the
// KeywordSearch MCP tool) must reuse unchanged, so a single request can
// never mix scoring modes across its primary/multi-query/step-back/
// sub-query keyword searches. Passed by value — deliberately not threaded
// as four separate parameters through every call site.
type keywordArmSettings struct {
	SimpleArm bool
	Mode      KeywordScoringMode
	Dim       int
	K1, B     float64
}

// buildKeywordCandidateClause extracts phrases/remainder from in.Query and
// composes the filter + tsquery match clauses, exactly reproducing the
// pre-Task-6 runKeywordSearch logic. ok is false when the query has
// neither a remainder nor phrases (nothing to search for) — same early-
// return as before.
func buildKeywordCandidateClause(in keywordSQLInput) (keywordCandidateClause, bool) {
	phrases, remainder := extractQuotedPhrases(in.Query)
	// Email literals: pulled out of remainder and promoted to phrases so
	// phraseto_tsquery exact-matches them (the default text-search parser
	// keeps emails as one token; bare-token tokenisation in
	// keyword_query.go would shred them on `@` and `.`).
	if emails, rest := extractEmailLiterals(remainder); len(emails) > 0 {
		phrases = append(phrases, emails...)
		remainder = rest
	}
	if remainder == "" && len(phrases) == 0 {
		return keywordCandidateClause{}, false
	}

	// Build parameters: kb_id is $1, pgConfig is $2. When the simple-arm
	// is enabled, the literal `simple` regconfig occupies $3 so the two
	// arms (language stemmer vs. surface form) share the SAME query-text
	// placeholders — args binding stays single-bind and the composed
	// expression is generated for arm-1 then string-substituted for
	// arm-2.
	args := []any{in.KbID, in.PgConfig}
	simpleConfigParam := 0 // 0 = arm disabled; else the placeholder index of 'simple'
	nextParam := 3
	if in.SimpleArm {
		args = append(args, "simple")
		simpleConfigParam = nextParam
		nextParam++
	}

	filterClause := "kb_id = $1::uuid"
	if len(in.FileIDs) > 0 {
		filterClause += fmt.Sprintf(" AND file_id = ANY($%d::uuid[])", nextParam)
		args = append(args, in.FileIDs)
		nextParam++
	}
	if in.NodeKindFilter != "" {
		// Phase F eval ablation: restrict the keyword arm to leaves or
		// summaries. Must be applied before the tsvector match
		// composition below since composed query parts use sequential
		// $N indices and we need the placeholder to land here.
		filterClause += fmt.Sprintf(" AND node_kind = $%d::text", nextParam)
		args = append(args, in.NodeKindFilter)
		nextParam++
	}
	filterClause += excludeCommunitySummaryClause(in.NodeKindFilter)

	// websearchClause is the websearch_to_tsquery(...) clause built from
	// the unquoted remainder; it becomes the AND-required alternative of
	// the websearch group below. Empty when remainder is empty.
	var websearchClause string
	var queryParts []string
	if remainder != "" {
		args = append(args, remainder)
		websearchClause = fmt.Sprintf("websearch_to_tsquery($2::regconfig, $%d)", nextParam)
		nextParam++

		// Detected proper-noun phrases ("Eberhard Kurz", "Marcus Enger")
		// get OR-folded into the websearch group as phraseto_tsquery
		// clauses — see the long-form rationale in the pre-extraction
		// history of this function (git blame internal/vector/search_sql.go).
		var detectedClauses []string
		if isEntityAskingQuery(in.Query) {
			for _, p := range extractProperNounPhrases(remainder) {
				args = append(args, p)
				detectedClauses = append(detectedClauses, fmt.Sprintf("phraseto_tsquery($2::regconfig, $%d)", nextParam))
				nextParam++
			}
		}

		orTokens, hasOrTokens := buildOrTokensExpr(remainder)
		var orClause string
		if hasOrTokens {
			args = append(args, orTokens)
			orClause = fmt.Sprintf("to_tsquery($2::regconfig, $%d)", nextParam)
			nextParam++
		}

		// Compose the websearch group: websearch (AND-required),
		// optionally OR'd with the OR-of-tokens recall floor, optionally
		// OR'd with each detected proper-noun phrase. The reranker
		// breaks ties downstream.
		groupAlts := []string{websearchClause}
		if hasOrTokens {
			groupAlts = append(groupAlts, orClause)
		}
		groupAlts = append(groupAlts, detectedClauses...)
		if len(groupAlts) == 1 {
			queryParts = append(queryParts, websearchClause)
		} else {
			queryParts = append(queryParts, parenJoin(groupAlts, " || "))
		}
	}
	for _, p := range phrases {
		args = append(args, p)
		queryParts = append(queryParts, fmt.Sprintf("phraseto_tsquery($2::regconfig, $%d)", nextParam))
		nextParam++
	}

	// If both the remainder and the phrase list produced zero clauses,
	// there is nothing to search for (defensive — the early return above
	// already covers the common case).
	if len(queryParts) == 0 {
		return keywordCandidateClause{}, false
	}

	// Compose with && (AND) at the top level: each phrase clause is
	// required, and the unquoted-remainder sub-clause (itself an
	// OR-union of the AND-required and OR-of-tokens variants) is also
	// required to match something.
	composed := parenJoin(queryParts, " && ")

	// SQL INJECTION INVARIANT: see the historical comment this function
	// was extracted from (search_sql.go, pre-Task-6) — composed contains
	// only $-placeholder numbers and the three whitelisted PG function
	// names (websearch_to_tsquery / to_tsquery / phraseto_tsquery); no
	// user data is embedded directly. All user-controllable values are
	// passed positionally via args and bound by pgx.
	var composedSimple string
	matchClause := fmt.Sprintf("vector_index @@ %s", composed)
	if simpleConfigParam > 0 {
		composedSimple = strings.ReplaceAll(composed, "$2::regconfig", fmt.Sprintf("$%d::regconfig", simpleConfigParam))
		matchClause = fmt.Sprintf("(vector_index @@ %s OR vector_index_simple @@ %s)", composed, composedSimple)
	}

	return keywordCandidateClause{
		whereClause:       fmt.Sprintf("%s AND %s", filterClause, matchClause),
		composed:          composed,
		composedSimple:    composedSimple,
		simpleConfigParam: simpleConfigParam,
		args:              args,
	}, true
}

// buildKeywordSQL renders the complete keyword-arm SQL (candidate WHERE
// clause + scoring expression) for one search, plus its positional args in
// placeholder order. ok is false when the query has neither remainder nor
// phrases — callers should skip the query entirely in that case (mirrors
// the pre-Task-6 early return in runKeywordSearch).
//
// SQL INJECTION INVARIANT: in.TableName is produced exclusively by
// GetVectorTableName(dimensions) (validVectorTable-shaped); the bm25
// variant's kbTable/termTable come from GetBM25KBStatsTableName /
// GetBM25TermStatsTableName(in.Dim), same shape, same trust level — dim
// is an int from config, never user-supplied text. All other
// caller-controllable values are bound positionally via args.
func buildKeywordSQL(in keywordSQLInput) (sql string, args []any, ok bool) {
	cc, ok := buildKeywordCandidateClause(in)
	if !ok {
		return "", nil, false
	}

	if in.Mode == KeywordScoringBM25 {
		return buildBM25KeywordSQL(in, cc)
	}
	return buildTsRankKeywordSQL(in, cc)
}

// buildTsRankKeywordSQL renders the default (today's) scoring mode. Pinned
// byte-for-byte by TestBuildKeywordSQL_TsRankIsByteStable. Wave 7 dropped
// the trailing "* <boost>" factor together with bm25_tiered_boost_enabled;
// with the boost gone the factor was the constant 1, so the scores are
// unchanged.
func buildTsRankKeywordSQL(in keywordSQLInput, cc keywordCandidateClause) (string, []any, bool) {
	var sqlText string
	if cc.simpleConfigParam > 0 {
		sqlText = fmt.Sprintf(`
			SELECT id::text, content, COALESCE(contextual_prefix, ''), metadata::text, file_id::text,
			       (ts_rank(vector_index, %s) + COALESCE(ts_rank(vector_index_simple, %s), 0)) AS score,
			       COALESCE(parent_chunk_id::text, ''),
			       COALESCE(node_kind, 'leaf'),
			       COALESCE(tree_level, 0)
			FROM "%s"
			WHERE %s
			ORDER BY score DESC
			LIMIT %d
		`, cc.composed, cc.composedSimple, in.TableName, cc.whereClause, in.Limit)
	} else {
		sqlText = fmt.Sprintf(`
			SELECT id::text, content, COALESCE(contextual_prefix, ''), metadata::text, file_id::text,
			       ts_rank(vector_index, %s) AS score,
			       COALESCE(parent_chunk_id::text, ''),
			       COALESCE(node_kind, 'leaf'),
			       COALESCE(tree_level, 0)
			FROM "%s"
			WHERE %s
			ORDER BY score DESC
			LIMIT %d
		`, cc.composed, in.TableName, cc.whereClause, in.Limit)
	}
	return sqlText, cc.args, true
}

// bm25ArmCTE renders the qlex/kb/tf/sc CTE quartet for ONE BM25 scoring
// arm (W2-R12: a single helper for both the language and simple arms — no
// second paste of the CTE trio). suffix is "" for the primary (language)
// arm and "_simple" for the second, so the two invocations' CTE names
// never collide. armLiteral is the bm25_kb_stats/bm25_term_stats.arm
// value ('lang' | 'simple'). tsvectorCol is the chunk column this arm's
// lexemes come from (vector_index | vector_index_simple).
// regconfigPlaceholder is the $N placeholder for this arm's regconfig
// (the shared $2 for the language arm, the simple-arm's own placeholder
// for the second). queryPlaceholder is the $N placeholder holding the
// full original query text — both arms tokenize the SAME query text
// against their own regconfig, since the language-stemmed and unstemmed
// lexeme sets differ from the same input (W2-R6). k1Lit/bLit are the
// pre-clamped, strconv.FormatFloat-rendered literals.
func bm25ArmCTE(suffix, armLiteral, tsvectorCol, regconfigPlaceholder, queryPlaceholder, kbTable, termTable, k1Lit, bLit string) string {
	return fmt.Sprintf(`,
		qlex%[1]s AS (SELECT DISTINCT unnest(tsvector_to_array(to_tsvector(%[4]s::regconfig, %[5]s))) AS lexeme),
		kb%[1]s AS (SELECT doc_count, avg_len FROM "%[6]s" WHERE kb_id = $1::uuid AND arm = '%[2]s'),
		tf%[1]s AS (
			SELECT c.id, u.lexeme, COALESCE(array_length(u.positions, 1), 1) AS tf, length(c.%[3]s) AS dl
			FROM cand c CROSS JOIN LATERAL unnest(c.%[3]s) AS u JOIN qlex%[1]s q ON q.lexeme = u.lexeme
		),
		sc%[1]s AS (
			SELECT tf%[1]s.id,
			       SUM( ln(1 + (kb%[1]s.doc_count - COALESCE(ts.doc_count, 0) + 0.5) / (COALESCE(ts.doc_count, 0) + 0.5))
			            * tf%[1]s.tf * (%[8]s + 1) / (tf%[1]s.tf + %[8]s * (1 - %[9]s + %[9]s * tf%[1]s.dl / NULLIF(kb%[1]s.avg_len, 0))) ) AS s
			FROM tf%[1]s CROSS JOIN kb%[1]s
			LEFT JOIN "%[7]s" ts ON ts.kb_id = $1::uuid AND ts.arm = '%[2]s' AND ts.lexeme = tf%[1]s.lexeme
			GROUP BY tf%[1]s.id
		)`, suffix, armLiteral, tsvectorCol, regconfigPlaceholder, queryPlaceholder, kbTable, termTable, k1Lit, bLit)
}

// buildBM25KeywordSQL renders the BM25 scoring mode: same candidate WHERE
// clause as ts_rank (cc.whereClause, embedded verbatim in the `cand` CTE)
// but scores with real BM25 (IDF + document-length normalisation) read
// from the per-KB corpus statistics tables. The full query text is bound
// as one more trailing argument (appended LAST, after every arg
// buildKeywordCandidateClause already bound) so existing placeholder
// numbering is untouched.
func buildBM25KeywordSQL(in keywordSQLInput, cc keywordCandidateClause) (string, []any, bool) {
	k1, b := clampBM25Params(in.K1, in.B)
	k1Lit := strconv.FormatFloat(k1, 'f', -1, 64)
	bLit := strconv.FormatFloat(b, 'f', -1, 64)

	kbTable := GetBM25KBStatsTableName(in.Dim)
	termTable := GetBM25TermStatsTableName(in.Dim)

	args := append([]any{}, cc.args...)
	queryParam := len(args) + 1
	args = append(args, in.Query)
	queryPlaceholder := fmt.Sprintf("$%d", queryParam)

	ctes := bm25ArmCTE("", bm25ArmLang, "vector_index", "$2", queryPlaceholder, kbTable, termTable, k1Lit, bLit)
	scoreExpr := "COALESCE(sc.s, 0)"
	joinExpr := "LEFT JOIN sc ON sc.id = c.id"
	if cc.simpleConfigParam > 0 {
		simpleRegconfig := fmt.Sprintf("$%d", cc.simpleConfigParam)
		ctes += bm25ArmCTE("_simple", bm25ArmSimple, "vector_index_simple", simpleRegconfig, queryPlaceholder, kbTable, termTable, k1Lit, bLit)
		scoreExpr += " + COALESCE(sc_simple.s, 0)"
		joinExpr += " LEFT JOIN sc_simple ON sc_simple.id = c.id"
	}

	// Fix round 1: `cand` used to carry every payload column (content,
	// metadata, ...) and was referenced by tf[/tf_simple] AND the final
	// SELECT, so Postgres materialised the full row for every
	// WHERE-matched chunk before qlex/tf ever pruned anything down to the
	// lexemes that matter. `cand` now carries only what scoring needs
	// (id + the two tsvector columns); `ranked` computes scores and
	// applies ORDER BY/LIMIT over that slim shape, and only the LIMIT-ed
	// winners are joined back to the base table for their payload
	// columns — content/metadata are read once per RETURNED row, not
	// once per candidate.
	sqlText := fmt.Sprintf(`
		WITH cand AS (
			SELECT id, vector_index, vector_index_simple
			FROM "%s" WHERE %s
		)%s,
		ranked AS (
			SELECT c.id, (%s) AS score
			FROM cand c %s
			ORDER BY score DESC
			LIMIT %d
		)
		SELECT t.id::text, t.content, COALESCE(t.contextual_prefix, ''), t.metadata::text, t.file_id::text,
		       ranked.score,
		       COALESCE(t.parent_chunk_id::text, ''), COALESCE(t.node_kind, 'leaf'), COALESCE(t.tree_level, 0)
		FROM ranked
		JOIN "%s" t ON t.id = ranked.id
		ORDER BY ranked.score DESC
	`, in.TableName, cc.whereClause, ctes, scoreExpr, joinExpr, in.Limit, in.TableName)

	return sqlText, args, true
}
