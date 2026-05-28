package ai

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers shared across rerank tests
// ---------------------------------------------------------------------------

// newRerankResolver builds a ConfigResolver backed by a mock store that
// returns a single provider with the given rerankModel name and the supplied
// baseURL (normally the test server URL).
func newRerankResolver(baseURL, rerankModel string) *ConfigResolver {
	store := &mockStore{
		activeProvider: &AIProviderInfo{
			ID:      "prov1",
			Name:    "TestProvider",
			APIKey:  "test-key",
			BaseURL: baseURL,
		},
		modelsByProv: map[string][]AIModelInfo{
			"prov1": {
				{Name: "gpt-4o"},
				{Name: "text-embedding-3-small", IsEmbedding: true},
				{Name: rerankModel, IsRerank: rerankModel != ""},
			},
		},
	}
	// When rerankModel is empty, remove the degenerate IsRerank entry so that
	// the resolver's rerankModels list remains empty.
	if rerankModel == "" {
		store.modelsByProv["prov1"] = []AIModelInfo{
			{Name: "gpt-4o"},
			{Name: "text-embedding-3-small", IsEmbedding: true},
		}
	}
	return NewConfigResolver(store)
}

// rerankHandler returns an http.HandlerFunc that serves a JSON rerank response
// built from the provided results slice.
func rerankHandler(results []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	}
}

// ---------------------------------------------------------------------------
// Test: no rerank model → identity scores
// ---------------------------------------------------------------------------

func TestRerank_NoModel_ReturnsIdentityScores(t *testing.T) {
	// Server is never called, but we still supply a URL for the resolver.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when no rerank model is configured")
	}))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "")
	docs := []string{"alpha", "beta", "gamma"}

	scores, err := Rerank(context.Background(), resolver, "query", docs, "", 0, RerankOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != len(docs) {
		t.Fatalf("expected %d scores, got %d", len(docs), len(scores))
	}
	for i, s := range scores {
		if s.Index != i {
			t.Errorf("scores[%d].Index = %d, want %d", i, s.Index, i)
		}
		if s.Score != 1.0 {
			t.Errorf("scores[%d].Score = %f, want 1.0", i, s.Score)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: valid response → normalized scores returned
// ---------------------------------------------------------------------------

func TestRerank_ValidResponse_ReturnsNormalizedScores(t *testing.T) {
	// Simulate a provider that uses "relevance_score" for the first result and
	// "score" for the second.
	results := []map[string]any{
		{"index": 2, "relevance_score": 0.9, "score": 0.0},
		{"index": 0, "relevance_score": 0.0, "score": 0.5},
		{"index": 1, "relevance_score": 0.3, "score": 0.0},
	}
	srv := httptest.NewServer(rerankHandler(results))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "rerank-v1")
	docs := []string{"doc0", "doc1", "doc2"}

	scores, err := Rerank(context.Background(), resolver, "test query", docs, "", 0, RerankOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}

	// First result: relevance_score 0.9 should win over score 0.0.
	if scores[0].Index != 2 {
		t.Errorf("scores[0].Index = %d, want 2", scores[0].Index)
	}
	if scores[0].Score != 0.9 {
		t.Errorf("scores[0].Score = %f, want 0.9", scores[0].Score)
	}

	// Second result: relevance_score is 0, so score 0.5 is used.
	if scores[1].Index != 0 {
		t.Errorf("scores[1].Index = %d, want 0", scores[1].Index)
	}
	if scores[1].Score != 0.5 {
		t.Errorf("scores[1].Score = %f, want 0.5", scores[1].Score)
	}

	// Third result: relevance_score 0.3.
	if scores[2].Score != 0.3 {
		t.Errorf("scores[2].Score = %f, want 0.3", scores[2].Score)
	}
}

// ---------------------------------------------------------------------------
// Test: degenerate scores (all identical) → RRF fallback
// ---------------------------------------------------------------------------

func TestRerank_DegenerateScores_ReturnsRRFFallback(t *testing.T) {
	results := []map[string]any{
		{"index": 0, "relevance_score": 0.5},
		{"index": 1, "relevance_score": 0.5},
		{"index": 2, "relevance_score": 0.5},
	}
	srv := httptest.NewServer(rerankHandler(results))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "rerank-v1")
	docs := []string{"a", "b", "c"}

	scores, err := Rerank(context.Background(), resolver, "q", docs, "", 0, RerankOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRRFFallback(t, scores, len(docs))
}

// ---------------------------------------------------------------------------
// Test: poor discrimination (range < 0.1) → RRF fallback
// ---------------------------------------------------------------------------

func TestRerank_PoorDiscrimination_ReturnsRRFFallback(t *testing.T) {
	results := []map[string]any{
		{"index": 0, "relevance_score": 0.80},
		{"index": 1, "relevance_score": 0.85},
		{"index": 2, "relevance_score": 0.83},
	}
	srv := httptest.NewServer(rerankHandler(results))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "rerank-v1")
	docs := []string{"x", "y", "z"}

	scores, err := Rerank(context.Background(), resolver, "q", docs, "", 0, RerankOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRRFFallback(t, scores, len(docs))
}

// ---------------------------------------------------------------------------
// Test: API error → RRF fallback, no error returned
// ---------------------------------------------------------------------------

func TestRerank_APIError_ReturnsRRFFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "rerank-v1")
	docs := []string{"p", "q", "r"}

	scores, err := Rerank(context.Background(), resolver, "search", docs, "", 0, RerankOptions{})
	if err != nil {
		t.Fatalf("expected nil error (fail-open), got: %v", err)
	}
	assertRRFFallback(t, scores, len(docs))
}

// ---------------------------------------------------------------------------
// Test: out-of-bounds indices → RRF fallback
// ---------------------------------------------------------------------------

func TestRerank_OutOfBoundsIndices_ReturnsRRFFallback(t *testing.T) {
	// Index 5 is out of bounds for a 3-document list.
	results := []map[string]any{
		{"index": 5, "relevance_score": 0.9},
		{"index": 1, "relevance_score": 0.4},
	}
	srv := httptest.NewServer(rerankHandler(results))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "rerank-v1")
	docs := []string{"doc0", "doc1", "doc2"}

	scores, err := Rerank(context.Background(), resolver, "q", docs, "", 0, RerankOptions{})
	if err != nil {
		t.Fatalf("expected nil error (fail-open), got: %v", err)
	}
	assertRRFFallback(t, scores, len(docs))
}

// ---------------------------------------------------------------------------
// validateRerankResponse unit tests
// ---------------------------------------------------------------------------

func TestValidateRerankResponse_Empty(t *testing.T) {
	reason := validateRerankResponse(nil, 3)
	if reason != "empty_or_invalid_results" {
		t.Errorf("expected empty_or_invalid_results, got %q", reason)
	}
}

func TestValidateRerankResponse_NaN(t *testing.T) {
	scores := []RerankScore{{Index: 0, Score: math.NaN()}}
	reason := validateRerankResponse(scores, 3)
	if reason != "invalid_scores" {
		t.Errorf("expected invalid_scores, got %q", reason)
	}
}

func TestValidateRerankResponse_Valid(t *testing.T) {
	scores := []RerankScore{
		{Index: 2, Score: 0.9},
		{Index: 0, Score: 0.4},
		{Index: 1, Score: 0.1},
	}
	reason := validateRerankResponse(scores, 3)
	if reason != "" {
		t.Errorf("expected valid response, got reason %q", reason)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// assertRRFFallback checks that scores match the expected RRF pattern: count
// entries with index i and score 1/(i+1).
func assertRRFFallback(t *testing.T, scores []RerankScore, count int) {
	t.Helper()
	if len(scores) != count {
		t.Fatalf("RRF fallback: expected %d scores, got %d", count, len(scores))
	}
	for i, s := range scores {
		if s.Index != i {
			t.Errorf("RRF fallback scores[%d].Index = %d, want %d", i, s.Index, i)
		}
		want := 1.0 / float64(i+1)
		if s.Score != want {
			t.Errorf("RRF fallback scores[%d].Score = %f, want %f", i, s.Score, want)
		}
	}
}
