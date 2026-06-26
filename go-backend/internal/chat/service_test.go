package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/splitter"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Fakes for SiteConfigReader tests
// ---------------------------------------------------------------------------

// Compile-time check: any drift in SiteConfigReader breaks the build here
// rather than at the call site.
var _ SiteConfigReader = (*fakeSiteConfigReader)(nil)

type fakeSiteConfigReader struct{ values map[string]*string }

func (f *fakeSiteConfigReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	return f.values[key], nil
}

func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeChunks creates a slice of SearchChunk with sequential content for testing.
func makeChunks(n int) []vector.SearchChunk {
	chunks := make([]vector.SearchChunk, n)
	for i := range chunks {
		chunks[i] = vector.SearchChunk{
			ID:      string(rune('a' + i)),
			Content: "chunk content " + string(rune('a'+i)),
		}
	}
	return chunks
}

// ---------------------------------------------------------------------------
// SandwichOrder tests
// ---------------------------------------------------------------------------

func TestSandwichOrder_SixItems(t *testing.T) {
	// Input indices: 0(1st) 1(2nd) 2(3rd) 3(4th) 4(5th) 5(6th)
	// Even → front: 0,2,4  (1st,3rd,5th)
	// Odd  → back reversed: [1,3,5] reversed = [5,3,1] → (6th,4th,2nd)
	// Expected: [1st,3rd,5th,6th,4th,2nd]
	t.Parallel()

	chunks := makeChunks(6)
	original := make([]string, 6)
	for i, c := range chunks {
		original[i] = c.ID
	}

	result := SandwichOrder(chunks)

	if len(result) != 6 {
		t.Fatalf("expected 6 chunks, got %d", len(result))
	}

	wantOrder := []string{
		original[0], // 1st (even)
		original[2], // 3rd (even)
		original[4], // 5th (even)
		original[5], // 6th (odd, reversed → first of back)
		original[3], // 4th (odd, reversed → second of back)
		original[1], // 2nd (odd, reversed → third of back)
	}

	for i, want := range wantOrder {
		if result[i].ID != want {
			t.Errorf("position %d: want ID %q, got %q", i, want, result[i].ID)
		}
	}
}

func TestSandwichOrder_TwoItems_ReturnedAsIs(t *testing.T) {
	t.Parallel()
	chunks := makeChunks(2)
	result := SandwichOrder(chunks)
	if len(result) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(result))
	}
	for i := range chunks {
		if result[i].ID != chunks[i].ID {
			t.Errorf("position %d: want %q, got %q", i, chunks[i].ID, result[i].ID)
		}
	}
}

func TestSandwichOrder_OneItem_ReturnedAsIs(t *testing.T) {
	t.Parallel()
	chunks := makeChunks(1)
	result := SandwichOrder(chunks)
	if len(result) != 1 || result[0].ID != chunks[0].ID {
		t.Errorf("single item not returned as-is")
	}
}

// ---------------------------------------------------------------------------
// TruncateChunksToFit tests
// ---------------------------------------------------------------------------

func TestTruncateChunksToFit_TruncatesAtBudget(t *testing.T) {
	// Use a body long enough to have a meaningful token count (real tiktoken).
	t.Parallel()

	body := strings.Repeat("token ", 50) // ~50 tokens
	makeFixedChunks := func(n int) []vector.SearchChunk {
		c := make([]vector.SearchChunk, n)
		for i := range c {
			c[i] = vector.SearchChunk{Content: body}
		}
		return c
	}

	perChunk := splitter.CountTokens(body)
	chunks := makeFixedChunks(10)
	budget := perChunk*5 + perChunk/2 // fits exactly 5 chunks

	result := TruncateChunksToFit(chunks, budget)

	if len(result) >= 10 {
		t.Errorf("expected truncation, got all %d chunks", len(result))
	}
	if len(result) == 0 {
		t.Error("expected at least 1 chunk to fit")
	}
}

func TestTruncateChunksToFit_AllFit(t *testing.T) {
	t.Parallel()
	chunks := []vector.SearchChunk{
		{Content: "short"},
		{Content: "also short"},
	}
	result := TruncateChunksToFit(chunks, 1000)
	if len(result) != 2 {
		t.Errorf("expected all 2 chunks to fit, got %d", len(result))
	}
}

func TestTruncateChunksToFit_AllFitReturnsAll(t *testing.T) {
	t.Parallel()
	chunks := []vector.SearchChunk{
		{Content: "short A", Score: 0.9},
		{Content: "short B", Score: 0.5},
	}
	got := TruncateChunksToFit(chunks, 10_000)
	if len(got) != 2 {
		t.Errorf("expected 2 chunks (all fit), got %d", len(got))
	}
}

func TestTruncateChunksToFit_EmptyInput(t *testing.T) {
	t.Parallel()
	got := TruncateChunksToFit(nil, 100)
	if len(got) != 0 {
		t.Errorf("expected empty for nil input, got %d", len(got))
	}
}

func TestTruncateChunksToFit_ZeroBudget(t *testing.T) {
	t.Parallel()
	chunks := []vector.SearchChunk{{Content: "anything", Score: 1.0}}
	got := TruncateChunksToFit(chunks, 0)
	if len(got) != 0 {
		t.Errorf("expected empty for zero budget, got %d", len(got))
	}
}

func TestTruncateChunksToFit_HighScoreLatePosition_Preferred(t *testing.T) {
	// Three roughly equal-token chunks, scores ascending. Budget fits exactly
	// 2 of them. Old first-fit would have kept positions 0+1 (scores 0.3+0.5);
	// new score-aware should keep positions 1+2 (scores 0.5+0.9), in original
	// input order.
	t.Parallel()

	body := strings.Repeat("token ", 100) // ~100 tokens regardless of tokenizer
	chunks := []vector.SearchChunk{
		{Content: body + " low", Score: 0.3},
		{Content: body + " mid", Score: 0.5},
		{Content: body + " high", Score: 0.9},
	}
	// Pick a budget that fits exactly two of them.
	perChunk := splitter.CountTokens(chunks[0].Content)
	budget := perChunk*2 + 5

	got := TruncateChunksToFit(chunks, budget)
	if len(got) != 2 {
		t.Fatalf("expected 2 survivors, got %d", len(got))
	}
	// Survivors in original order: chunks[1] then chunks[2].
	if got[0].Score != 0.5 || got[1].Score != 0.9 {
		t.Errorf("expected scores 0.5, 0.9 in original order; got %v, %v", got[0].Score, got[1].Score)
	}
}

func TestTruncateChunksToFit_TieScoresPreferLowerIndex(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("token ", 100)
	chunks := []vector.SearchChunk{
		{Content: body + " a", Score: 0.5},
		{Content: body + " b", Score: 0.5},
		{Content: body + " c", Score: 0.5},
	}
	perChunk := splitter.CountTokens(chunks[0].Content)
	budget := perChunk*2 + 5

	got := TruncateChunksToFit(chunks, budget)
	if len(got) != 2 {
		t.Fatalf("expected 2 survivors, got %d", len(got))
	}
	// Tie-break by lower input index → keep [0] and [1], skip [2].
	if got[0].Content != chunks[0].Content || got[1].Content != chunks[1].Content {
		t.Errorf("tie-break should prefer lower indices; got contents %q, %q", got[0].Content, got[1].Content)
	}
}

// ---------------------------------------------------------------------------
// IsLowConfidence tests
// ---------------------------------------------------------------------------

func TestIsLowConfidence_Empty(t *testing.T) {
	t.Parallel()
	if !IsLowConfidence(nil) {
		t.Error("expected low confidence for nil/empty chunks")
	}
	if !IsLowConfidence([]vector.SearchChunk{}) {
		t.Error("expected low confidence for empty slice")
	}
}

func TestIsLowConfidence_TwoChunks(t *testing.T) {
	t.Parallel()
	chunks := []vector.SearchChunk{
		{VectorScore: 0.9},
		{VectorScore: 0.8},
	}
	if !IsLowConfidence(chunks) {
		t.Error("expected low confidence for fewer than 3 chunks")
	}
}

func TestIsLowConfidence_AllLowScores(t *testing.T) {
	t.Parallel()
	chunks := []vector.SearchChunk{
		{VectorScore: 0.1},
		{VectorScore: 0.2},
		{VectorScore: 0.3},
	}
	if !IsLowConfidence(chunks) {
		t.Error("expected low confidence when all vector scores < 0.4")
	}
}

func TestIsLowConfidence_GoodResults(t *testing.T) {
	t.Parallel()
	chunks := []vector.SearchChunk{
		{VectorScore: 0.8},
		{VectorScore: 0.7},
		{VectorScore: 0.6},
	}
	if IsLowConfidence(chunks) {
		t.Error("expected high confidence for 3 chunks with good scores")
	}
}

func TestIsLowConfidence_MixedScores_OneGood(t *testing.T) {
	// At least one chunk meets the 0.4 threshold → not low confidence.
	t.Parallel()

	chunks := []vector.SearchChunk{
		{VectorScore: 0.1},
		{VectorScore: 0.1},
		{VectorScore: 0.5},
	}
	if IsLowConfidence(chunks) {
		t.Error("expected high confidence when at least one vector score >= 0.4")
	}
}

// ---------------------------------------------------------------------------
// maybeDecomposeQuery gating tests (T1-1)
// ---------------------------------------------------------------------------
//
// The LLM-success path is covered by ai.TestGenerateSubQueries_* in the
// ai package — these tests pin the chat-side gating contract: nil reader,
// disabled flag, wrong query type, and pre-populated SubQueries must all
// short-circuit BEFORE any LLM call (so a nil aiResolver is safe in the
// short-circuit branches).

func TestMaybeDecomposeQuery_NilSiteConfigShortCircuits(t *testing.T) {
	t.Parallel()
	opts := &vector.SearchOptions{}
	maybeDecomposeQuery(
		context.Background(), nil, nil,
		ChatContextParams{QueryType: vector.QueryTypeComplexReasoning, SearchQuery: "q"},
		opts,
	)
	if len(opts.SubQueries) != 0 {
		t.Errorf("nil siteConfig should not touch opts.SubQueries, got %v", opts.SubQueries)
	}
}

func TestMaybeDecomposeQuery_DisabledShortCircuits(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"query_decompose_enabled": strPtr("false"),
	}}
	opts := &vector.SearchOptions{}
	maybeDecomposeQuery(
		context.Background(), nil, reader,
		ChatContextParams{QueryType: vector.QueryTypeComplexReasoning, SearchQuery: "q"},
		opts,
	)
	if len(opts.SubQueries) != 0 {
		t.Errorf("disabled flag must skip; opts.SubQueries=%v", opts.SubQueries)
	}
}

func TestMaybeDecomposeQuery_NonComplexQueryTypeSkips(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"query_decompose_enabled": strPtr("true"),
	}}
	// lookup + enumeration must both bypass — only complex_reasoning fires.
	for _, qt := range []string{vector.QueryTypeLookup, vector.QueryTypeEnumeration, vector.QueryTypeUnknown, ""} {
		opts := &vector.SearchOptions{}
		maybeDecomposeQuery(
			context.Background(), nil, reader,
			ChatContextParams{QueryType: qt, SearchQuery: "q"},
			opts,
		)
		if len(opts.SubQueries) != 0 {
			t.Errorf("queryType=%q must skip decomposition; opts.SubQueries=%v", qt, opts.SubQueries)
		}
	}
}

func TestMaybeDecomposeQuery_PrePopulatedSubQueriesSkips(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"query_decompose_enabled": strPtr("true"),
	}}
	// Caller (a hypothetical orchestrator) already filled SubQueries.
	// maybeDecomposeQuery must leave the existing slice alone — no
	// LLM call, no append, no double-decomposition.
	existing := []string{"caller-provided-sub-1", "caller-provided-sub-2"}
	opts := &vector.SearchOptions{SubQueries: append([]string(nil), existing...)}
	maybeDecomposeQuery(
		context.Background(), nil, reader,
		ChatContextParams{QueryType: vector.QueryTypeComplexReasoning, SearchQuery: "q"},
		opts,
	)
	if len(opts.SubQueries) != len(existing) {
		t.Errorf("pre-populated SubQueries must not grow; want len=%d, got %d (%v)",
			len(existing), len(opts.SubQueries), opts.SubQueries)
	}
	for i, q := range existing {
		if opts.SubQueries[i] != q {
			t.Errorf("pre-populated SubQueries mutated at index %d: want %q, got %q", i, q, opts.SubQueries[i])
		}
	}
}

// ---------------------------------------------------------------------------
// CountTokensApprox tests
// ---------------------------------------------------------------------------

func TestCountTokensApprox(t *testing.T) {
	// Now delegates to splitter.CountTokens (tiktoken cl100k_base).
	// We only assert structural invariants: empty → 0, non-empty → > 0.
	t.Parallel()

	if got := CountTokensApprox(""); got != 0 {
		t.Errorf("CountTokensApprox(\"\") = %d, want 0", got)
	}
	texts := []string{"hello", "hello world", strings.Repeat("token ", 100)}
	for _, text := range texts {
		if got := CountTokensApprox(text); got <= 0 {
			t.Errorf("CountTokensApprox(%q) = %d, want > 0", text, got)
		}
	}
}

func TestRenderSourceHeader_CommunitySummary(t *testing.T) {
	got := renderSourceHeader(3, "kg", "", "community_summary", 0)
	if got != "[3] (knowledge-graph community) [Source: kg]" {
		t.Errorf("community header: got %q", got)
	}
	// RAPTOR summary still works (regression).
	if got := renderSourceHeader(1, "doc.pdf", "", "summary", 2); got != "[1] (summary, level 2) [Source: doc.pdf]" {
		t.Errorf("raptor header regressed: got %q", got)
	}
	// Plain leaf unchanged.
	if got := renderSourceHeader(2, "doc.pdf", " p.4", "leaf", 0); got != "[2] [Source: doc.pdf p.4]" {
		t.Errorf("leaf header: got %q", got)
	}
}
