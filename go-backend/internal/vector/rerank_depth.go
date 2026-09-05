package vector

// MaxRerankCandidateDepth bounds the pre-rerank candidate pool. jina-v3 at
// 500 candidates is already several seconds; anything larger is a
// misconfiguration, not a tuning choice.
const MaxRerankCandidateDepth = 500

// EffectiveRerankDepth resolves how many fused candidates Search() fetches
// per arm before reranking. Sentinel ladder mirrors EffectiveTopN: the
// per-route key (0 = inherit) → the global key (0 = legacy) → the legacy
// formula max(4×limit, 50) with a reranker, max(2×limit, 30) without.
// The legacy formula is returned as-is, uncapped, matching the pre-branch
// behavior exactly — a long-context query or a KB with a large top_n must
// not get a narrower pool than before this knob existed. The floor (never
// below limit) and the MaxRerankCandidateDepth cap apply only when an
// operator has supplied an explicit depth (global or per-route key).
func EffectiveRerankDepth(cfg KBVectorConfig, queryType string, limit int, rerankerActive bool) int {
	depth := 0
	switch queryType {
	case QueryTypeLookup:
		depth = cfg.RerankCandidateDepthLookup
	case QueryTypeEnumeration:
		depth = cfg.RerankCandidateDepthEnumeration
	case QueryTypeComplexReasoning:
		depth = cfg.RerankCandidateDepthComplexReasoning
	}
	if depth <= 0 {
		depth = cfg.RerankCandidateDepth
	}
	if depth <= 0 {
		if rerankerActive {
			return max(limit*4, 50)
		}
		return max(limit*2, 30)
	}
	if depth < limit {
		depth = limit
	}
	if depth > MaxRerankCandidateDepth {
		depth = MaxRerankCandidateDepth
	}
	return depth
}
