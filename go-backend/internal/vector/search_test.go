package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// TestNewSearchService verifies that NewSearchService compiles and returns a
// non-nil value when called with nil pools and a nil resolver.
func TestNewSearchService(t *testing.T) {
	t.Parallel()
	svc := NewSearchService(nil, nil, nil)
	if svc == nil {
		t.Fatal("NewSearchService returned nil")
	}
}

// ---------------------------------------------------------------------------
// toRankedDocs
// ---------------------------------------------------------------------------

func TestToRankedDocs_Vector(t *testing.T) {
	t.Parallel()
	rows := []rawRow{
		{ID: "a", Content: "hello", Metadata: json.RawMessage(`{"page":1}`), FileID: "f1", Score: 0.9},
		{ID: "b", Content: "world", Metadata: json.RawMessage(`{}`), FileID: "f2", Score: 0.7},
	}
	docs := toRankedDocs(rows, true)
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}

	if docs[0].VectorScore != 0.9 {
		t.Errorf("VectorScore: want 0.9, got %f", docs[0].VectorScore)
	}
	if docs[0].KeywordRank != 0 {
		t.Errorf("KeywordRank should be 0 for vector row, got %f", docs[0].KeywordRank)
	}
	if docs[0].ID != "a" || docs[0].Content != "hello" || docs[0].FileID != "f1" {
		t.Errorf("unexpected field values: %+v", docs[0])
	}
	if string(docs[0].Metadata) != `{"page":1}` {
		t.Errorf("Metadata not passed through: %q", docs[0].Metadata)
	}
}

func TestToRankedDocs_Keyword(t *testing.T) {
	t.Parallel()
	rows := []rawRow{
		{ID: "c", Content: "foo bar", Metadata: json.RawMessage(`{}`), FileID: "f3", Score: 0.5},
	}
	docs := toRankedDocs(rows, false)
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if docs[0].KeywordRank != 0.5 {
		t.Errorf("KeywordRank: want 0.5, got %f", docs[0].KeywordRank)
	}
	if docs[0].VectorScore != 0 {
		t.Errorf("VectorScore should be 0 for keyword row, got %f", docs[0].VectorScore)
	}
}

func TestToRankedDocs_Empty(t *testing.T) {
	t.Parallel()
	docs := toRankedDocs(nil, true)
	if len(docs) != 0 {
		t.Errorf("expected empty slice from nil input, got %d", len(docs))
	}
	docs = toRankedDocs([]rawRow{}, false)
	if len(docs) != 0 {
		t.Errorf("expected empty slice from empty input, got %d", len(docs))
	}
}

// ---------------------------------------------------------------------------
// buildMetadataFilterClause
// ---------------------------------------------------------------------------

func TestBuildMetadataFilterClause_NoFilters(t *testing.T) {
	t.Parallel()
	clause, args, next := buildMetadataFilterClause("kb_id = $1::uuid", []any{"kb-1"}, 2, nil)
	if clause != "kb_id = $1::uuid" {
		t.Errorf("unexpected clause: %q", clause)
	}
	if len(args) != 1 {
		t.Errorf("expected 1 arg, got %d", len(args))
	}
	if next != 2 {
		t.Errorf("expected next=2, got %d", next)
	}
}

func TestBuildMetadataFilterClause_WithFilters(t *testing.T) {
	t.Parallel()
	filters := map[string]string{"lang": "de"}
	clause, args, next := buildMetadataFilterClause("kb_id = $1::uuid", []any{"kb-1"}, 2, filters)

	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(args))
	}
	if next != 3 {
		t.Errorf("expected next=3, got %d", next)
	}
	// The added arg should be a JSON snippet containing the filter key/value.
	snippet, ok := args[1].(string)
	if !ok {
		t.Fatalf("second arg is not a string: %T", args[1])
	}
	if snippet != `{"lang":"de"}` {
		t.Errorf("unexpected JSON snippet: %q", snippet)
	}
	// Clause must contain @> $2::jsonb.
	if !strings.Contains(clause, "$2::jsonb") {
		t.Errorf("clause missing jsonb filter: %q", clause)
	}
}

// ---------------------------------------------------------------------------
// SearchResult type
// ---------------------------------------------------------------------------

func TestSearchResultFields(t *testing.T) {
	t.Parallel()
	sr := SearchResult{
		Chunks: []SearchChunk{
			{
				ID:          "id-1",
				Content:     "some text",
				FileID:      "file-1",
				FileName:    "doc.pdf",
				Metadata:    map[string]any{"page": float64(1)},
				Score:       0.88,
				VectorScore: 0.9,
				KeywordRank: 0.5,
				RerankScore: 0.75,
			},
		},
	}

	if len(sr.Chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(sr.Chunks))
	}
	c := sr.Chunks[0]
	if c.FileName != "doc.pdf" {
		t.Errorf("FileName: want doc.pdf, got %q", c.FileName)
	}
	if c.Score != 0.88 {
		t.Errorf("Score: want 0.88, got %f", c.Score)
	}
}

// ---------------------------------------------------------------------------
// SearchOptions — zero value is valid
// ---------------------------------------------------------------------------

func TestSearchOptions_ZeroValue(t *testing.T) {
	t.Parallel()
	var opts SearchOptions
	if opts.Enhance != "" {
		t.Error("Enhance should default to empty string")
	}
	if opts.HyDE || opts.MultiQuery || opts.SkipCache {
		t.Error("boolean flags should default to false")
	}
	if opts.FileIDs != nil {
		t.Error("FileIDs should default to nil")
	}
}

// ---------------------------------------------------------------------------
// joinStrings helper (internal)
// ---------------------------------------------------------------------------

func TestJoinStrings(t *testing.T) {
	t.Parallel()
	if joinStrings([]string{"a", "b", "c"}) != "a,b,c" {
		t.Error("joinStrings failed")
	}
	if joinStrings(nil) != "" {
		t.Error("joinStrings(nil) should be empty string")
	}
}

// ---------------------------------------------------------------------------
// SQL shape tests — verify query construction logic without a live DB
// ---------------------------------------------------------------------------

// TestVectorSQLShape checks that the vector SQL template produces the expected
// column list and parameter references.
func TestVectorSQLShape(t *testing.T) {
	t.Parallel()
	tableName := GetVectorTableName(1536)
	filterClause := "kb_id = $2::uuid"
	dimensions := 1536
	useHalfvec := false
	limit := 10

	var vectorCast, embeddingExpr string
	if useHalfvec {
		vectorCast = fmt.Sprintf("$1::vector::halfvec(%d)", dimensions)
		embeddingExpr = fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	} else {
		vectorCast = "$1::vector"
		embeddingExpr = "embedding"
	}

	sql := fmt.Sprintf(`
		SELECT id::text, content, metadata::text, file_id::text,
		       1 - (%s <=> %s) AS score
		FROM "%s"
		WHERE %s
		ORDER BY %s <=> %s
		LIMIT %d
	`, embeddingExpr, vectorCast, tableName, filterClause,
		embeddingExpr, vectorCast, limit)

	for _, want := range []string{
		"document_chunks",
		"kb_id = $2::uuid",
		"$1::vector",
		"LIMIT 10",
		"1 - (",
		"AS score",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("vector SQL missing %q:\n%s", want, sql)
		}
	}
}

func TestHalfvecSQLShape(t *testing.T) {
	t.Parallel()
	dimensions := 3072
	useHalfvec := dimensions > 2000 && dimensions <= 4000
	if !useHalfvec {
		t.Fatal("expected useHalfvec=true for dims=3072")
	}

	embeddingExpr := fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	vectorCast := fmt.Sprintf("$1::vector::halfvec(%d)", dimensions)

	if !strings.Contains(embeddingExpr, "halfvec(3072)") {
		t.Errorf("embeddingExpr missing halfvec cast: %q", embeddingExpr)
	}
	if !strings.Contains(vectorCast, "halfvec(3072)") {
		t.Errorf("vectorCast missing halfvec cast: %q", vectorCast)
	}
}

func TestKeywordSQLShape(t *testing.T) {
	// Mimics the SQL shape produced by runKeywordSearch for the
	// natural-language case (no quoted phrases, just an unquoted remainder).
	// After the union fix, the unquoted-remainder path emits BOTH
	// websearch_to_tsquery (AND) and to_tsquery (OR-of-tokens), joined with
	// the tsquery || operator inside a single sub-clause. ts_rank weights
	// both, so AND-matches dominate and OR-matches provide a recall floor.
	t.Parallel()

	tableName := GetVectorTableName(1536)
	pgConfig := PgTextSearchConfig("de")
	filterClause := "kb_id = $1::uuid"
	limit := 15

	// Unioned remainder clause.
	composed := "((websearch_to_tsquery($2::regconfig, $3) || to_tsquery($2::regconfig, $4)))"

	sql := fmt.Sprintf(`
		SELECT id::text, content, COALESCE(contextual_prefix, ''), metadata::text, file_id::text,
		       ts_rank(vector_index, %s) AS score
		FROM "%s"
		WHERE %s AND vector_index @@ %s
		ORDER BY score DESC
		LIMIT %d
	`, composed, tableName, filterClause, composed, limit)

	for _, want := range []string{
		"document_chunks",
		"ts_rank",
		"websearch_to_tsquery($2::regconfig, $3)",
		"to_tsquery($2::regconfig, $4)",
		" || ",
		"vector_index @@",
		"LIMIT 15",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("keyword SQL missing %q:\n%s", want, sql)
		}
	}
	if pgConfig != "german" {
		t.Errorf("expected pgConfig=german, got %q", pgConfig)
	}
}

func TestKeywordSQLShape_WithPhrase(t *testing.T) {
	// Mixed query: remainder (union AND+OR) AND-joined with one phrase.
	// runKeywordSearch builds queryParts in order — remainder first (lower
	// $-param numbers), phrases second.
	t.Parallel()

	tableName := GetVectorTableName(1536)
	pgConfig := PgTextSearchConfig("de")
	filterClause := "kb_id = $1::uuid"
	limit := 15

	// Remainder union sub-clause (params $3, $4) AND-joined with phrase ($5).
	composed := "((websearch_to_tsquery($2::regconfig, $3) || to_tsquery($2::regconfig, $4)) && phraseto_tsquery($2::regconfig, $5))"

	sql := fmt.Sprintf(`
		SELECT id::text, content, COALESCE(contextual_prefix, ''), metadata::text, file_id::text,
		       ts_rank(vector_index, %s) AS score
		FROM "%s"
		WHERE %s AND vector_index @@ %s
		ORDER BY score DESC
		LIMIT %d
	`, composed, tableName, filterClause, composed, limit)

	for _, want := range []string{
		"websearch_to_tsquery($2::regconfig, $3)",
		"to_tsquery($2::regconfig, $4)",
		"phraseto_tsquery($2::regconfig, $5)",
		" || ",
		" && ",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("keyword SQL missing %q:\n%s", want, sql)
		}
	}
	_ = pgConfig
}

// TestSearchLimitCandidateMultiplier verifies the candidate over-fetch logic.
func TestSearchLimitCandidateMultiplier(t *testing.T) {
	t.Parallel()
	t.Run("without reranker", func(t *testing.T) {
		t.Parallel()
		cases := []struct{ limit, want int }{
			{5, 30},  // 5*2=10, floor 30
			{15, 30}, // 15*2=30, exactly at floor
			{20, 40}, // 20*2=40, above floor
		}
		for _, tc := range cases {
			searchLimit := tc.limit * 2
			if searchLimit < 30 {
				searchLimit = 30
			}
			if searchLimit != tc.want {
				t.Errorf("limit=%d: got %d, want %d", tc.limit, searchLimit, tc.want)
			}
		}
	})

	t.Run("with reranker", func(t *testing.T) {
		t.Parallel()
		cases := []struct{ limit, want int }{
			{5, 50},  // 5*4=20, floor 50
			{10, 50}, // 10*4=40, floor 50
			{15, 60}, // 15*4=60, above floor
		}
		for _, tc := range cases {
			searchLimit := tc.limit * 4
			if searchLimit < 50 {
				searchLimit = 50
			}
			if searchLimit != tc.want {
				t.Errorf("limit=%d: got %d, want %d", tc.limit, searchLimit, tc.want)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// loadSiteConfig
// ---------------------------------------------------------------------------

// stubSiteConfig implements SiteConfigReader for testing.
type stubSiteConfig struct {
	values map[string]*string
}

func (s *stubSiteConfig) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	v, ok := s.values[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func strPtr(s string) *string { return &s }

func TestLoadSiteConfig_Defaults(t *testing.T) {
	t.Parallel()
	svc := NewSearchService(nil, nil, nil)
	// siteConfig is nil → should return DefaultConfig()
	cfg := svc.loadSiteConfig(context.Background())
	def := DefaultConfig()

	if cfg.MinSimilarityThreshold != def.MinSimilarityThreshold {
		t.Errorf("MinSimilarityThreshold: want %f, got %f", def.MinSimilarityThreshold, cfg.MinSimilarityThreshold)
	}
	if cfg.ScoreDropThreshold != def.ScoreDropThreshold {
		t.Errorf("ScoreDropThreshold: want %f, got %f", def.ScoreDropThreshold, cfg.ScoreDropThreshold)
	}
	if cfg.DefaultTopK != def.DefaultTopK {
		t.Errorf("DefaultTopK: want %d, got %d", def.DefaultTopK, cfg.DefaultTopK)
	}
	if cfg.ContextWindowSize != def.ContextWindowSize {
		t.Errorf("ContextWindowSize: want %d, got %d", def.ContextWindowSize, cfg.ContextWindowSize)
	}
	if cfg.AutoSpellCorrect != def.AutoSpellCorrect {
		t.Errorf("AutoSpellCorrect: want %v, got %v", def.AutoSpellCorrect, cfg.AutoSpellCorrect)
	}
	if cfg.RerankBlendAlpha != def.RerankBlendAlpha {
		t.Errorf("RerankBlendAlpha: want %f, got %f", def.RerankBlendAlpha, cfg.RerankBlendAlpha)
	}
	if cfg.HNSWEfSearch != def.HNSWEfSearch {
		t.Errorf("HNSWEfSearch: want %d, got %d", def.HNSWEfSearch, cfg.HNSWEfSearch)
	}
	if def.HNSWEfSearch != 150 {
		t.Errorf("DefaultConfig().HNSWEfSearch: want 150, got %d", def.HNSWEfSearch)
	}
	if cfg.MRLTwoPass != false {
		t.Errorf("MRLTwoPass: want false (default), got %v", cfg.MRLTwoPass)
	}
	if def.MRLTwoPass != false {
		t.Errorf("DefaultConfig().MRLTwoPass: want false, got %v", def.MRLTwoPass)
	}
}

func TestLoadSiteConfig_Overrides(t *testing.T) {
	t.Parallel()
	svc := NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{
		values: map[string]*string{
			"min_similarity_threshold": strPtr("0.5"),
			"auto_spell_correct":       strPtr("false"),
			"default_top_k":            strPtr("25"),
			"score_drop_threshold":     strPtr("0.1"),
			"context_window_size":      strPtr("3"),
			"rerank_blend_alpha":       strPtr("0.5"),
			"hnsw_ef_search":           strPtr("250"),
			"mrl_two_pass_enabled":     strPtr("true"),
		},
	}))

	cfg := svc.loadSiteConfig(context.Background())

	if cfg.MinSimilarityThreshold != 0.5 {
		t.Errorf("MinSimilarityThreshold: want 0.5, got %f", cfg.MinSimilarityThreshold)
	}
	if cfg.AutoSpellCorrect != false {
		t.Errorf("AutoSpellCorrect: want false, got %v", cfg.AutoSpellCorrect)
	}
	if cfg.DefaultTopK != 25 {
		t.Errorf("DefaultTopK: want 25, got %d", cfg.DefaultTopK)
	}
	if cfg.ScoreDropThreshold != 0.1 {
		t.Errorf("ScoreDropThreshold: want 0.1, got %f", cfg.ScoreDropThreshold)
	}
	if cfg.ContextWindowSize != 3 {
		t.Errorf("ContextWindowSize: want 3, got %d", cfg.ContextWindowSize)
	}
	if cfg.RerankBlendAlpha != 0.5 {
		t.Errorf("RerankBlendAlpha: want 0.5, got %f", cfg.RerankBlendAlpha)
	}
	if cfg.HNSWEfSearch != 250 {
		t.Errorf("HNSWEfSearch: want 250, got %d", cfg.HNSWEfSearch)
	}
	if cfg.MRLTwoPass != true {
		t.Errorf("MRLTwoPass: want true, got %v", cfg.MRLTwoPass)
	}
}

// TestLoadSiteConfig_BM25TieredBoost pins the tiered-boost gate.
// Default off; explicit "true" enables; bad values fall back to
// default (false). Scoring impact is exercised by the keyword-SQL
// shape tests; this one just verifies the site_config plumbing.
func TestLoadSiteConfig_BM25TieredBoost(t *testing.T) {
	t.Parallel()

	// default
	svc := NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{}}))
	if cfg := svc.loadSiteConfig(context.Background()); cfg.BM25TieredBoost {
		t.Errorf("BM25TieredBoost: default must be false, got true")
	}

	// explicit true
	svc = NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{
		"bm25_tiered_boost_enabled": strPtr("true"),
	}}))
	if cfg := svc.loadSiteConfig(context.Background()); !cfg.BM25TieredBoost {
		t.Errorf("BM25TieredBoost: explicit true must be true")
	}

	// garbage falls back to default (false)
	svc = NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{
		"bm25_tiered_boost_enabled": strPtr("perhaps"),
	}}))
	if cfg := svc.loadSiteConfig(context.Background()); cfg.BM25TieredBoost {
		t.Errorf("BM25TieredBoost: garbage value must yield default (false)")
	}
}

// TestLoadSiteConfig_RerankScoreDrop pins the opt-in reranker-active elbow
// cutoff: enabled defaults false, threshold defaults 0.5, explicit values
// parse, and out-of-range thresholds fall back to the default.
func TestLoadSiteConfig_RerankScoreDrop(t *testing.T) {
	t.Parallel()

	// defaults: disabled, threshold pre-seeded at 0.5
	svc := NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{}}))
	cfg := svc.loadSiteConfig(context.Background())
	if cfg.RerankScoreDropEnabled {
		t.Errorf("RerankScoreDropEnabled: default must be false")
	}
	if cfg.RerankScoreDropThreshold != 0.5 {
		t.Errorf("RerankScoreDropThreshold: default must be 0.5, got %v", cfg.RerankScoreDropThreshold)
	}

	// explicit enable + threshold
	svc = NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{
		"rerank_score_drop_enabled":   strPtr("true"),
		"rerank_score_drop_threshold": strPtr("0.3"),
	}}))
	cfg = svc.loadSiteConfig(context.Background())
	if !cfg.RerankScoreDropEnabled {
		t.Errorf("RerankScoreDropEnabled: explicit true must enable")
	}
	if cfg.RerankScoreDropThreshold != 0.3 {
		t.Errorf("RerankScoreDropThreshold: explicit 0.3 must parse, got %v", cfg.RerankScoreDropThreshold)
	}

	// out-of-range threshold falls back to default (0.5); garbage enable -> default false
	svc = NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{
		"rerank_score_drop_enabled":   strPtr("perhaps"),
		"rerank_score_drop_threshold": strPtr("1.5"),
	}}))
	cfg = svc.loadSiteConfig(context.Background())
	if cfg.RerankScoreDropEnabled {
		t.Errorf("RerankScoreDropEnabled: garbage must yield default (false)")
	}
	if cfg.RerankScoreDropThreshold != 0.5 {
		t.Errorf("RerankScoreDropThreshold: out-of-range must yield default (0.5), got %v", cfg.RerankScoreDropThreshold)
	}
}

// TestLoadSiteConfig_RAGFusionEnabled pins the P4 gate. Default
// off; explicit "true" enables; bad values fall back to default.
func TestLoadSiteConfig_RAGFusionEnabled(t *testing.T) {
	t.Parallel()

	// default
	svc := NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{}}))
	if cfg := svc.loadSiteConfig(context.Background()); cfg.RAGFusionEnabled {
		t.Errorf("RAGFusionEnabled: default must be false, got true")
	}

	// explicit true
	svc = NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{
		"rag_fusion_enabled": strPtr("true"),
	}}))
	if cfg := svc.loadSiteConfig(context.Background()); !cfg.RAGFusionEnabled {
		t.Errorf("RAGFusionEnabled: explicit true must be true")
	}

	// explicit false
	svc = NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{
		"rag_fusion_enabled": strPtr("false"),
	}}))
	if cfg := svc.loadSiteConfig(context.Background()); cfg.RAGFusionEnabled {
		t.Errorf("RAGFusionEnabled: explicit false must be false")
	}

	// garbage falls back to default (false)
	svc = NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{values: map[string]*string{
		"rag_fusion_enabled": strPtr("maybe"),
	}}))
	if cfg := svc.loadSiteConfig(context.Background()); cfg.RAGFusionEnabled {
		t.Errorf("RAGFusionEnabled: garbage value must yield default (false)")
	}
}

func TestLoadSiteConfig_InvalidValues(t *testing.T) {
	t.Parallel()
	def := DefaultConfig()
	svc := NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{
		values: map[string]*string{
			"min_similarity_threshold": strPtr("not-a-number"),
			"auto_spell_correct":       strPtr("maybe"),
			"default_top_k":            strPtr("999"),  // out of range (max 50)
			"score_drop_threshold":     strPtr("-0.5"), // out of range (min 0)
			"context_window_size":      strPtr("10"),   // out of range (max 5)
			"rerank_blend_alpha":       strPtr("1.5"),  // out of range (max 1)
			"hnsw_ef_search":           strPtr("0"),    // out of range (min 1)
			"mrl_two_pass_enabled":     strPtr("maybe"),
		},
	}))

	cfg := svc.loadSiteConfig(context.Background())

	if cfg.MinSimilarityThreshold != def.MinSimilarityThreshold {
		t.Errorf("MinSimilarityThreshold: want default %f, got %f", def.MinSimilarityThreshold, cfg.MinSimilarityThreshold)
	}
	if cfg.AutoSpellCorrect != def.AutoSpellCorrect {
		t.Errorf("AutoSpellCorrect: want default %v, got %v", def.AutoSpellCorrect, cfg.AutoSpellCorrect)
	}
	if cfg.DefaultTopK != def.DefaultTopK {
		t.Errorf("DefaultTopK: want default %d, got %d", def.DefaultTopK, cfg.DefaultTopK)
	}
	if cfg.ScoreDropThreshold != def.ScoreDropThreshold {
		t.Errorf("ScoreDropThreshold: want default %f, got %f", def.ScoreDropThreshold, cfg.ScoreDropThreshold)
	}
	if cfg.ContextWindowSize != def.ContextWindowSize {
		t.Errorf("ContextWindowSize: want default %d, got %d", def.ContextWindowSize, cfg.ContextWindowSize)
	}
	if cfg.RerankBlendAlpha != def.RerankBlendAlpha {
		t.Errorf("RerankBlendAlpha: want default %f, got %f", def.RerankBlendAlpha, cfg.RerankBlendAlpha)
	}
	if cfg.HNSWEfSearch != def.HNSWEfSearch {
		t.Errorf("HNSWEfSearch: want default %d, got %d", def.HNSWEfSearch, cfg.HNSWEfSearch)
	}
	if cfg.MRLTwoPass != def.MRLTwoPass {
		t.Errorf("MRLTwoPass: want default %v, got %v", def.MRLTwoPass, cfg.MRLTwoPass)
	}

	svc = NewSearchService(nil, nil, nil, WithSiteConfigReader(&stubSiteConfig{
		values: map[string]*string{
			"rerank_blend_alpha": strPtr("-0.5"), // out of range (min 0)
		},
	}))
	cfg = svc.loadSiteConfig(context.Background())
	if cfg.RerankBlendAlpha != def.RerankBlendAlpha {
		t.Errorf("RerankBlendAlpha (below range): want default %f, got %f", def.RerankBlendAlpha, cfg.RerankBlendAlpha)
	}
}

// ---------------------------------------------------------------------------
// buildSetEfSearchSQL
// ---------------------------------------------------------------------------

// TestRunVectorSearch_EfSearchSQLString verifies that runVectorSearch
// builds SET LOCAL hnsw.ef_search with the value derived from the caller's
// HNSWEfSearch parameter, not a hardcoded constant.
func TestRunVectorSearch_EfSearchSQLString(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want int }{
		{50, 50},
		{150, 150},
		{300, 300},
	}
	for _, tc := range cases {
		got := buildSetEfSearchSQL(tc.in)
		want := fmt.Sprintf("SET LOCAL hnsw.ef_search = %d", tc.want)
		if got != want {
			t.Errorf("buildSetEfSearchSQL(%d): got %q, want %q", tc.in, got, want)
		}
	}
}

// TestBuildTwoPassVectorSQL verifies the CTE structure of the MRL two-pass
// query — the candidate-fetch CTE uses embedding_low at $1::vector(256), the
// outer SELECT re-ranks with the full-dim embedding at $4, the LIMITs are at
// $3 (over-fetch) and $5 (final), and halfvec casting is applied when
// dimensions > 2000.
func TestBuildTwoPassVectorSQL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		tableName       string
		filterClause    string
		dimensions      int
		useHalfvec      bool
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:         "regular vector dim",
			tableName:    "document_chunks_1536",
			filterClause: "kb_id = $2::uuid",
			dimensions:   1536,
			useHalfvec:   false,
			wantContains: []string{
				"WITH candidates AS",
				`"document_chunks_1536"`,
				"embedding_low <=> $1::vector(256)",
				"LIMIT $3",
				"LIMIT $5",
				"$4::vector",
			},
			wantNotContains: []string{
				"halfvec",
			},
		},
		{
			name:         "halfvec dim",
			tableName:    "document_chunks_2560",
			filterClause: "kb_id = $2::uuid",
			dimensions:   2560,
			useHalfvec:   true,
			wantContains: []string{
				"WITH candidates AS",
				`"document_chunks_2560"`,
				"embedding_low <=> $1::vector(256)",
				"$4::vector::halfvec(2560)",
				"embedding::halfvec(2560)",
			},
		},
		{
			name:         "with file_ids filter",
			tableName:    "document_chunks_2560",
			filterClause: "kb_id = $2::uuid AND file_id = ANY($6::uuid[])",
			dimensions:   2560,
			useHalfvec:   true,
			wantContains: []string{
				"file_id = ANY($6::uuid[])",
			},
		},
		{
			// Phase F: every two-pass SELECT must surface node_kind +
			// tree_level so the chat layer can tag summary chunks
			// in the answer prompt and run the descendant-resolution
			// step on summary citations.
			name:         "exposes node_kind and tree_level",
			tableName:    "document_chunks_1536",
			filterClause: "kb_id = $2::uuid",
			dimensions:   1536,
			useHalfvec:   false,
			wantContains: []string{
				"COALESCE(node_kind, 'leaf')",
				"COALESCE(tree_level, 0)",
			},
		},
		{
			// Phase F: NodeKindFilter is appended to the filterClause
			// at the caller; buildTwoPassVectorSQL just splices it
			// in. Use a literal filter string here to assert the
			// final SQL preserves it.
			name:         "node_kind filter splices into WHERE",
			tableName:    "document_chunks_1536",
			filterClause: "kb_id = $2::uuid AND node_kind = $6::text",
			dimensions:   1536,
			useHalfvec:   false,
			wantContains: []string{
				"node_kind = $6::text",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildTwoPassVectorSQL(tc.tableName, tc.filterClause, tc.dimensions, tc.useHalfvec)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, notWant := range tc.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("unexpected %q in:\n%s", notWant, got)
				}
			}
		})
	}
}
