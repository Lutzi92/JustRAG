package vector

import "testing"

func TestEffectiveRerankDepth_LegacyWhenUnset(t *testing.T) {
	cfg := KBVectorConfig{}
	if got := EffectiveRerankDepth(cfg, QueryTypeLookup, 10, true); got != 50 { // max(4*10, 50)
		t.Fatalf("reranker legacy: want 50, got %d", got)
	}
	if got := EffectiveRerankDepth(cfg, QueryTypeLookup, 20, true); got != 80 {
		t.Fatalf("reranker legacy 4x: want 80, got %d", got)
	}
	if got := EffectiveRerankDepth(cfg, QueryTypeLookup, 10, false); got != 30 { // max(2*10, 30)
		t.Fatalf("no-reranker legacy: want 30, got %d", got)
	}
	if got := EffectiveRerankDepth(cfg, QueryTypeLookup, 20, false); got != 40 {
		t.Fatalf("no-reranker legacy 2x: want 40, got %d", got)
	}
}

func TestEffectiveRerankDepth_GlobalAndRouteOverrides(t *testing.T) {
	cfg := KBVectorConfig{RerankCandidateDepth: 120, RerankCandidateDepthLookup: 60}
	if got := EffectiveRerankDepth(cfg, QueryTypeLookup, 10, true); got != 60 {
		t.Fatalf("route override: want 60, got %d", got)
	}
	if got := EffectiveRerankDepth(cfg, QueryTypeComplexReasoning, 10, true); got != 120 {
		t.Fatalf("global fallback: want 120, got %d", got)
	}
	if got := EffectiveRerankDepth(cfg, QueryTypeUnknown, 10, true); got != 120 {
		t.Fatalf("unknown route uses global: want 120, got %d", got)
	}
}

func TestEffectiveRerankDepth_NeverBelowLimitAndCapped(t *testing.T) {
	cfg := KBVectorConfig{RerankCandidateDepth: 20}
	if got := EffectiveRerankDepth(cfg, QueryTypeLookup, 40, true); got != 40 {
		t.Fatalf("depth below limit must floor at limit: want 40, got %d", got)
	}
	cfg = KBVectorConfig{RerankCandidateDepth: 9999}
	if got := EffectiveRerankDepth(cfg, QueryTypeLookup, 10, true); got != MaxRerankCandidateDepth {
		t.Fatalf("cap: want %d, got %d", MaxRerankCandidateDepth, got)
	}
}

// TestEffectiveRerankDepth_LegacyIsUncapped asserts the legacy formula
// (no explicit RerankCandidateDepth/per-route key configured) is returned
// uncapped, exactly as it was before MaxRerankCandidateDepth existed. A
// long-context query (limit 200 → legacy 4x = 800) or a KB with a large
// top_n must not get a narrower rerank pool than before this branch.
func TestEffectiveRerankDepth_LegacyIsUncapped(t *testing.T) {
	cfg := KBVectorConfig{}
	if got := EffectiveRerankDepth(cfg, QueryTypeComplexReasoning, 200, true); got != 800 {
		t.Fatalf("legacy uncapped 4x: want 800, got %d", got)
	}
	if got := EffectiveRerankDepth(cfg, QueryTypeComplexReasoning, 150, true); got != 600 {
		t.Fatalf("legacy uncapped 4x: want 600, got %d", got)
	}
}
