package vector

import (
	"context"
	"testing"
	"time"
)

func TestLoadQueryCacheConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := loadQueryCacheConfig(context.Background(), nil)
	if cfg.enabled {
		t.Errorf("default should be disabled")
	}
	if cfg.similarityThreshold != 0.96 {
		t.Errorf("default threshold: want 0.96, got %v", cfg.similarityThreshold)
	}
	if cfg.ttl != 24*time.Hour {
		t.Errorf("default ttl: want 24h, got %v", cfg.ttl)
	}
}

func TestLoadQueryCacheConfig_OverridesFromReader(t *testing.T) {
	t.Parallel()
	reader := &stubSiteConfig{values: map[string]*string{
		"query_cache_enabled":              strPtr("true"),
		"query_cache_similarity_threshold": strPtr("0.92"),
		"query_cache_ttl_hours":            strPtr("12"),
	}}
	cfg := loadQueryCacheConfig(context.Background(), reader)
	if !cfg.enabled {
		t.Errorf("enabled should be true")
	}
	if cfg.similarityThreshold != 0.92 {
		t.Errorf("want threshold 0.92, got %v", cfg.similarityThreshold)
	}
	if cfg.ttl != 12*time.Hour {
		t.Errorf("want ttl 12h, got %v", cfg.ttl)
	}
}

func TestLoadQueryCacheConfig_InvalidValuesFallBackToDefaults(t *testing.T) {
	t.Parallel()
	reader := &stubSiteConfig{values: map[string]*string{
		"query_cache_similarity_threshold": strPtr("not_a_float"),
		"query_cache_ttl_hours":            strPtr("-5"),
	}}
	cfg := loadQueryCacheConfig(context.Background(), reader)
	if cfg.similarityThreshold != 0.96 {
		t.Errorf("invalid threshold should fall back: got %v", cfg.similarityThreshold)
	}
	if cfg.ttl != 24*time.Hour {
		t.Errorf("invalid ttl should fall back: got %v", cfg.ttl)
	}
}

// TestQueryCacheConfig_EffectiveThreshold covers the per-query-type
// threshold override matrix. Documents the inheritance contract:
// unset/zero per-type values fall back to the base threshold; unknown
// query types always inherit the base.
func TestQueryCacheConfig_EffectiveThreshold(t *testing.T) {
	t.Parallel()

	// Base only — every query type inherits.
	base := queryCacheConfig{similarityThreshold: 0.96}
	for _, qt := range []string{QueryTypeLookup, QueryTypeEnumeration, QueryTypeComplexReasoning, QueryTypeUnknown, ""} {
		if got := base.effectiveThreshold(qt); got != 0.96 {
			t.Errorf("queryType=%q: want base 0.96, got %v", qt, got)
		}
	}

	// All per-type overrides set.
	cfg := queryCacheConfig{
		similarityThreshold:                 0.96,
		similarityThresholdLookup:           0.92,
		similarityThresholdEnumeration:      0.94,
		similarityThresholdComplexReasoning: 0.98,
	}
	cases := []struct {
		queryType string
		want      float64
	}{
		{QueryTypeLookup, 0.92},
		{QueryTypeEnumeration, 0.94},
		{QueryTypeComplexReasoning, 0.98},
		{QueryTypeUnknown, 0.96}, // inherits
		{"", 0.96},               // inherits
		{"bogus", 0.96},          // inherits
	}
	for _, tc := range cases {
		if got := cfg.effectiveThreshold(tc.queryType); got != tc.want {
			t.Errorf("queryType=%q: want %v, got %v", tc.queryType, tc.want, got)
		}
	}

	// Mixed: some overrides set, some left at sentinel 0.
	mixed := queryCacheConfig{
		similarityThreshold:                 0.96,
		similarityThresholdComplexReasoning: 0.99,
		// Lookup / Enumeration sentinels stay at 0 → inherit.
	}
	if got := mixed.effectiveThreshold(QueryTypeLookup); got != 0.96 {
		t.Errorf("Lookup with unset override: want inherit 0.96, got %v", got)
	}
	if got := mixed.effectiveThreshold(QueryTypeComplexReasoning); got != 0.99 {
		t.Errorf("ComplexReasoning override: want 0.99, got %v", got)
	}
}

func TestLoadQueryCacheConfig_PerTypeThresholdsFromReader(t *testing.T) {
	t.Parallel()
	reader := &stubSiteConfig{values: map[string]*string{
		"query_cache_similarity_threshold":                   strPtr("0.96"),
		"query_cache_similarity_threshold_lookup":            strPtr("0.92"),
		"query_cache_similarity_threshold_complex_reasoning": strPtr("0.98"),
		// Enumeration intentionally omitted — should stay at sentinel 0 (inherit).
	}}
	cfg := loadQueryCacheConfig(context.Background(), reader)
	if cfg.similarityThresholdLookup != 0.92 {
		t.Errorf("lookup override: want 0.92, got %v", cfg.similarityThresholdLookup)
	}
	if cfg.similarityThresholdEnumeration != 0 {
		t.Errorf("enumeration omitted: want sentinel 0, got %v", cfg.similarityThresholdEnumeration)
	}
	if cfg.similarityThresholdComplexReasoning != 0.98 {
		t.Errorf("complex_reasoning override: want 0.98, got %v", cfg.similarityThresholdComplexReasoning)
	}
	// Resolution via the live helper.
	if got := cfg.effectiveThreshold(QueryTypeEnumeration); got != 0.96 {
		t.Errorf("Enumeration should inherit base 0.96, got %v", got)
	}
}

func TestLoadQueryCacheConfig_PerTypeOutOfRangeIgnored(t *testing.T) {
	t.Parallel()
	reader := &stubSiteConfig{values: map[string]*string{
		"query_cache_similarity_threshold_lookup":            strPtr("1.5"), // > 1
		"query_cache_similarity_threshold_enumeration":       strPtr("-0.1"),
		"query_cache_similarity_threshold_complex_reasoning": strPtr("oops"),
	}}
	cfg := loadQueryCacheConfig(context.Background(), reader)
	if cfg.similarityThresholdLookup != 0 {
		t.Errorf("out-of-range lookup should stay at sentinel 0, got %v", cfg.similarityThresholdLookup)
	}
	if cfg.similarityThresholdEnumeration != 0 {
		t.Errorf("negative enumeration should stay at sentinel 0, got %v", cfg.similarityThresholdEnumeration)
	}
	if cfg.similarityThresholdComplexReasoning != 0 {
		t.Errorf("non-numeric complex_reasoning should stay at sentinel 0, got %v", cfg.similarityThresholdComplexReasoning)
	}
}
