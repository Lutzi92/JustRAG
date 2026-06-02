package vector

import "math"

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
