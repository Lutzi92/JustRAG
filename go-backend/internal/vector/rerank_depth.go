package vector

// MaxRerankCandidateDepth bounds the pre-rerank candidate pool. jina-v3 at
// 500 candidates is already several seconds; anything larger is a
// misconfiguration, not a tuning choice.
const MaxRerankCandidateDepth = 500

// EffectiveRerankDepth resolves how many fused candidates Search() fetches
// per arm before reranking. Sentinel ladder mirrors EffectiveTopN: the
// per-route key (0 = inherit) → the global key (0 = legacy) → the legacy
// formula max(4×limit, 50) with a reranker, max(2×limit, 30) without.
// The result is never below limit and never above MaxRerankCandidateDepth.
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
			depth = max(limit*4, 50)
		} else {
			depth = max(limit*2, 30)
		}
	}
	if depth < limit {
		depth = limit
	}
	if depth > MaxRerankCandidateDepth {
		depth = MaxRerankCandidateDepth
	}
	return depth
}
