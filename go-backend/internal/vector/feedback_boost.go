package vector

import (
	"cmp"
	"context"
	"math"
	"slices"

	"github.com/justrag/go-backend/internal/logctx"
)

// ApplyFeedbackBoost adds a small, bounded score adjustment to each doc based
// on accumulated user feedback (net = upvotes - downvotes across answers that
// cited the chunk). The adjustment is weight * tanh(net/2), so it saturates at
// ±weight and never dominates the relevance score. Callers must re-sort by
// Score afterward. A zero weight or empty map is a no-op.
func ApplyFeedbackBoost(docs []RankedDoc, net map[string]int, weight float64) {
	if weight <= 0 || len(net) == 0 {
		return
	}
	for i := range docs {
		n, ok := net[docs[i].ID]
		if !ok || n == 0 {
			continue
		}
		docs[i].Score += weight * math.Tanh(float64(n)/2.0)
	}
}

// applyFeedbackPrior is stage 10b of Search: nudge scores by accumulated
// thumbs up/down on answers that cited each chunk, then re-sort by the
// boosted score. Returns the number of net signals applied, 0 when the
// gate is off, no signals exist, or the feedback read failed — fail-open,
// a read error never fails the search.
func (s *SearchService) applyFeedbackPrior(ctx context.Context, kbID string, fused []RankedDoc, siteCfg KBVectorConfig) int {
	if !siteCfg.FeedbackBoostEnabled || s.feedback == nil || len(fused) == 0 {
		return 0
	}
	ids := make([]string, len(fused))
	for i := range fused {
		ids[i] = fused[i].ID
	}
	net, err := s.feedback.NetSignals(ctx, kbID, ids)
	if err != nil {
		logctx.From(ctx).Warn("feedback boost: net signals", "error", err, "kb_id", kbID)
		return 0
	}
	if len(net) == 0 {
		return 0
	}
	weight := siteCfg.FeedbackBoostWeight
	if weight <= 0 {
		weight = 0.05
	}
	ApplyFeedbackBoost(fused, net, weight)
	slices.SortStableFunc(fused, func(a, b RankedDoc) int { return cmp.Compare(b.Score, a.Score) })
	return len(net)
}
