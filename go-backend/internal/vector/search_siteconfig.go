package vector

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
)

// siteConfigSetter parses a raw site_config value and applies it to cfg.
// Each setter is responsible for its own validation; invalid values leave
// the field at its DefaultConfig() value.
type siteConfigSetter func(cfg *KBVectorConfig, v string)

// siteConfigParsers binds each consumed site_configs key to the setter that
// applies it. Adding a new key only requires adding one entry here — the
// parser, the key list used by the batch reader, and the loop in
// loadSiteConfig all derive from this map.
var siteConfigParsers = map[string]siteConfigSetter{
	"min_similarity_threshold": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.MinSimilarityThreshold = f
		}
	},
	"auto_spell_correct": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.AutoSpellCorrect = b
		}
	},
	"mrl_two_pass_enabled": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.MRLTwoPass = b
		}
	},
	"default_top_k": func(cfg *KBVectorConfig, v string) {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 50 {
			cfg.DefaultTopK = n
		}
	},
	"score_drop_threshold": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.ScoreDropThreshold = f
		}
	},
	"rerank_score_drop_enabled": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.RerankScoreDropEnabled = b
		}
	},
	"rerank_score_drop_threshold": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.RerankScoreDropThreshold = f
		}
	},
	"context_window_size": func(cfg *KBVectorConfig, v string) {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 5 {
			cfg.ContextWindowSize = n
		}
	},
	"mmr_lambda": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.MMRLambda = f
		}
	},
	"rerank_blend_alpha": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.RerankBlendAlpha = f
		}
	},
	"rerank_blend_alpha_lookup": func(cfg *KBVectorConfig, v string) {
		// Sentinel-aware: any value outside [0, 1] resets to inherit (-1).
		// We do NOT clamp out-of-range values to 0 or 1 — that would
		// silently change semantics. An invalid input means "operator
		// typed something wrong; keep inheriting the global".
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.RerankBlendAlphaLookup = f
		}
	},
	"rerank_blend_alpha_enumeration": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.RerankBlendAlphaEnumeration = f
		}
	},
	"rerank_blend_alpha_complex_reasoning": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.RerankBlendAlphaComplexReasoning = f
		}
	},
	"top_n_lookup": func(cfg *KBVectorConfig, v string) {
		// Sentinel 0 = inherit DefaultTopK. Valid override range mirrors
		// default_top_k's [1, 50]. Out-of-range silently inherits.
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 50 {
			cfg.TopNLookup = n
		}
	},
	"top_n_enumeration": func(cfg *KBVectorConfig, v string) {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 50 {
			cfg.TopNEnumeration = n
		}
	},
	"top_n_complex_reasoning": func(cfg *KBVectorConfig, v string) {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 50 {
			cfg.TopNComplexReasoning = n
		}
	},
	"rrf_weight_vector": func(cfg *KBVectorConfig, v string) {
		// Allow 0 (disable vector leg) up to 10 (heavily favored). Above
		// 10 is almost certainly a typo — RRF magnitudes are 1/(60+rank+1)
		// so weights >>1 already dominate completely.
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 10 {
			cfg.RRFWeightVector = f
		}
	},
	"rrf_weight_bm25": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 10 {
			cfg.RRFWeightBM25 = f
		}
	},
	"query_instruction": func(cfg *KBVectorConfig, v string) {
		cfg.QueryInstruction = strings.TrimSpace(v)
	},
	"hnsw_ef_search": func(cfg *KBVectorConfig, v string) {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 1000 {
			cfg.HNSWEfSearch = n
		}
	},
	"rerank_use_chat_template": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.RerankUseChatTemplate = b
		}
	},
	"rerank_instruction": func(cfg *KBVectorConfig, v string) {
		cfg.RerankInstruction = strings.TrimSpace(v)
	},
	"rag_fusion_enabled": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.RAGFusionEnabled = b
		}
	},
	"bm25_simple_arm_enabled": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.BM25SimpleArmEnabled = b
		}
	},
	"bm25_tiered_boost_enabled": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.BM25TieredBoost = b
		}
	},
	"hybrid_dynamic_alpha_enabled": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.HybridDynamicAlphaEnabled = b
		}
	},
	"hybrid_dynamic_alpha_sensitivity": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.HybridDynamicAlphaSensitivity = f
		}
	},
	"chat_feedback_boost_enabled": func(cfg *KBVectorConfig, v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.FeedbackBoostEnabled = b
		}
	},
	"feedback_boost_weight": func(cfg *KBVectorConfig, v string) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 0.5 {
			cfg.FeedbackBoostWeight = f
		}
	},
	"rerank_blend_alpha_entity": func(cfg *KBVectorConfig, v string) {
		// Sentinel-aware: any value outside [0, 1] keeps the field at -1
		// (inherit). Same convention as the per-route alpha overrides.
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			cfg.RerankBlendAlphaEntity = f
		}
	},
}

// siteConfigKeys is the materialized key list passed to readSiteConfigBatch.
// Sorted for stable batch-query plans / cache hits across processes.
var siteConfigKeys = func() []string {
	keys := make([]string, 0, len(siteConfigParsers))
	for k := range siteConfigParsers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}()

// loadSiteConfig returns a KBVectorConfig populated from site_configs values,
// falling back to DefaultConfig() for missing or invalid entries.
//
// One DB round-trip per Search call: when the attached reader supports
// batched reads (siteConfigBatchReader, satisfied by chat.PGStore in
// production) all keys are fetched in a single query. Older readers (e.g.
// test fakes) fall back to one-query-per-key.
func (s *SearchService) loadSiteConfig(ctx context.Context) KBVectorConfig {
	cfg := DefaultConfig()
	if s.siteConfig == nil {
		return cfg
	}

	values := readSiteConfigBatch(ctx, s.siteConfig, siteConfigKeys)

	for key, parse := range siteConfigParsers {
		v, ok := values[key]
		if !ok || v == nil {
			continue
		}
		parse(&cfg, *v)
	}

	return cfg
}

// loadSiteConfigCached returns the cached site config when fresh, otherwise
// re-runs loadSiteConfig and seeds the cache. Search calls this on the hot
// path; tests that want fresh values should keep calling loadSiteConfig
// directly. Hot reads are an atomic pointer load + a time comparison;
// refresh goes through singleflight.Group so concurrent refreshers coalesce
// onto a single DB round-trip per TTL window. KBVectorConfig is a value type
// so the snapshot returned to callers is race-free.
//
// The DB call runs inside the singleflight closure rather than under a
// mutex: losing goroutines wait on a channel and don't serialise behind a
// slow DB round-trip the way they would with a refresh lock.
func (s *SearchService) loadSiteConfigCached(ctx context.Context) KBVectorConfig {
	if e := s.siteConfigCache.Load(); e != nil && time.Now().Before(e.expiresAt) {
		return e.cfg
	}

	v, _, _ := s.siteConfigSF.Do("sitecfg", func() (any, error) {
		// Re-check inside the flight so a concurrent refresher that
		// already populated the cache wins without re-fetching.
		if e := s.siteConfigCache.Load(); e != nil && time.Now().Before(e.expiresAt) {
			return e.cfg, nil
		}
		cfg := s.loadSiteConfig(ctx)
		s.siteConfigCache.Store(&siteConfigCacheEntry{
			cfg:       cfg,
			expiresAt: time.Now().Add(siteConfigCacheTTL),
		})
		return cfg, nil
	})
	return v.(KBVectorConfig)
}

// resolveKBLanguage looks up the KB's language with a process-wide snapshot
// cache. The snapshot is rebuilt at most once per kbLangCacheTTL by a single
// goroutine (singleflight coalesces concurrent refreshers); the steady-state
// hot read is an atomic.Pointer load + map lookup with no locking.
//
// Falls back to "de" on any DB error. For a KB that was created AFTER the
// last snapshot refresh (cache hit for the snapshot itself, miss for the
// kbID), we do a single-row QueryRow rather than poisoning the snapshot —
// the next TTL-driven refresh picks it up, and one extra round-trip per new
// KB per TTL window is cheaper than rebuilding the snapshot on every miss.
func (s *SearchService) resolveKBLanguage(ctx context.Context, kbID string) string {
	const fallback = "de"
	if kbID == "" {
		return fallback
	}
	snap := s.kbLangCachePtr.Load()
	if snap == nil || time.Now().After(snap.expiresAt) {
		snap = s.refreshKBLangSnapshot(ctx)
	}
	if lang, ok := snap.langs[kbID]; ok {
		return lang
	}
	var lang string
	err := s.mainDB.QueryRow(ctx, `SELECT language FROM knowledge_bases WHERE id = $1`, kbID).Scan(&lang)
	if err != nil || lang == "" {
		return fallback
	}
	return lang
}

// refreshKBLangSnapshot rebuilds the per-KB language map from one SELECT.
// Wrapped in singleflight so concurrent refreshers serialize onto a single
// DB round-trip per TTL window, mirroring loadSiteConfigCached above.
func (s *SearchService) refreshKBLangSnapshot(ctx context.Context) *kbLangSnapshot {
	v, _, _ := s.kbLangSF.Do("kblang", func() (any, error) {
		// Re-check inside the flight so a concurrent refresher that already
		// populated the cache wins without re-fetching.
		if snap := s.kbLangCachePtr.Load(); snap != nil && time.Now().Before(snap.expiresAt) {
			return snap, nil
		}
		snap := &kbLangSnapshot{
			langs:     make(map[string]string),
			expiresAt: time.Now().Add(kbLangCacheTTL),
		}
		rows, err := s.mainDB.Query(ctx, `SELECT id::text, language FROM knowledge_bases`)
		if err != nil {
			// Don't poison the cache with an empty snapshot on a transient
			// query error; return un-cached so the next call retries instead
			// of falling back to the wrong stemmer for the whole TTL window.
			return snap, nil
		}
		defer rows.Close()
		for rows.Next() {
			var id, lang string
			if scanErr := rows.Scan(&id, &lang); scanErr == nil && lang != "" {
				snap.langs[id] = lang
			}
		}
		if err := rows.Err(); err != nil {
			// A network error mid-stream leaves the map partially populated;
			// caching it would silently route the missing KBs through the
			// wrong language stemmer. Skip the Store so the next refresh
			// retries rather than serving a truncated snapshot.
			return snap, nil
		}
		s.kbLangCachePtr.Store(snap)
		return snap, nil
	})
	return v.(*kbLangSnapshot)
}

// readSiteConfigBatch fetches every key in one round-trip when the reader
// supports it, falling back to one query per key. Errors are swallowed at
// the value level (the existing single-key callers also ignored them) so a
// transient DB blip yields defaults rather than an aborted search.
func readSiteConfigBatch(ctx context.Context, reader SiteConfigReader, keys []string) map[string]*string {
	if br, ok := reader.(siteConfigBatchReader); ok {
		if values, err := br.GetSiteConfigValues(ctx, keys); err == nil {
			return values
		}
	}
	values := make(map[string]*string, len(keys))
	for _, k := range keys {
		if v, err := reader.GetSiteConfigValue(ctx, k); err == nil && v != nil {
			values[k] = v
		}
	}
	return values
}

// maybeLogRerankDefaultNotice emits a one-shot operator notice when a search
// runs with a reranker active but no explicit `rerank_blend_alpha` entry in
// site_configs. Logged once per process to stay out of the steady-state
// log stream. Surfaces the default so operators can tune per KB character
// (keyword-heavy / templated content benefits from a low alpha; pure Q/A
// benefits from a high alpha).
func (s *SearchService) maybeLogRerankDefaultNotice(ctx context.Context) {
	if s.siteConfig == nil {
		return
	}
	s.rerankDefaultNoticeOnce.Do(func() {
		v, err := s.siteConfig.GetSiteConfigValue(ctx, "rerank_blend_alpha")
		if err != nil || v != nil {
			// v != nil: value explicitly set — no silent default in effect.
			// err != nil: lookup failed, so we don't know either way; skip the
			// one-shot notice rather than emit it on every failed read.
			return
		}
		logctx.From(ctx).Info(
			"rag.rerank.blend_alpha_default_applied",
			"message", "rerank_blend_alpha is unset in site_configs; defaulting to 0.5 (50/50 blend of reranker and normalized RRF). "+
				"Set 1.0 for pure reranker dominance, 0.0 to ignore the reranker entirely (e.g. keyword-heavy templated KBs where BM25 carries the evidence).",
			"default", 0.5,
		)
	})
}
