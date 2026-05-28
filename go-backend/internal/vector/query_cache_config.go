package vector

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// queryCacheConfig holds the runtime knobs for the semantic query cache,
// resolved from site_configs at request time. Defaults match the design
// doc: disabled, 0.96 cosine threshold, 24h TTL.
//
// The per-query-type thresholds (similarityThresholdLookup / _Enumeration /
// _ComplexReasoning) override the base threshold for the matching QueryType.
// Sentinel 0 = inherit the base. Rationale: paraphrase-tolerant lookups
// (factoid recall) cache safely at a looser threshold (~0.92); complex
// reasoning queries are paraphrase-sensitive (slight rephrasings can flip
// the answer) and need a tighter threshold (~0.98) to avoid silent wrong
// answers from semantically-near-but-different prior turns.
type queryCacheConfig struct {
	enabled                             bool
	similarityThreshold                 float64
	similarityThresholdLookup           float64
	similarityThresholdEnumeration      float64
	similarityThresholdComplexReasoning float64
	ttl                                 time.Duration
}

// queryCacheConfigKeys are the site_configs keys read by loadQueryCacheConfig.
var queryCacheConfigKeys = []string{
	"query_cache_enabled",
	"query_cache_similarity_threshold",
	"query_cache_similarity_threshold_lookup",
	"query_cache_similarity_threshold_enumeration",
	"query_cache_similarity_threshold_complex_reasoning",
	"query_cache_ttl_hours",
}

func defaultQueryCacheConfig() queryCacheConfig {
	return queryCacheConfig{
		enabled:             false,
		similarityThreshold: 0.96,
		// Sentinel 0 = inherit. Operators set explicit values via the
		// site_config keys; without overrides every query type uses the
		// base threshold (back-compat with single-threshold deployments).
		similarityThresholdLookup:           0,
		similarityThresholdEnumeration:      0,
		similarityThresholdComplexReasoning: 0,
		ttl:                                 24 * time.Hour,
	}
}

// effectiveThreshold returns the per-query-type threshold when set,
// otherwise the base similarityThreshold. Unknown query types (empty
// string, QueryTypeUnknown) inherit the base. Out-of-range overrides
// already filtered to 0 by the loader, so a 0 here always means inherit.
func (c queryCacheConfig) effectiveThreshold(queryType string) float64 {
	switch queryType {
	case QueryTypeLookup:
		if c.similarityThresholdLookup > 0 {
			return c.similarityThresholdLookup
		}
	case QueryTypeEnumeration:
		if c.similarityThresholdEnumeration > 0 {
			return c.similarityThresholdEnumeration
		}
	case QueryTypeComplexReasoning:
		if c.similarityThresholdComplexReasoning > 0 {
			return c.similarityThresholdComplexReasoning
		}
	}
	return c.similarityThreshold
}

// loadQueryCacheConfig reads the cache config from site_configs.
// Invalid values silently fall back to defaults — same pattern as
// loadCRAGConfig in chat/service.go and loadSiteConfig in this package.
// Reuses readSiteConfigBatch + siteConfigBatchReader already declared in
// search.go for consistent batch-read behaviour.
func loadQueryCacheConfig(ctx context.Context, reader SiteConfigReader) queryCacheConfig {
	cfg := defaultQueryCacheConfig()
	if reader == nil {
		return cfg
	}

	values := readSiteConfigBatch(ctx, reader, queryCacheConfigKeys)

	if v, ok := values["query_cache_enabled"]; ok && v != nil {
		switch strings.ToLower(strings.TrimSpace(*v)) {
		case "true", "1":
			cfg.enabled = true
		}
	}
	if v, ok := values["query_cache_similarity_threshold"]; ok && v != nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(*v), 64); err == nil && f > 0 && f <= 1 {
			cfg.similarityThreshold = f
		}
	}
	if v, ok := values["query_cache_similarity_threshold_lookup"]; ok && v != nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(*v), 64); err == nil && f > 0 && f <= 1 {
			cfg.similarityThresholdLookup = f
		}
	}
	if v, ok := values["query_cache_similarity_threshold_enumeration"]; ok && v != nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(*v), 64); err == nil && f > 0 && f <= 1 {
			cfg.similarityThresholdEnumeration = f
		}
	}
	if v, ok := values["query_cache_similarity_threshold_complex_reasoning"]; ok && v != nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(*v), 64); err == nil && f > 0 && f <= 1 {
			cfg.similarityThresholdComplexReasoning = f
		}
	}
	if v, ok := values["query_cache_ttl_hours"]; ok && v != nil {
		if h, err := strconv.Atoi(strings.TrimSpace(*v)); err == nil && h >= 1 && h <= 720 {
			cfg.ttl = time.Duration(h) * time.Hour
		}
	}

	return cfg
}
