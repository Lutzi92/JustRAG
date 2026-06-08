package vector

import (
	"fmt"
	"regexp"
)

// validVectorTable matches table names produced by GetVectorTableName and
// GetHyPETableName:
//
//   - "document_chunks" or "document_chunks_{digits}"   (chunk tables)
//   - "chunk_hype_questions" or "chunk_hype_questions_{digits}" (HyPE tables)
//
// Exposed to other packages via IsValidVectorTableName so callers that
// interpolate a table name into SQL (worker maintenance, future tooling)
// reuse the same canonical pattern instead of redefining their own regex
// that could drift out of sync with the table-name generator functions.
var validVectorTable = regexp.MustCompile(`^(?:document_chunks|chunk_hype_questions)(?:_\d+)?$`)

// IsValidVectorTableName reports whether name matches the format produced
// by GetVectorTableName: "document_chunks" or "document_chunks_<digits>".
// Callers that interpolate a vector chunks table name into SQL MUST gate
// the interpolation on this check — the pattern is the only allowlist
// preventing identifier injection on the maintenance / dynamic-DDL paths.
func IsValidVectorTableName(name string) bool {
	return validVectorTable.MatchString(name)
}

// KBVectorConfig holds vector search configuration for a knowledge base.
type KBVectorConfig struct {
	Dimensions        int
	TableName         string
	RerankModel       string
	Language          string // "de", "en", etc.
	DefaultTopK       int
	ContextWindowSize int

	// MinSimilarityThreshold is the absolute floor: chunks with an RRF score
	// below this value are discarded outright. Applied only when no reranker
	// is active (the reranker provides its own quality signal).
	//
	// ScoreDropThreshold works relative to the top-scoring chunk: once a
	// chunk's score falls below ScoreDropThreshold * topScore, it and all
	// subsequent chunks are cut (elbow detection). Also skipped when a
	// reranker is used.
	//
	// Together they form a two-stage filter:
	//   1. ApplyMinThreshold removes globally weak results.
	//   2. ApplyScoreDrop removes the long tail relative to the best hit.
	// Keep MinSimilarityThreshold low (0.2–0.4) to avoid discarding valid
	// results with modest RRF scores. ScoreDropThreshold (0.10–0.25) trims
	// the trailing noise. If too many results are filtered, lower
	// MinSimilarityThreshold first; if irrelevant tail results remain,
	// raise ScoreDropThreshold.
	MinSimilarityThreshold float64
	ScoreDropThreshold     float64

	// RerankScoreDropEnabled extends ApplyScoreDrop's elbow detection to the
	// reranker-active path. The two filters above are skipped when a reranker
	// runs (the blended rerank score is trusted), but a sharp gap in that
	// blended, normalized score still marks a natural relevance cliff. When
	// enabled, the tail past the elbow is cut on the post-rerank list using
	// RerankScoreDropThreshold (same ratio-of-top semantics as
	// ScoreDropThreshold). Default OFF — retrieval stays byte-stable until an
	// operator enables it and validates the threshold on a golden set. Never
	// applied in long-context mode (which deliberately keeps the wide pool).
	RerankScoreDropEnabled   bool
	RerankScoreDropThreshold float64

	// MMRLambda controls the relevance-diversity trade-off in Maximal Marginal
	// Relevance reranking. 1.0 = pure relevance (MMR is a no-op), 0.0 = pure
	// diversity. Tunable via site_configs key "mmr_lambda".
	MMRLambda float64

	// RerankBlendAlpha is the global blend weight when no per-query-type
	// override applies. The final score is
	// `alpha * rerankScore + (1 - alpha) * normalizedRRF`. 0.8 (default)
	// weights the reranker 80/20 over the RRF-fused hybrid signal, which
	// reflects the calibration of the current jina-reranker-v3 deployment.
	// The RRF half still carries BM25 evidence that cross-encoders can
	// downrank on templated / keyword-heavy content (e.g. project-profile
	// team lists in a named-entity query). Set 1.0 for pure reranker
	// dominance, 0.0 to ignore the reranker entirely. Tunable via
	// site_configs key "rerank_blend_alpha".
	RerankBlendAlpha float64

	// RerankBlendAlphaLookup overrides RerankBlendAlpha when the query
	// is classified as QueryTypeLookup. Sentinel -1 (or any value < 0)
	// means "inherit RerankBlendAlpha". Tunable via site_configs key
	// "rerank_blend_alpha_lookup". Lookup queries (named-entity needles
	// on templated docs) typically benefit from a lower alpha so BM25's
	// exact-match signal isn't overridden by cross-encoder downranking
	// of structural chunks (the same failure mode the BM25 floor
	// addresses defensively).
	RerankBlendAlphaLookup float64

	// RerankBlendAlphaEnumeration overrides RerankBlendAlpha when the
	// query is classified as QueryTypeEnumeration. Sentinel -1 = inherit.
	// Tunable via site_configs key "rerank_blend_alpha_enumeration".
	RerankBlendAlphaEnumeration float64

	// RerankBlendAlphaComplexReasoning overrides RerankBlendAlpha when
	// the query is classified as QueryTypeComplexReasoning. Sentinel
	// -1 = inherit. Tunable via site_configs key
	// "rerank_blend_alpha_complex_reasoning". Complex queries usually
	// benefit from a higher alpha — semantic relevance matters more than
	// exact-string match.
	RerankBlendAlphaComplexReasoning float64

	// TopNLookup overrides the global default_top_k when the query is
	// classified as QueryTypeLookup. Sentinel 0 (or any value < 1) means
	// "inherit DefaultTopK". Tunable via site_configs key "top_n_lookup".
	// Lookup queries typically need a smaller candidate pool — they have
	// one canonical answer chunk and a wider pool just adds reranker
	// latency and reduces context density.
	TopNLookup int

	// TopNEnumeration overrides DefaultTopK when QueryTypeEnumeration is
	// set. Sentinel 0 = inherit. Tunable via site_configs key
	// "top_n_enumeration".
	TopNEnumeration int

	// TopNComplexReasoning overrides DefaultTopK when QueryTypeComplexReasoning
	// is set. Sentinel 0 = inherit. Tunable via site_configs key
	// "top_n_complex_reasoning". Complex queries usually benefit from a
	// wider pre-rerank pool because relevant chunks span multiple sources.
	TopNComplexReasoning int

	AutoSpellCorrect bool

	// QueryInstruction is the natural-language task description fed to
	// asymmetric embedding models (Qwen3-Embedding family) as the query-side
	// prefix, via the template:
	//
	//	Instruct: {instruction}
	//	Query: {query}
	//
	// Document embeddings stay bare — that's the trained behavior. Empty
	// string disables the wrapping entirely, which is the correct setting for
	// symmetric embedders (OpenAI `text-embedding-3-*`, Cohere, etc.) where
	// an instruction prefix would poison the vector. Tunable via site_configs
	// key "query_instruction". Also affects cache keys: the wrapped input is
	// what lands in the embedding cache, so query-side and document-side
	// encodings of the same string never collide.
	QueryInstruction string

	// RerankUseChatTemplate routes reranking through the Qwen3-Reranker
	// chat-template yes/no scorer (one chat completion per (query, doc)
	// pair, softmax over the first-token "yes"/"no" logprobs) instead of
	// the provider's /rerank endpoint. Only effective when the configured
	// reranker model is a Qwen3-Reranker variant AND the backend exposes
	// `top_logprobs` on chat completions (vLLM and sglang do). Off by
	// default — flipping this on a Cohere/Voyage reranker would either
	// no-op (model-family check skips the new path) or hit a backend that
	// doesn't support the template, so explicit opt-in prevents foot-guns.
	// Tunable via site_configs key "rerank_use_chat_template".
	RerankUseChatTemplate bool

	// RerankInstruction is the `<Instruct>:` tag value fed to Qwen3's
	// reranker template. Only used when RerankUseChatTemplate is true.
	// Empty = the default from the Qwen3 model card. Tunable via
	// site_configs key "rerank_instruction".
	RerankInstruction string

	// HNSWEfSearch sets pgvector's per-query `hnsw.ef_search` via SET LOCAL
	// inside runVectorSearch. Higher values raise recall and latency by
	// expanding the HNSW candidate list during search. Tunable via
	// site_configs key "hnsw_ef_search". Valid range 1–1000; out-of-range
	// values fall back to DefaultConfig().HNSWEfSearch (150).
	HNSWEfSearch int

	// MRLTwoPass enables Matryoshka two-pass retrieval: the HNSW candidate
	// scan runs against a 256-dim L2-normalized projection of each
	// embedding, then the candidates are re-ranked against the full-dim
	// vector inside Postgres. Off by default. Tunable via site_configs key
	// "mrl_two_pass_enabled". Requires the embedding_low column to be
	// populated; toggling on with no backfill yields zero results until
	// EnsureChunkTable + the 0009 vector migration have run.
	MRLTwoPass bool

	// RRFWeightVector multiplies the RRF contribution of the dense
	// vector retrieval list (and any multi-query alternative vector
	// lists) during fusion. Default 1.0 = unweighted (current
	// behavior). Tunable via site_configs key "rrf_weight_vector".
	RRFWeightVector float64

	// RRFWeightBM25 multiplies the RRF contribution of the BM25 keyword
	// retrieval list during fusion. Default 1.0 = unweighted. Raise
	// above 1.0 on KBs with many named-entity / exact-string lookup
	// queries; lower below 1.0 for conversational FAQ-style KBs.
	// Tunable via site_configs key "rrf_weight_bm25".
	RRFWeightBM25 float64

	// RAGFusionEnabled is the P4 gate: when true AND the multi-query
	// / step-back / sub-queries path produces alternative phrasings,
	// each alt query also fires a BM25 keyword search whose ranked
	// list folds into RRF alongside the existing per-alt vector
	// list. Off by default — flipping it adds up to N extra BM25
	// queries per chat turn (N = number of alt queries, typically 3)
	// and shifts the BM25/vector balance in the RRF pool. Operators
	// re-tune `rrf_weight_vector` / `rrf_weight_bm25` against the
	// golden set after enabling. Tunable via "rag_fusion_enabled".
	RAGFusionEnabled bool

	// BM25SimpleArmEnabled is the prototype-A gate: when true, the
	// keyword-search SQL ORs against a second tsvector column
	// (`vector_index_simple`) built with the `simple` text-search config
	// (no stemming, no language-specific stop words). The two arms'
	// ts_rank values are summed so a chunk that matches in the
	// language-stemmed arm OR the surface-form arm contributes to the
	// BM25 candidate list. Targets entity-anchored lookup queries where
	// the German stemmer collapses surnames into adjective stems (Krüger
	// → krug, Vorlage → vorlag, …). Off by default. Tunable via
	// "bm25_simple_arm_enabled". Requires the vector_index_simple column
	// to be populated; EnsureChunkTable + migration 0012 handle that
	// idempotently on boot. Toggling on without re-ingesting older
	// chunks still works because migration 0012 backfills existing rows.
	BM25SimpleArmEnabled bool

	// HybridDynamicAlphaEnabled gates the T2-4 per-query alpha shift.
	// When true (and HybridDynamicAlphaSensitivity > 0), the
	// effective `rerank_blend_alpha` for each query is adjusted by
	// PredictHybridAlpha based on the query's BPE-ID rarity — rare
	// queries (named entities, error codes) get a lower α (more BM25
	// weight), common queries get a higher α (more reranker weight).
	// Orthogonal to the per-route alpha overrides
	// (RerankBlendAlphaLookup / _Enumeration / _ComplexReasoning):
	// those resolve first, this heuristic shifts the resulting base.
	// Off by default. Tunable via "hybrid_dynamic_alpha_enabled".
	HybridDynamicAlphaEnabled bool

	// HybridDynamicAlphaSensitivity caps the magnitude of the per-
	// query shift. At 0 the heuristic is effectively off (returns
	// baseAlpha unchanged) regardless of HybridDynamicAlphaEnabled;
	// at 1 the most-rare query can shift α by up to (1 - neutral)
	// ≈ 0.6 of its full range. Default 0.3 keeps shifts in the
	// ±0.18 band — large enough to matter, small enough that an
	// over-corrected query stays in a sensible α range. Range [0, 1].
	// Tunable via "hybrid_dynamic_alpha_sensitivity".
	HybridDynamicAlphaSensitivity float64

	// FeedbackBoostEnabled gates the online-feedback retrieval boost
	// ("chat_feedback_boost_enabled"). Off by default.
	FeedbackBoostEnabled bool
	// FeedbackBoostWeight is the max |score adjustment| from feedback
	// ("feedback_boost_weight"); 0 falls back to 0.05 at apply time.
	FeedbackBoostWeight float64

	// BM25TieredBoost gates a multiplicative ranking adjustment in the BM25
	// keyword arm: chunks matching the strict (AND-required) websearch
	// form score ×100, chunks matching only the OR-tokens recall floor
	// (or proper-noun fallback) score ×10. Single-token queries — where
	// `composed` collapses to `websearchClause` alone — are unaffected
	// because every match satisfies the strict form, yielding a uniform
	// scale that re-normalises out at the RRF stage. Phrases-only queries
	// (no remainder) skip the boost entirely (no websearch group exists).
	// External validation reported +7.5% NDCG; flip on after onsite
	// golden-set eval. Off by default. Tunable via
	// "bm25_tiered_boost_enabled".
	BM25TieredBoost bool

	// RerankBlendAlphaEntity overrides RerankBlendAlpha when the query
	// matches the entity-asking heuristic (isEntityAskingQuery: starts
	// with "Wer/Wem/Wen/Who…" or contains "welche rolle / funktion / …"
	// patterns). Sentinel -1 = inherit. Tunable via site_configs key
	// "rerank_blend_alpha_entity". Takes precedence over the per-route
	// overrides because entity-asking is orthogonal to query-type
	// classification — a query can be both `lookup` and entity-asking,
	// and the failure mode is reranker saturation across many lookalike
	// chunks all matching the role/function semantics, which a lower
	// alpha (more BM25 weight) recovers via phrase-match evidence.
	RerankBlendAlphaEntity float64
}

// GetVectorTableName returns the pgvector table name for the given dimensions.
// The default 1536-dimension table uses the legacy name "document_chunks"
// (without suffix) for backward compatibility with the original Drizzle schema.
// All other dimensions use "document_chunks_{dimensions}".
//
// Return value is always safe to interpolate into SQL: dimensions is an int,
// so the suffix is digits-only and the constant prefix is hard-coded. Callers
// still double-quote the result to be defensive.
func GetVectorTableName(dimensions int) string {
	if dimensions == 1536 {
		return "document_chunks"
	}
	return fmt.Sprintf("document_chunks_%d", dimensions)
}

// PgTextSearchConfig maps a language code to PostgreSQL text search configuration.
func PgTextSearchConfig(lang string) string {
	switch lang {
	case "de":
		return "german"
	case "en":
		return "english"
	case "fr":
		return "french"
	case "es":
		return "spanish"
	case "it":
		return "italian"
	case "pt":
		return "portuguese"
	case "nl":
		return "dutch"
	case "sv":
		return "swedish"
	case "no":
		return "norwegian"
	case "da":
		return "danish"
	case "fi":
		return "finnish"
	case "ru":
		return "russian"
	case "tr":
		return "turkish"
	default:
		return "english"
	}
}

// DefaultConfig returns a KBVectorConfig with sensible defaults.
func DefaultConfig() KBVectorConfig {
	return KBVectorConfig{
		Dimensions:                       1536,
		TableName:                        "document_chunks",
		Language:                         "de",
		MinSimilarityThreshold:           0.3,
		AutoSpellCorrect:                 false,
		ScoreDropThreshold:               0.15,
		// RerankScoreDropEnabled defaults false (inert). The threshold is
		// pre-seeded at the documented midpoint so the first enable already
		// trims a tail; operators tune it against a golden set from there.
		RerankScoreDropThreshold: 0.5,
		DefaultTopK:                      15,
		ContextWindowSize:                3,
		MMRLambda:                        0.7,
		RerankBlendAlpha:                 0.8,
		RerankBlendAlphaLookup:           -1, // sentinel: inherit
		RerankBlendAlphaEnumeration:      -1,
		RerankBlendAlphaComplexReasoning: -1,
		RerankBlendAlphaEntity:           -1,
		TopNLookup:                       0,
		TopNEnumeration:                  0,
		TopNComplexReasoning:             0,
		HNSWEfSearch:                     150,
		RRFWeightVector:                  1.0,
		RRFWeightBM25:                    1.0,
		// T2-4 dynamic alpha: default 0.3 sensitivity is "feature
		// designed but inert" — the heuristic only fires when the
		// operator flips HybridDynamicAlphaEnabled to true. Setting
		// the default at the documented operational midpoint means
		// the very first flip-on already produces measurable shifts
		// without a separate config step.
		HybridDynamicAlphaSensitivity: 0.3,
	}
}
