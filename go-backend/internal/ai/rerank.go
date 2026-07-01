package ai

import (
	"context"
	"log/slog"
	"math"
)

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// RerankScore holds a document index and its relevance score after reranking.
type RerankScore struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

const (
	// rerankInputTokenBudget bounds the *estimated* total token count of all
	// documents in a single rerank request. The served reranker rejects any
	// request over its context window (jina-rerank: 131072 tokens) with a 400,
	// which fails the whole rerank into RRF fallback and silently degrades
	// ranking (see the "api error 400 ... maximum context length" incident).
	// Kept well under the model window because estimateRerankTokens (chars/4)
	// under-counts token-dense content (code, German, CJK — CJK is ~3 bytes but
	// ~1 token per char, the worst case) and the served endpoint also counts
	// the query per pair; the ~50k-token headroom absorbs both.
	rerankInputTokenBudget = 80_000
	// rerankPerDocTokenCap bounds any single document. A cross-encoder's
	// relevance signal saturates well before this, so truncating an oversized
	// chunk (parent-child parents, RAPTOR / community summaries) costs no
	// meaningful ranking quality. It also bounds the per-pair request on the
	// Qwen3 chat-template path, which fans out one request per document.
	rerankPerDocTokenCap = 8_000
)

// estimateRerankTokens is a cheap chars/4 token approximation, matching the
// ingestion-side parser.EstimateTokens heuristic. Deliberately local to avoid
// importing the parser package into the low-level ai package.
func estimateRerankTokens(s string) int { return len(s) / 4 }

// capRerankDocuments truncates documents so a single rerank request stays
// within the reranker's context window. It returns the (possibly truncated)
// slice and the number of documents that were truncated.
//
// Two bounds apply together: an equal-share allocation (budget / N) that
// guarantees the summed estimate stays under rerankInputTokenBudget regardless
// of document count, and a per-document ceiling (rerankPerDocTokenCap) so a
// small pool never hands one document the entire window. The common case —
// a couple dozen normal-sized chunks — hits neither bound and returns the
// input slice untouched (no allocation).
//
// Truncation is purely for scoring: callers keep the full document text
// elsewhere, and the returned RerankScore indices line up 1:1 with the input
// (count and order are preserved).
func capRerankDocuments(query string, documents []string) ([]string, int) {
	if len(documents) == 0 {
		return documents, 0
	}

	budget := max(rerankInputTokenBudget-estimateRerankTokens(query), len(documents)) // floor: ~1 token/doc
	perDoc := min(budget/len(documents), rerankPerDocTokenCap)
	maxChars := perDoc * 4

	var out []string
	truncated := 0
	for i, d := range documents {
		if len(d) <= maxChars { // byte length ≥ rune count, so a safe fast pass
			continue
		}
		if out == nil { // copy-on-first-write to keep the caller's slice intact
			out = make([]string, len(documents))
			copy(out, documents)
		}
		out[i] = truncateToRunes(d, maxChars)
		truncated++
	}
	if out == nil {
		return documents, 0
	}
	return out, truncated
}

// truncateToRunes returns the longest prefix of s with at most maxChars runes,
// cutting on a rune boundary so a multibyte character is never split.
func truncateToRunes(s string, maxChars int) string {
	if len(s) <= maxChars { // byte length is an upper bound on rune count
		return s
	}
	n := 0
	for i := range s { // range over a string yields rune start byte offsets
		if n == maxChars {
			return s[:i]
		}
		n++
	}
	return s
}

// RerankOptions carries optional routing + instruction settings for Rerank.
//
// The zero value preserves the pre-existing behaviour: hit the provider's
// /rerank endpoint and use the returned scores directly. Set UseChatTemplate
// and pair it with a Qwen3-Reranker model to get the trained yes/no
// decision path. Instruction is the <Instruct>: tag fed into Qwen3's
// template; it's safe to leave empty (the scorer falls back to the
// web-search phrasing from the Qwen3 model card).
type RerankOptions struct {
	UseChatTemplate bool
	Instruction     string
}

// ---------------------------------------------------------------------------
// Public function
// ---------------------------------------------------------------------------

// Rerank reranks documents by relevance. Returns fallback scores if no rerank
// model is configured or on failure (fail-open: degraded search is better than
// no search).
//
//   - kbID: knowledge-base ID used for config resolution (empty = global).
//   - topN: how many results to request; 0 means all documents.
//   - opts: optional routing (e.g. Qwen3-Reranker chat-template path).
func Rerank(ctx context.Context, resolver *ConfigResolver, query string, documents []string, kbID string, topN int, opts RerankOptions) ([]RerankScore, error) {
	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		slog.ErrorContext(ctx, "ai: rerank: config resolution failed, using RRF fallback",
			"error", err, "kbID", kbID)
		return rrfFallbackScores(len(documents)), nil
	}

	// No rerank model configured — return identity scores so every document
	// keeps its original position with equal weight.
	if cfg.RerankModel == "" {
		return identityScores(len(documents)), nil
	}

	// Bound the request to the reranker's context window. Oversized chunks or
	// a large long-context pool can push the summed input past the model's
	// limit, which 400s the whole call into RRF fallback. Truncation preserves
	// document count and order, so the returned indices still map 1:1.
	if capped, n := capRerankDocuments(query, documents); n > 0 {
		slog.WarnContext(ctx, "ai: rerank: truncated oversized documents to fit reranker context window",
			"truncated_docs", n, "total_docs", len(documents), "model", cfg.RerankModel)
		documents = capped
	}

	// Route to the model-family-specific path. Today only Qwen3-Reranker with
	// explicit chat-template opt-in diverges from the standard /rerank endpoint;
	// selectRerankStrategy is the single place to add new families. On any
	// per-pair error we fall through to the legacy /rerank endpoint below —
	// same fail-open pattern as the rest of this function.
	if selectRerankStrategy(cfg.RerankModel, opts) == strategyQwen3ChatTemplate {
		scores, qErr := rerankWithQwen3ChatTemplate(ctx, cfg, query, opts.Instruction, documents)
		if qErr == nil {
			if reason := validateRerankResponse(scores, len(documents)); reason != "" {
				slog.WarnContext(ctx, "ai: rerank: qwen3 chat template response validation failed, falling back to /rerank",
					"reason", reason, "model", cfg.RerankModel)
			} else {
				return scores, nil
			}
		} else {
			slog.WarnContext(ctx, "ai: rerank: qwen3 chat template path failed, falling back to /rerank",
				"error", qErr, "model", cfg.RerankModel)
		}
	}

	n := topN
	if n == 0 {
		n = len(documents)
	}

	client := CachedClient(cfg.BaseURL, cfg.APIKey)
	resp, err := client.Rerank(ctx, &RerankRequest{
		Model:           cfg.RerankModel,
		Query:           query,
		Documents:       documents,
		TopN:            n,
		ReturnDocuments: false,
	})
	if err != nil {
		slog.ErrorContext(ctx, "ai: rerank: API call failed, using RRF fallback",
			"error", err, "model", cfg.RerankModel)
		return rrfFallbackScores(len(documents)), nil
	}

	scores := make([]RerankScore, len(resp.Results))
	for i, r := range resp.Results {
		scores[i] = RerankScore{Index: r.Index, Score: r.NormalizedScore()}
	}

	if reason := validateRerankResponse(scores, len(documents)); reason != "" {
		slog.WarnContext(ctx, "ai: rerank: response validation failed, using RRF fallback",
			"reason", reason, "model", cfg.RerankModel)
		return rrfFallbackScores(len(documents)), nil
	}

	return scores, nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// validateRerankResponse checks the normalized scores for common failure modes.
// Returns an empty string when the response is valid, or a reason string when
// it is not.
func validateRerankResponse(results []RerankScore, documentCount int) string {
	if len(results) == 0 {
		return "empty_or_invalid_results"
	}

	for _, r := range results {
		if math.IsNaN(r.Score) {
			return "invalid_scores"
		}
		if r.Index < 0 || r.Index >= documentCount {
			return "out_of_bounds_indices"
		}
	}

	if len(results) > 2 {
		first := results[0].Score
		allSame := true
		minScore, maxScore := first, first
		for _, r := range results[1:] {
			if r.Score != first {
				allSame = false
			}
			if r.Score < minScore {
				minScore = r.Score
			}
			if r.Score > maxScore {
				maxScore = r.Score
			}
		}
		if allSame {
			return "degenerate_all_identical_scores"
		}
		if maxScore-minScore < 0.1 {
			return "poor_discrimination"
		}
	}

	return ""
}

// ---------------------------------------------------------------------------
// Fallback helpers
// ---------------------------------------------------------------------------

// identityScores returns a score of 1.0 for every document index — used when
// no rerank model is configured and the original ranking should be preserved.
func identityScores(count int) []RerankScore {
	scores := make([]RerankScore, count)
	for i := range scores {
		scores[i] = RerankScore{Index: i, Score: 1.0}
	}
	return scores
}

// rrfFallbackScores returns Reciprocal Rank Fusion scores (1/(i+1)) as a safe
// degraded fallback that preserves the original document order.
func rrfFallbackScores(count int) []RerankScore {
	scores := make([]RerankScore, count)
	for i := range scores {
		scores[i] = RerankScore{Index: i, Score: 1.0 / float64(i+1)}
	}
	return scores
}
