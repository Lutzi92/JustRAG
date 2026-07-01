package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// capRerankDocuments unit tests
// ---------------------------------------------------------------------------

func TestCapRerankDocuments_UnderBudget_NoTruncationNoAlloc(t *testing.T) {
	docs := []string{"alpha", "beta", "gamma"}
	out, truncated := capRerankDocuments("query", docs)
	if truncated != 0 {
		t.Fatalf("expected 0 truncated, got %d", truncated)
	}
	// Common path must not copy: identical backing array.
	if len(out) != len(docs) || &out[0] != &docs[0] {
		t.Fatalf("expected original slice returned unchanged")
	}
}

func TestCapRerankDocuments_OversizedDoc_TruncatedToPerDocCap(t *testing.T) {
	huge := strings.Repeat("a", (rerankPerDocTokenCap+10_000)*4)
	docs := []string{"small", huge, "small2"}
	out, truncated := capRerankDocuments("q", docs)
	if truncated != 1 {
		t.Fatalf("expected 1 truncated, got %d", truncated)
	}
	// Untouched docs keep their identity/content.
	if out[0] != "small" || out[2] != "small2" {
		t.Fatalf("non-oversized docs were altered: %q %q", out[0], out[2])
	}
	// Truncated doc bounded by the per-doc cap (few docs → cap dominates the
	// equal-share allocation).
	maxChars := rerankPerDocTokenCap * 4
	if utf8.RuneCountInString(out[1]) > maxChars {
		t.Fatalf("truncated doc %d runes exceeds per-doc cap %d", utf8.RuneCountInString(out[1]), maxChars)
	}
	// Original slice must not be mutated.
	if len(docs[1]) == len(out[1]) {
		t.Fatalf("original document slice was mutated in place")
	}
}

func TestCapRerankDocuments_ManyDocs_TotalUnderBudget(t *testing.T) {
	// 300 fat docs, each ~2k tokens, would be ~600k tokens uncapped —
	// far over any reranker window. Equal-share must clamp the sum.
	docs := make([]string, 300)
	for i := range docs {
		docs[i] = strings.Repeat("x", 2000*4)
	}
	out, truncated := capRerankDocuments("q", docs)
	if len(out) != len(docs) {
		t.Fatalf("document count changed: %d != %d", len(out), len(docs))
	}
	if truncated == 0 {
		t.Fatalf("expected truncation for an oversized pool")
	}
	total := 0
	for _, d := range out {
		total += estimateRerankTokens(d)
	}
	if total > rerankInputTokenBudget {
		t.Fatalf("summed estimate %d exceeds budget %d", total, rerankInputTokenBudget)
	}
}

func TestCapRerankDocuments_PreservesRuneBoundaries(t *testing.T) {
	// Fill past the per-doc cap with a 3-byte rune so a byte-wise cut would
	// land mid-rune.
	huge := strings.Repeat("世", (rerankPerDocTokenCap+5_000)*4)
	out, truncated := capRerankDocuments("q", []string{huge})
	if truncated != 1 {
		t.Fatalf("expected 1 truncated, got %d", truncated)
	}
	if !utf8.ValidString(out[0]) {
		t.Fatalf("truncation split a multibyte rune (invalid UTF-8)")
	}
}

func TestCapRerankDocuments_Empty(t *testing.T) {
	out, truncated := capRerankDocuments("q", nil)
	if truncated != 0 || out != nil {
		t.Fatalf("empty input should pass through unchanged")
	}
}

// ---------------------------------------------------------------------------
// Integration: oversized docs are truncated before hitting the /rerank endpoint
// ---------------------------------------------------------------------------

func TestRerank_OversizedDocs_TruncatedBeforeSend(t *testing.T) {
	var gotDocs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.Unmarshal(body, &req)
		gotDocs = req.Documents
		// Return a well-discriminated response so no fallback kicks in.
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"index": 0, "relevance_score": 0.9},
			{"index": 1, "relevance_score": 0.5},
			{"index": 2, "relevance_score": 0.1},
		}})
	}))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "rerank-v1")
	docs := []string{
		"short",
		strings.Repeat("y", 2_000_000), // ~500k tokens on its own — over any window
		"short2",
	}

	scores, err := Rerank(context.Background(), resolver, "q", docs, "", 0, RerankOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}
	if len(gotDocs) != 3 {
		t.Fatalf("server saw %d docs, want 3 (count must be preserved)", len(gotDocs))
	}
	total := 0
	for _, d := range gotDocs {
		total += estimateRerankTokens(d)
	}
	if total > rerankInputTokenBudget {
		t.Fatalf("sent %d estimated tokens, over budget %d", total, rerankInputTokenBudget)
	}
}
