package vector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Keyword-arm SQL diagnostics (Wave-3 Task 7)
// ---------------------------------------------------------------------------
//
// The keyword arm's SQL is assembled at query time from a dozen inputs (KB
// language, simple arm, tiered boost, scoring mode, dim-keyed stats tables,
// k1/b). Reproducing it by hand for an EXPLAIN — the only way to measure what
// the candidate CTE costs at corpus scale — is error-prone and drifts the
// moment the builder changes. RenderKeywordArmSQL renders the REAL builders'
// output for a given KB + query, for BOTH scoring modes, without executing
// anything, so a cost check profiles the statement production actually runs.
//
// Diagnostics only: no caller in the request path uses this.

// RenderedKeywordSQL is one scoring mode's rendered keyword-arm statement.
type RenderedKeywordSQL struct {
	// Mode is the scoring mode this statement was rendered for.
	Mode KeywordScoringMode `json:"mode"`
	// OK mirrors buildKeywordSQL's ok result: false when the query has
	// neither an unquoted remainder nor a quoted phrase, i.e. the keyword
	// arm would be skipped entirely for this query. SQL/ExecutableSQL are
	// empty in that case.
	OK bool `json:"ok"`
	// SQL is the statement exactly as the builder produces it, with $N
	// placeholders.
	SQL string `json:"sql"`
	// Args are the positional arguments in placeholder order, rendered as
	// strings (every argument the keyword builder binds is a string or a
	// []string).
	Args []string `json:"args"`
	// ExecutableSQL is SQL with every placeholder replaced by its quoted
	// literal, so it can be pasted after EXPLAIN (ANALYZE, BUFFERS) in
	// psql. Semantically equivalent but NOT what production sends: a
	// literal-inlined statement is planned with the literals visible,
	// where the pgx path plans a parameterised statement.
	ExecutableSQL string `json:"executable_sql"`
}

// KeywordSQLDiagnostics is the full resolved picture for one (KB, query):
// the settings the keyword arm would run under plus both modes' statements.
type KeywordSQLDiagnostics struct {
	KBID        string  `json:"kb_id"`
	Query       string  `json:"query"`
	TableName   string  `json:"table"`
	Dim         int     `json:"dim"`
	PgConfig    string  `json:"pg_config"`
	Limit       int     `json:"limit"`
	SimpleArm   bool    `json:"simple_arm"`
	TieredBoost bool    `json:"tiered_boost"`
	K1          float64 `json:"k1"`
	B           float64 `json:"b"`
	// ConfiguredMode is the mode this deployment would actually use
	// (before the per-query stats-availability fallback). Both modes are
	// rendered regardless — this records which one is live.
	ConfiguredMode KeywordScoringMode   `json:"configured_mode"`
	Modes          []RenderedKeywordSQL `json:"modes"`
}

// defaultKeywordSQLPrintLimit is the LIMIT used when the caller passes none.
// 50 is the legacy pre-rerank candidate depth for top-k 10 with a reranker
// (max(4×k, 50)) — the value the keyword arm actually runs with on the
// production fixture.
const defaultKeywordSQLPrintLimit = 50

// RenderKeywordArmSQL resolves the KB's chunk table, text-search config and
// keyword-arm settings the same way the query path does, then renders the
// keyword-arm SQL for BOTH scoring modes. It runs no search: the only DB
// round-trips are the table probe (cached), the KB-language lookup (cached)
// and the site-config read (cached).
//
// Deliberately does NOT apply the stats-availability fallback
// (bm25ModeDecision): the point is to see what each builder emits, including
// on a KB whose stats have not been refreshed yet.
func (s *SearchService) RenderKeywordArmSQL(ctx context.Context, kbID, query string, limit int) (KeywordSQLDiagnostics, error) {
	if kbID == "" {
		return KeywordSQLDiagnostics{}, fmt.Errorf("render_keyword_sql: kb_id required")
	}
	if limit <= 0 {
		limit = defaultKeywordSQLPrintLimit
	}

	tableName, err := s.resolveKBChunkTable(ctx, kbID)
	if err != nil {
		return KeywordSQLDiagnostics{}, err
	}
	cfg := s.loadSiteConfigCached(ctx)
	pgConfig := PgTextSearchConfig(s.resolveKBLanguage(ctx, kbID))
	dim := dimFromTableName(tableName)

	in := keywordSQLInput{
		TableName:   tableName,
		Query:       query,
		KbID:        kbID,
		PgConfig:    pgConfig,
		Limit:       limit,
		SimpleArm:   cfg.BM25SimpleArmEnabled,
		TieredBoost: cfg.BM25TieredBoost,
		Dim:         dim,
		K1:          cfg.BM25K1,
		B:           cfg.BM25B,
	}

	return KeywordSQLDiagnostics{
		KBID:           kbID,
		Query:          query,
		TableName:      tableName,
		Dim:            dim,
		PgConfig:       pgConfig,
		Limit:          limit,
		SimpleArm:      in.SimpleArm,
		TieredBoost:    in.TieredBoost,
		K1:             in.K1,
		B:              in.B,
		ConfiguredMode: cfg.BM25ScoringMode,
		Modes:          renderKeywordArmSQLModes(in),
	}, nil
}

// renderKeywordArmSQLModes renders in through both scoring builders. Pure —
// the resolution above is the only I/O — so the "both modes, same candidate
// clause" contract is unit-testable without a DB.
func renderKeywordArmSQLModes(in keywordSQLInput) []RenderedKeywordSQL {
	out := make([]RenderedKeywordSQL, 0, 2)
	for _, mode := range []KeywordScoringMode{KeywordScoringTsRank, KeywordScoringBM25} {
		modeIn := in
		modeIn.Mode = mode
		sqlText, args, ok := buildKeywordSQL(modeIn)
		r := RenderedKeywordSQL{Mode: mode, OK: ok}
		if ok {
			r.SQL = sqlText
			r.Args = formatKeywordSQLArgs(args)
			r.ExecutableSQL = inlineKeywordSQLArgs(sqlText, args)
		}
		out = append(out, r)
	}
	return out
}

// formatKeywordSQLArgs renders the bound arguments for display.
func formatKeywordSQLArgs(args []any) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, fmt.Sprint(a))
	}
	return out
}

// inlineKeywordSQLArgs substitutes every $N placeholder with its quoted SQL
// literal so the statement can be handed to EXPLAIN in psql.
//
// Placeholders are substituted from the HIGHEST index down, so "$1" can never
// eat the "$1" prefix of "$11". Diagnostics-only: the inputs are an operator's
// own query text and their KB id, and the result is printed, never executed by
// this process — but the literals are still escaped properly (single quotes
// doubled) so a query containing an apostrophe produces a runnable statement
// rather than a truncated one.
func inlineKeywordSQLArgs(sqlText string, args []any) string {
	for i := len(args); i >= 1; i-- {
		sqlText = strings.ReplaceAll(sqlText, "$"+strconv.Itoa(i), sqlLiteral(args[i-1]))
	}
	return sqlText
}

// sqlLiteral renders one bound argument as a SQL literal. The keyword builder
// binds only strings and []string (FileIDs); anything else is rendered through
// its default formatting and quoted, which is wrong-but-visible rather than
// silently plausible.
func sqlLiteral(a any) string {
	switch v := a.(type) {
	case string:
		return quoteSQLString(v)
	case []string:
		parts := make([]string, 0, len(v))
		for _, s := range v {
			parts = append(parts, quoteSQLString(s))
		}
		return "ARRAY[" + strings.Join(parts, ",") + "]"
	default:
		return quoteSQLString(fmt.Sprint(v))
	}
}

// quoteSQLString wraps s in single quotes, doubling any it contains.
func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
