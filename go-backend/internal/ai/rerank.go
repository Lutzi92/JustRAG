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

	// Qwen3-Reranker chat-template path. Gated by the explicit admin opt-in
	// AND by a model-family check: switching a Cohere/Voyage endpoint onto
	// the chat template would fail outright. On any per-pair error we fall
	// through to the legacy /rerank endpoint below — same fail-open pattern
	// as the rest of this function.
	if opts.UseChatTemplate && IsQwen3RerankerModel(cfg.RerankModel) {
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
