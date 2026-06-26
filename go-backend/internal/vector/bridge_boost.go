package vector

import (
	"cmp"
	"slices"
)

// bridgeFreqCap bounds the frequency multiplier so a chunk on many bridge
// paths can't dominate the reranker: 1 path → weight/3, 2 → 2*weight/3,
// 3+ → weight.
const bridgeFreqCap = 3

// ApplyBridgeBoost adds a bounded, frequency-weighted score adjustment to each
// doc lying on one or more KG bridge paths between the query's matched
// entities (bridge maps chunk id → path count). Mirrors ApplyRecencyBoost:
// in-place Score mutation; the caller re-sorts. Non-positive weight or empty
// bridge map is a no-op.
func ApplyBridgeBoost(docs []RankedDoc, bridge map[string]int, weight float64) {
	if weight <= 0 || len(bridge) == 0 {
		return
	}
	for i := range docs {
		n, ok := bridge[docs[i].ID]
		if !ok || n <= 0 {
			continue
		}
		if n > bridgeFreqCap {
			n = bridgeFreqCap
		}
		docs[i].Score += weight * float64(n) / float64(bridgeFreqCap)
	}
}

// applyBridgePrior is the bridge-evidence boost stage of Search: nudge fused
// scores for chunks on KG bridge paths (supplied by the chat layer via
// opts.BridgeChunks) and re-sort. Returns the number of bridge chunks present
// in the result set, 0 when there is nothing to boost. No DB read; never errors.
func (s *SearchService) applyBridgePrior(fused []RankedDoc, opts SearchOptions, siteCfg KBVectorConfig) int {
	if len(opts.BridgeChunks) == 0 || siteCfg.BridgeBoostWeight <= 0 || len(fused) == 0 {
		return 0
	}
	ApplyBridgeBoost(fused, opts.BridgeChunks, siteCfg.BridgeBoostWeight)
	slices.SortStableFunc(fused, func(a, b RankedDoc) int { return cmp.Compare(b.Score, a.Score) })
	n := 0
	for i := range fused {
		if c, ok := opts.BridgeChunks[fused[i].ID]; ok && c > 0 {
			n++
		}
	}
	return n
}
