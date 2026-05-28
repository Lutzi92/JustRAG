package vector

import (
	"github.com/justrag/go-backend/internal/splitter"
)

// Dynamic α (T2-4 Weaviate-Hybrid-2.0 style)
//
// `rerank_blend_alpha` is normally a static per-query-type knob: 0.8
// means "80 % reranker, 20 % RRF" inside EffectiveRerankAlpha. The
// dynamic-alpha heuristic shifts that value per query based on a
// cheap rarity proxy of the query's BPE token IDs.
//
// Mechanism. cl100k_base BPE assigns lower IDs to tokens learned
// early (i.e. frequent in the training corpus) and higher IDs to
// tokens learned later (rarer subwords, domain-specific
// terminology, multilingual). The mean ID across a query is a
// rough but useful rarity proxy: a query like "summarise the
// findings" has mean ID well under 5 000; "Q4-2024 EBITDA
// reconciliation" climbs into the tens of thousands.
//
// Rare → BM25-favouring (lower α). Exact-identifier queries
// (error codes, version strings, named entities) reward BM25's
// surface-form precision over the reranker's paraphrase tolerance.
// Common → reranker-favouring (higher α). Paraphrase-heavy
// abstract questions benefit from semantic matching.
//
// We expose two knobs — `hybrid_dynamic_alpha_enabled` and
// `hybrid_dynamic_alpha_sensitivity` (default 0.3, range [0, 1]).
// Sensitivity caps the magnitude of the shift: at sensitivity=0.3
// the most-rare possible query nudges α down by 0.3 × (1 - neutral)
// ≈ 0.18; the most-common query nudges α up by 0.3 × neutral ≈ 0.12.
// Larger values are reachable for operators willing to validate the
// behaviour against their golden set.
//
// Two intentional simplifications:
//   - We don't normalise by query length. Two-token queries with
//     ID=50 000 each are just as much a "rare" signal as five-token
//     queries with the same mean.
//   - We don't filter out punctuation / whitespace tokens (cl100k
//     bundles them with low IDs anyway). They marginally pull the
//     mean down but the effect is consistent across queries.

// bpeMaxIDForRarity is the divisor that maps mean BPE ID to a [0, 1]
// rarity score. cl100k_base has ~100 256 tokens total, but the
// distribution of IDs in real-world queries is heavily skewed toward
// the low end. Empirically (sample queries, 2026-05):
//   - Common English (3-5 tokens): mean ID 3-5 k
//   - Technical English (Q4-2024 EBITDA …): mean ID ~15 k
//   - German technical / mixed-language: mean ID 14-22 k
// A divisor of 30 000 puts those classes at rarity ~0.1, ~0.5, and
// ~0.6 respectively — meaningful per-class separation without
// pinning extremes at the [0, 1] clamp.
const bpeMaxIDForRarity = 30_000.0

// rarityNeutral is the mid-point: at this rarity score the dynamic
// shift is zero (α stays at the base value). Below → α moves up
// (reranker weighted more); above → α moves down (BM25 weighted
// more). 0.2 corresponds to a mean BPE ID of ~6 000, which the
// sampled common-English queries sit a hair under (so they nudge
// up only slightly) while technical / multilingual queries sit
// well above (and shift down by a useful amount under default
// sensitivity 0.3).
const rarityNeutral = 0.2

// PredictHybridAlpha returns the dynamic alpha for a given query,
// starting from baseAlpha (the operator's per-route value, post
// EffectiveRerankAlphaForQuery resolution) and sensitivity (the
// operator's `hybrid_dynamic_alpha_sensitivity` flag value).
//
// The return value is clamped to [0, 1] so a misconfigured
// sensitivity can never push α out of the EffectiveRerankAlpha
// contract. Empty / un-tokenisable queries return baseAlpha
// unchanged — there's no signal to act on.
//
// sensitivity ≤ 0 is treated as "feature off": baseAlpha passes
// through. This lets the caller flag-gate the feature without
// branching on a separate "enabled" bool — the
// hybrid_dynamic_alpha_enabled gate is the operator's switch,
// while sensitivity=0 is the disabled-but-flag-on safety case.
func PredictHybridAlpha(query string, baseAlpha, sensitivity float64) float64 {
	if sensitivity <= 0 || baseAlpha < 0 || baseAlpha > 1 {
		return baseAlpha
	}
	ids := splitter.EncodeBPE(query)
	if len(ids) == 0 {
		return baseAlpha
	}
	var sum float64
	for _, id := range ids {
		sum += float64(id)
	}
	meanID := sum / float64(len(ids))
	rarity := meanID / bpeMaxIDForRarity
	if rarity > 1 {
		rarity = 1
	}
	// Shift = -sensitivity × (rarity - neutral).
	// rarity > neutral → negative shift (α decreases, BM25 weighted more).
	// rarity < neutral → positive shift (α increases, reranker weighted more).
	shift := -sensitivity * (rarity - rarityNeutral)
	alpha := baseAlpha + shift
	if alpha < 0 {
		return 0
	}
	if alpha > 1 {
		return 1
	}
	return alpha
}

