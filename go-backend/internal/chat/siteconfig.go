package chat

import (
	"context"
	"strconv"
	"strings"

	"github.com/justrag/go-backend/internal/siteconfig"
)

// ---------------------------------------------------------------------------
// Reader interfaces
// ---------------------------------------------------------------------------

// SiteConfigReader is the minimal site-config interface the chat layer needs.
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// siteConfigBatchReader aliases the shared siteconfig.BatchReader. The single
// source of truth lives in internal/siteconfig so a method-signature change on
// the concrete store updates every consumer in lock-step.
type siteConfigBatchReader = siteconfig.BatchReader

// ---------------------------------------------------------------------------
// Generic helpers
// ---------------------------------------------------------------------------

// parseBool parses a trimmed, case-insensitive "true"/"1"/"false"/"0" into a
// bool, falling back to def when v is nil or unparseable. Operators may set
// "True", " false ", etc.; everything else returns def.
func parseBool(v *string, def bool) bool {
	if v == nil {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(*v)) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		return def
	}
}

// parseInt parses a trimmed integer clamped to [lo, hi]. Falls back to def
// when v is nil, doesn't parse, or sits outside [lo, hi].
func parseInt(v *string, def, lo, hi int) int {
	if v == nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(*v))
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

// parseFloat parses a trimmed float clamped to [lo, hi]. Falls back to def
// when v is nil, doesn't parse, or sits outside [lo, hi].
func parseFloat(v *string, def, lo, hi float64) float64 {
	if v == nil {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(*v), 64)
	if err != nil || f < lo || f > hi {
		return def
	}
	return f
}

// parseString returns the trimmed value, or "" when v is nil.
func parseString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

// readBool returns the parsed boolean value of key, or def when the reader is
// nil, the key is missing, the read errors, or the value parses to neither
// "true"/"1" nor "false"/"0". Comparison is case-insensitive and
// whitespace-trimmed so operators can set "True", " false ", etc.
func readBool(ctx context.Context, r SiteConfigReader, key string, def bool) bool {
	if r == nil {
		return def
	}
	v, err := r.GetSiteConfigValue(ctx, key)
	if err != nil {
		return def
	}
	return parseBool(v, def)
}

// readInt returns the parsed integer value of key, clamped to [lo, hi]. Falls
// back to def when the reader is nil, the key is missing, the read errors,
// the value doesn't parse, or the parsed value is outside [lo, hi].
func readInt(ctx context.Context, r SiteConfigReader, key string, def, lo, hi int) int {
	if r == nil {
		return def
	}
	v, err := r.GetSiteConfigValue(ctx, key)
	if err != nil {
		return def
	}
	return parseInt(v, def, lo, hi)
}

// readFloat returns the parsed float value of key, clamped to [lo, hi]. Falls
// back to def when the reader is nil, the key is missing, the read errors,
// the value doesn't parse, or the parsed value is outside [lo, hi].
func readFloat(ctx context.Context, r SiteConfigReader, key string, def, lo, hi float64) float64 {
	if r == nil {
		return def
	}
	v, err := r.GetSiteConfigValue(ctx, key)
	if err != nil {
		return def
	}
	return parseFloat(v, def, lo, hi)
}

// readString returns the trimmed string value of key, or "" when the reader
// is nil, the key is missing, or the read errors.
func readString(ctx context.Context, r SiteConfigReader, key string) string {
	if r == nil {
		return ""
	}
	v, err := r.GetSiteConfigValue(ctx, key)
	if err != nil {
		return ""
	}
	return parseString(v)
}

// readSiteConfigBatch fetches keys in one round-trip when the reader
// supports it; otherwise falls back to per-key reads. Errors are swallowed —
// missing values let callers apply their defaults.
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

// ---------------------------------------------------------------------------
// Public accessors
//
// Each function is a one-liner over one of the four helpers above. The
// doc comment explains semantics; the body is the contract.
// ---------------------------------------------------------------------------

// ChatModelTierFast is the deployment-wide default model for fast-tier
// tasks: classification, structured extraction, per-chunk verdicts,
// short critic decisions. Tasks like CRAG grading, KG extraction,
// contextual enrichment, the DAG critic, the Self-RAG verifier, the
// long-term memory extractor, and the KB router all consult this
// value as a fallback when no per-task override is set.
//
// Empty (default) preserves the legacy behaviour: each task falls
// back directly to the KB's chat model. Operators set this once
// (e.g. "gemma-4-4b-it") to point every fast-tier task at a smaller
// model in one configuration change instead of duplicating the same
// override across 6+ keys. Tunable via "model_tier_fast".
//
// The reasoning tier is not gated by a sibling key in v1 — answer
// generation, plan decomposition, and the refine path already use
// the KB chat model directly, so a "model_tier_reasoning" knob would
// duplicate the existing default. If a deployment ever wants to
// route reasoning-heavy tasks to a separately-configured model
// instead of the KB chat model, that's a follow-up.
func ChatModelTierFast(ctx context.Context, reader SiteConfigReader) string {
	return readString(ctx, reader, "model_tier_fast")
}

// ResolveFastTierModel returns the model to use for a fast-tier
// task. Resolution chain (first non-empty wins):
//  1. per-task override (perTaskKey, e.g. "crag_grader_model")
//  2. model_tier_fast (deployment-wide fast-tier default)
//  3. "" (caller falls back to the KB's default chat model)
//
// Trimmed strings; whitespace-only counts as empty so a config
// scrub doesn't accidentally pin a task to "   ".
//
// Use this from any reader for a task that's classified as
// fast-tier. Consumers in other packages (e.g. processor) call
// this directly with their own SiteConfigReader value — both
// interfaces have the same single method so the cross-package
// type satisfaction is structural.
func ResolveFastTierModel(ctx context.Context, reader SiteConfigReader, perTaskKey string) string {
	if v := readString(ctx, reader, perTaskKey); v != "" {
		return v
	}
	return ChatModelTierFast(ctx, reader)
}

// EnrichmentModel returns the chat model that should be used for auxiliary
// planning LLM calls in the deep-chat path (HyDE, MultiQuery, RewriteQuery,
// ExpandQuery). Empty string means "use the resolved ChatModel" — i.e. no
// override. Admins configure this via the `contextual_enrichment_model`
// site config so deep-chat planning can run on a faster/cheaper model while
// the final streamed answer stays on the main model.
//
// Falls back through the fast-tier chain (per-task →
// `model_tier_fast` → empty) so a deployment can pin every fast
// task at one model via the tier key alone.
func EnrichmentModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "contextual_enrichment_model")
}

// FactcheckEnabled reports whether post-generation factchecking should run
// in the default chat path. Defaults to true when the key is unset; explicit
// "false" or "0" disables.
func FactcheckEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "factcheck_in_chat", true)
}

// CitationValidationEnabled reports whether the deterministic per-citation
// validator should run. Phase 2 §B flipped the default from off to on:
// the previous off-by-default rationale (false positives on paraphrase)
// is now mitigated by the semantic-similarity fallback in
// RunCitationValidation. Admins who previously persisted "false" / "0"
// explicitly stay off — only the missing / unset case flips. nil reader
// → on (matches the no-row-set production case).
func CitationValidationEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "citation_validation_enabled", true)
}

// AdaptiveRoutingEnabled reports whether the Phase 2 §C tiered routing
// should fire. When true, PrepareChatContext disables CRAG for queries
// the Phase 1 classifier labeled QueryTypeLookup (and that didn't carry
// an explicit Enhance setting from the caller). Default off — existing
// deployments are bit-for-bit unchanged. Tunable via site_configs key
// "adaptive_routing_enabled".
func AdaptiveRoutingEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "adaptive_routing_enabled", false)
}

// RAGASSamplingEnabled reports whether the production-traffic RAGAS sampling
// task should fire on completed chat responses. When true, runPostResponseTasks
// rolls a per-response dice against RAGASSamplingRate and enqueues an Asynq
// task on a hit. Default off — sampling adds LLM cost (three judge prompts
// per sampled response). Tunable via site_configs key
// "ragas_sampling_enabled".
func RAGASSamplingEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "ragas_sampling_enabled", false)
}

// RAGASSamplingRate returns the per-response sampling probability in [0, 1].
// Default 0.0 (no sampling) so the toggle controls behavior independently
// from the rate — flipping ragas_sampling_enabled off keeps the rate value
// for when it's flipped back on. Out-of-range or unparseable values fall
// back to 0.0. Tunable via site_configs key "ragas_sampling_rate".
func RAGASSamplingRate(ctx context.Context, reader SiteConfigReader) float64 {
	return readFloat(ctx, reader, "ragas_sampling_rate", 0.0, 0.0, 1.0)
}

// CitationValidationSemanticThreshold returns the cosine-similarity floor
// for the semantic-fallback verification tier. The value comes from the
// site_configs key "citation_validation_semantic_threshold" and must lie
// in [0.0, 1.0]. Out-of-range or unparseable values fall back to 0.85,
// the documented default that mirrors the Phase 2 §B plan's tuning
// recommendation.
func CitationValidationSemanticThreshold(ctx context.Context, reader SiteConfigReader) float64 {
	return readFloat(ctx, reader, "citation_validation_semantic_threshold", 0.85, 0.0, 1.0)
}

// ChatAgenticEnabled reports whether the Phase 3 §F agentic chat loop should
// fire on complex_reasoning queries in streaming mode. When true, tryDeepChat
// dispatches to RunAgenticChat (multi-hop with LLM critique gating) instead
// of the existing 2-step RunDeepChat. Default off — agentic adds 1-2 LLM
// judge calls per request and 1-2 extra searches, so operators opt in once
// they're ready for the latency trade-off. Tunable via site_configs key
// "chat_agentic_enabled".
func ChatAgenticEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_agentic_enabled", false)
}

// ChatAgenticMaxHops returns the agentic chat hop budget. Default 3
// (one initial search + up to 2 critique-driven follow-ups). Valid range
// [1, 5]; out-of-range or unparseable values fall back to 3. A value of 1
// makes the loop equivalent to a one-shot search (no follow-ups, judge
// never invoked) — useful for testing the wiring without paying judge
// cost. Tunable via site_configs key "chat_agentic_max_hops".
func ChatAgenticMaxHops(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_agentic_max_hops", 3, 1, 5)
}

// ParentChildEnabled reports whether the Phase 3 §D parent-child chunking
// path is active for new ingestions. When true, the processor splits each
// document into 512-token parents (default) + 128-token children (default);
// children carry parent_chunk_id FKs; the search pipeline returns parent
// content to the LLM. Default off — flipping this on does NOT migrate
// existing chunks (they keep parent_chunk_id NULL and pass through the
// legacy flat path); it only affects ingestions running AFTER the flip.
// Tunable via site_configs key "parent_child_enabled".
func ParentChildEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "parent_child_enabled", false)
}

// ParentChildParentChunkSize returns the parent chunk token budget. Default
// 512. Valid range [128, 4096]; out-of-range or unparseable values fall
// back to 512. Tunable via site_configs key "parent_chunk_size".
func ParentChildParentChunkSize(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "parent_chunk_size", 512, 128, 4096)
}

// ParentChildChildChunkSize returns the child chunk token budget. Default
// 128. Valid range [32, 512]; out-of-range or unparseable values fall back
// to 128. Tunable via site_configs key "child_chunk_size".
func ParentChildChildChunkSize(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "child_chunk_size", 128, 32, 512)
}

// ChatPlanExecuteEnabled reports whether the Phase 1 (combined Plan-and-Execute)
// agentic chat path should fire on streaming complex_reasoning queries. When
// true the dispatcher prefers RunPlanExecuteChat over RunAgenticChat and
// RunDeepChat. Default off — Plan stage adds 1.5–3s; operators opt in
// after eval-gate sign-off. Tunable via site_configs key
// "chat_plan_execute_enabled".
func ChatPlanExecuteEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_plan_execute_enabled", false)
}

// ChatPlanExecuteMaxSubQueries returns the Plan stage's sub-query cap.
// Default 3. Valid range [1, 5]; out-of-range or unparseable falls back to 3.
// Tunable via site_configs key "chat_plan_execute_max_sub_queries".
func ChatPlanExecuteMaxSubQueries(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_plan_execute_max_sub_queries", 3, 1, 5)
}

// ChatPlanExecuteMaxIterations returns the Iterate stage's round cap.
// Default 3. Valid range [1, 5]; out-of-range or unparseable falls back to 3.
// Tunable via site_configs key "chat_plan_execute_max_iterations".
func ChatPlanExecuteMaxIterations(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_plan_execute_max_iterations", 3, 1, 5)
}

// ChatPlanExecuteTokenBudget returns the total LLM-token budget for one
// Plan-and-Execute run (Plan + all Iterate calls). Default 8000. Valid
// range [2000, 32000]. Caller wires this into aibudget.New.
// Tunable via site_configs key "chat_plan_execute_token_budget".
func ChatPlanExecuteTokenBudget(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_plan_execute_token_budget", 8000, 2000, 32000)
}

// ChatPlanExecuteModel returns the LLM model override for Plan and
// Iterate calls. Empty string → caller falls back to the agentic chat
// model (typically the enrichment model). Tunable via site_configs key
// "chat_plan_execute_model".
func ChatPlanExecuteModel(ctx context.Context, reader SiteConfigReader) string {
	return readString(ctx, reader, "chat_plan_execute_model")
}

// ChatFactualityVerifierEnabled reports whether the Phase 3 §3.3
// agent-as-judge factuality verifier is wired into the chat
// post-response tasks. When true, every answer that triggers the
// citation validator's suspect-marker path is also handed to the
// verifier (cost gate); the verifier's flagged claims are stored
// alongside the existing factcheck verdict in the verification JSONB.
// Default off. Tunable via site_configs key
// "chat_factuality_verifier_enabled".
func ChatFactualityVerifierEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_factuality_verifier_enabled", false)
}

// ChatFactualityVerifierAlwaysRun bypasses the cost gate when true:
// the verifier fires on every answer (regardless of whether the
// citation validator raised a suspect marker). For high-stakes
// deployments that want maximum safety at the LLM-cost expense.
// Default false. Tunable via "chat_factuality_verifier_always_run".
func ChatFactualityVerifierAlwaysRun(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_factuality_verifier_always_run", false)
}

// ChatFactualityVerifierModel returns the LLM model override for the
// verifier call. Empty string = caller falls back to the KB default
// chat model. Falls back through the fast-tier chain (per-task →
// `model_tier_fast` → empty). Tunable via
// "chat_factuality_verifier_model".
func ChatFactualityVerifierModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "chat_factuality_verifier_model")
}

// ChatFactualityGateEnabled reports whether the AP-A1 refine gate runs
// when the verifier flags ≥1 unsupported/contradicted claim. When true
// AND the verifier produced refine-eligible flags, the chat post-
// response path runs ONE refine LLM call, replaces the message content
// in the database, and re-runs the verifier on the refined text. Out
// of scope claims do NOT trigger refine — only unsupported and
// contradicted do. Default off. Tunable via "chat_factuality_gate_enabled".
//
// The refine round runs blocking inside the post-response goroutine,
// so enabling this knob extends the post-response wait by one extra
// LLM call (typically 1–3 s). The frontend's existing verification
// SSE payload carries the refined content and the new `refine` block;
// AP-A2 will add streaming diff visualisation on top.
func ChatFactualityGateEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_factuality_gate_enabled", false)
}

// ChatFactualityGateMaxRefines caps the number of refine rounds per
// turn. The plan fixes this at 1 (no infinite-loop risk: the second
// verifier verdict is logged but not acted on). The knob exists so
// operators can disable refine without flipping the master gate
// (set to 0) or, in a future phase, allow a single follow-up refine
// when the second verifier still flags. Default 1; clamped to [0, 2].
// Tunable via "chat_factuality_gate_max_refines".
func ChatFactualityGateMaxRefines(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_factuality_gate_max_refines", 1, 0, 2)
}

// ChatRefineModel returns the LLM model override for the refine call.
// Empty string → caller uses the same model as the original answer
// generation (so the refine text matches the answer's register and
// formatting conventions). Distinct from the verifier's override so
// operators can run a small verifier and a stronger refiner. Tunable
// via "chat_refine_model".
func ChatRefineModel(ctx context.Context, reader SiteConfigReader) string {
	return readString(ctx, reader, "chat_refine_model")
}

// ChatTurnBudgetSeconds is the AP-A3 wall-clock budget per chat
// turn. 0 (default) = unlimited; any positive value caps the turn at
// that many seconds. Range [0, 600]; out-of-range values fall back
// to the default. Tunable via "chat_turn_budget_seconds".
func ChatTurnBudgetSeconds(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_turn_budget_seconds", 0, 0, 600)
}

// ChatTurnBudgetTokens is the AP-A3 cumulative token cap per chat
// turn. 0 (default) = unlimited. The orchestrators deduct AFTER each
// completion using the LLM-reported prompt+completion token count;
// once the counter hits zero, no more LLM calls are issued in this
// turn. Range [0, 2_000_000]; out-of-range falls back to default.
// Tunable via "chat_turn_budget_tokens".
func ChatTurnBudgetTokens(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_turn_budget_tokens", 0, 0, 2_000_000)
}

// ChatPlanExecuteDAGIterative gates the AP-D3 inter-level DAG
// critic. When true AND the plan-execute DAG path is active, an
// LLM-driven critic runs after each topological level and can
// add new sub-query nodes, prune redundant pending nodes, or
// stop early ("answer_now"). Default off — the critic adds one
// LLM call per level which costs a small but real latency
// budget. Operators flip this after eval-gate sign-off shows
// the iterative refinement actually lifts answer quality.
// Tunable via "chat_plan_execute_dag_iterative".
func ChatPlanExecuteDAGIterative(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_plan_execute_dag_iterative", false)
}

// ChatPlanExecuteDAGIterativeModel returns the LLM model override
// for the AP-D3 critic. Falls back through the fast-tier chain
// (per-task → `model_tier_fast` → empty). When still empty, the
// caller falls back to the planning model (chat_plan_execute_model)
// and ultimately to the KB chat model. Operators usually point at
// a small fast model — the critic is classification, not reasoning.
func ChatPlanExecuteDAGIterativeModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "chat_plan_execute_dag_iterative_model")
}

// ChatSelfRAGEnabled gates the AP-D2 unified Self-RAG verifier
// that consolidates ISREL / ISSUP / ISUSE into one LLM call.
// When true AND the citation validator's cost gate fires (a
// suspect citation exists), the chat post-response path runs
// Self-RAG INSTEAD of the legacy factuality verifier — the two
// are mutually exclusive (running both would duplicate the same
// LLM cost). Self-RAG's ISSUP output feeds the existing AP-A1
// refine gate unchanged; its ISUSE output triggers a holistic
// refine even when no claims are flagged. Default off. Tunable
// via "chat_self_rag_enabled".
func ChatSelfRAGEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_self_rag_enabled", false)
}

// ChatSelfRAGModel returns the LLM model override for Self-RAG.
// Falls back through the fast-tier chain (per-task →
// `model_tier_fast` → empty). When still empty the caller uses the
// KB chat model. Same model class as the factuality verifier —
// Gemma/Qwen3 4B handles the structured-output verification well
// enough.
func ChatSelfRAGModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "chat_self_rag_model")
}

// ChatLongmemEnabled gates the AP-D1 long-term per-user memory.
// When true, the chat handler:
//  1. recalls the user's most-relevant durable facts via
//     longmem.Recall and prepends them to the system prompt
//     before each turn;
//  2. fires a fire-and-forget LLM extraction at end-of-turn that
//     writes new salient facts into user_memory.
//
// Default off — privacy-sensitive deployments need the DSGVO UI
// (per-user delete + bulk-clear + export) wired before enabling.
// Tunable via "chat_longmem_enabled".
func ChatLongmemEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_longmem_enabled", false)
}

// ChatLongmemMinSalience filters the AP-D1 extractor's output:
// facts whose self-rated salience is below this threshold drop
// before insertion. Range [0.0, 1.0]; default 0.5. Set lower for
// noisier extraction (more facts land, more drift); set higher
// to keep only identity-strength facts.
func ChatLongmemMinSalience(ctx context.Context, reader SiteConfigReader) float64 {
	return readFloat(ctx, reader, "chat_longmem_min_salience", 0.5, 0.0, 1.0)
}

// ChatLongmemRecallTopK caps how many facts the recall path
// prepends to the system prompt per turn. Range [1, 20]; default
// 5. Higher values inflate every turn's token cost — most useful
// signal is in the top 3-5.
func ChatLongmemRecallTopK(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_longmem_recall_top_k", 5, 1, 20)
}

// ChatLongmemDecayDays controls the half-life of the recall
// score's recency component. Range [1, 365]; default 30. Lower
// values make memories age out of recall faster (good for
// project-tied work where stale facts mislead); higher values
// keep identity facts visible longer.
func ChatLongmemDecayDays(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_longmem_decay_days", 30, 1, 365)
}

// ChatLongmemExtractionModel returns the LLM model override for
// the salient-fact extractor. Falls back through the fast-tier
// chain (per-task → `model_tier_fast` → empty); when still empty
// the caller uses the KB chat model. Operators usually point at a
// small/fast model — the task is structured-output, not
// reasoning, so Gemma/Qwen3-class
// 4B models do well.
func ChatLongmemExtractionModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "chat_longmem_extraction_model")
}

// ChatLongmemRecallSemantic gates the T1-2 ANN-based recall path
// for long-term per-user memory. When true:
//   - At insert time, the chat handler embeds the fact content via
//     ai.EncodeQuery and stores the vector on user_memory.embedding;
//   - At recall time, the chat handler embeds the current user query
//     and calls longmem.RecallSemantic instead of (recency-only)
//     longmem.Recall. The combined score is
//     cosine_sim × salience × exp_decay × log1p_access.
//
// Default off. Flipping on adds one embedding call per fact-extract
// and one per chat turn (both hit the embedding cache after warmup).
// Requires the user_memory.embedding column dim to match the
// deployment's embedder; on dim mismatch Insert falls back to the
// zero-vector placeholder (warns once) and RecallSemantic returns
// an error which the chat layer treats as a fallback signal to v1
// Recall. Tunable via "chat_longmem_recall_semantic".
func ChatLongmemRecallSemantic(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_longmem_recall_semantic", false)
}

// ChatLongmemConflictResolution gates the T1-3 Mem0-style
// memory-conflict classifier. When true (and the recall_semantic
// flag is also on, since this depends on embeddings), each newly
// extracted fact is checked against its semantically nearest
// existing memories before insertion; the classifier picks one of
// {create_new, supersede, skip_redundant}. Supersede flips the
// existing row's superseded_by to the new row's id so the older
// fact stops surfacing in recall. Default off — adds one
// fast-tier LLM call per newly-extracted fact above
// chat_longmem_min_salience. Tunable via
// "chat_longmem_conflict_resolution".
func ChatLongmemConflictResolution(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_longmem_conflict_resolution", false)
}

// ChatLongmemConflictModel returns the LLM model override for the
// T1-3 conflict classifier. Same fast-tier chain as the other
// longmem helpers: per-task → model_tier_fast → empty (caller
// then uses the KB chat model). Tunable via
// "chat_longmem_conflict_model".
func ChatLongmemConflictModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "chat_longmem_conflict_model")
}

// ChatLongmemConflictCandidates caps the size of the candidate
// pool the conflict classifier sees per fact. Range [1, 10];
// default 3. Higher values give the LLM more context but inflate
// per-fact token cost; the recall ANN already filters to the
// nearest matches so 3 is enough in practice.
func ChatLongmemConflictCandidates(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_longmem_conflict_candidates", 3, 1, 10)
}

// ChatLongContextEnabled gates the T2-1 System-2 long-context
// routing. When true AND the query is classified
// complex_reasoning AND the IsGlobalSynthesisQuery keyword
// heuristic fires, SearchOptions.LongContextMode is set: Search
// returns a much wider chunk pool (LongContextTopK ≈ 200 chunks),
// skips MMR / score-drop / parent-child swap, and the chat
// pipeline truncates that pool to ChatLongContextMaxTokens
// before prompt assembly. Default off — flipping it on can
// raise per-turn LLM cost ~30× when the gate fires; validate
// classifier firing rate via `rag_longcontext_route_total`
// before enabling broadly. Tunable via
// "chat_longcontext_enabled".
func ChatLongContextEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_longcontext_enabled", false)
}

// ChatLongContextMaxTokens is the token-budget ceiling the chat
// layer enforces against the long-context chunk pool before
// prompt assembly. Range [10_000, 500_000]; default 100_000.
// 100 k strikes the documented balance: enough room for ~200
// chunks at ~500 tokens each but well under most production
// chat-model context windows (1 M tokens on the latest Gemini /
// Claude tiers, 200 k on Sonnet). Tunable via
// "chat_longcontext_max_tokens".
func ChatLongContextMaxTokens(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_longcontext_max_tokens", 100_000, 10_000, 500_000)
}

// ChatContextCompressionEnabled gates the T2-3 ECoRAG
// evidentiality compression. When true and the post-sandwich
// chunk pool exceeds ChatContextCompressionMinChunks, one
// fast-tier LLM call scores every chunk on whether it provides
// DIRECT evidence to answer the query (orthogonal to relevance,
// which the reranker already captured); chunks below the
// threshold drop out before prompt assembly. Default: off — adds
// one fast-tier LLM call per matching turn; validate on the
// golden set before flipping on. Tunable via
// "chat_context_compression_enabled".
func ChatContextCompressionEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_context_compression_enabled", false)
}

// ChatSufficientContextEnabled gates the Q2 sufficient-context
// abstention gate: one fast-tier LLM call between context assembly
// and generation asking whether the assembled chunk set as a WHOLE
// suffices to answer (Google ICLR 2025 "sufficient context";
// complements per-chunk CRAG grading). On "insufficient" the
// existing abstain plumbing fires. Model resolves via
// ResolveFastTierModel("chat_sufficient_context_model"). Default:
// off — adds one fast-tier LLM call per standard-path turn;
// validate abstain rates on the golden set before flipping on.
// Tunable via "chat_sufficient_context_enabled".
func ChatSufficientContextEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_sufficient_context_enabled", false)
}

// ChatContextCompressionMinChunks is the chunk-count threshold
// below which the compressor skips — small candidate pools rarely
// have drop-able distractors and aren't worth the LLM call. Range
// [1, 50]; default 15. Roughly matches the typical reranker
// top_n_complex_reasoning at which distractor crowding starts
// hurting answer quality. Tunable via
// "chat_context_compression_min_chunks".
func ChatContextCompressionMinChunks(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_context_compression_min_chunks", 15, 1, 50)
}

// ChatContextCompressionThreshold is the evidentiality score
// below which a chunk is dropped (must be < this value). Range
// [0.0, 1.0]; default 0.3. Lower = stricter (more chunks kept);
// higher = aggressive compression (more dropped). 0.3 errs on
// "keep weak evidence" since the LLM's score distribution often
// clusters around 0.4-0.6 even on relevant passages.
// Tunable via "chat_context_compression_threshold".
func ChatContextCompressionThreshold(ctx context.Context, reader SiteConfigReader) float64 {
	return readFloat(ctx, reader, "chat_context_compression_threshold", 0.3, 0.0, 1.0)
}

// ChatContextCompressionModel returns the LLM model override for
// the evidentiality classifier. Falls back through the fast-tier
// chain (per-task → model_tier_fast → empty). The task is
// constrained-output (one JSON object with N floats), not
// reasoning, so a small fast model handles it. Tunable via
// "chat_context_compression_model".
func ChatContextCompressionModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "chat_context_compression_model")
}

// QueryDecomposeEnabled reports whether sub-question decomposition
// (DecomposeRAG) runs inside PrepareChatContext for complex_reasoning
// queries. When true, the LLM produces 2-4 sub-questions whose
// retrieval results fold into the RRF candidate pool via
// SearchOptions.SubQueries. Orthogonal to MultiQuery (paraphrase) and
// the Plan-Execute orchestrator's own decomposition — Plan-Execute
// already handles this at the orchestration level and skips
// PrepareChatContext for its planning step, so the flag fires only
// for the standard fallback path (and any orchestrator that delegates
// to PrepareChatContext for retrieval). Default off — adds 1 fast-tier
// LLM call to the chat critical path; validate on the golden set
// before flipping on. Tunable via "query_decompose_enabled".
func QueryDecomposeEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "query_decompose_enabled", false)
}

// QueryDecomposeModel returns the model used by the decomposition LLM
// call. Resolution chain: per-task key → model_tier_fast → KB chat
// model. Empty string lets ai.GenerateSubQueriesWithModel fall through
// to the resolver default.
func QueryDecomposeModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "query_decompose_model")
}

// ChatGraphRoutingPathMode controls which graph-traversal
// algorithm fills the chat-layer's chunk-injection pool when
// graph routing fires. Values:
//   - "neighbors" (default): depth-1 BFS via kg.Store.LookupSubgraph,
//     the v1 behaviour. Returns chunks tied to the top-1 matched
//     entity's direct neighbours.
//   - "ppr" (T1-4): Personalized PageRank seeded from every matched
//     entity. Returns chunks tied to the top-K structurally-relevant
//     entities the random-walk surfaces. Best for multi-hop reasoning
//     where the relevant entities are 2+ steps away from the seed.
//   - "paths" (T1-5): relational paths between pairs of matched
//     entities. Fires only when ≥2 entities matched; falls back to
//     "neighbors" otherwise. Best for "how do X and Y relate"
//     questions whose answer is in the connecting chunks rather
//     than near either endpoint alone.
//
// Unknown values normalise to "neighbors" so a typo never falls
// off the safe v1 path. Tunable via
// "chat_graph_routing_path_mode".
func ChatGraphRoutingPathMode(ctx context.Context, reader SiteConfigReader) string {
	v := strings.ToLower(strings.TrimSpace(readString(ctx, reader, "chat_graph_routing_path_mode")))
	switch v {
	case "ppr", "paths":
		return v
	default:
		return "neighbors"
	}
}

// ChatGraphRoutingPPRDamping is the PPR damping factor (1 - teleport
// probability). Standard 0.85 keeps random walks long enough to
// surface multi-hop structure without smearing into the whole graph.
// Tunable via "chat_graph_routing_ppr_damping"; clamped to (0, 1).
func ChatGraphRoutingPPRDamping(ctx context.Context, reader SiteConfigReader) float64 {
	v := readFloat(ctx, reader, "chat_graph_routing_ppr_damping", 0.85, 0.01, 0.99)
	return v
}

// ChatGraphRoutingPPRMaxIter caps PPR iterations. 20 is the
// HippoRAG 2 default — graphs typically converge in 5-10 on dense
// regions, slower on sparse ones. Tunable via
// "chat_graph_routing_ppr_max_iter"; clamped to [1, 100].
func ChatGraphRoutingPPRMaxIter(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_graph_routing_ppr_max_iter", 20, 1, 100)
}

// ChatGraphRoutingPPRTopEntities controls how many high-score
// entities PPR's chunk-projection step considers when collecting
// evidence. 10 is enough to capture the strong neighbourhood
// without diluting the chunk pool. Tunable via
// "chat_graph_routing_ppr_top_entities"; clamped to [1, 50].
func ChatGraphRoutingPPRTopEntities(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_graph_routing_ppr_top_entities", 10, 1, 50)
}

// ChatGraphRoutingPathsMaxLen caps the path length the PathRAG
// enumerator walks. 3 is PathRAG's sweet spot — multi-hop
// reasoning rarely benefits past 3 hops on KG-scale graphs and
// the path count grows combinatorially. Tunable via
// "chat_graph_routing_paths_max_len"; clamped to [1, 6].
func ChatGraphRoutingPathsMaxLen(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_graph_routing_paths_max_len", 3, 1, 6)
}

// ChatGraphRoutingPathsMaxPaths caps the number of paths the
// enumerator returns. 5 surfaces alternative routes without
// swamping the chunk pool. Tunable via
// "chat_graph_routing_paths_max_paths"; clamped to [1, 50].
func ChatGraphRoutingPathsMaxPaths(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_graph_routing_paths_max_paths", 5, 1, 50)
}

// ChatGraphRoutingEnabled reports whether the AP-C4 graph-routing
// heuristic runs as a chat pre-step. When true AND the query is
// classified `complex_reasoning` AND the query mentions an entity
// the KB's knowledge graph knows about, the chat handler dispatches
// `graph_search` and folds the resulting subgraph chunks into the
// retrieval pool. Default off — without AP-C1 KG extraction having
// run, the heuristic always reports `skipped_no_entity` (or
// `db_error` if the kg.Store wiring is missing) so the cost is
// negligible, but operators should flip this deliberately to keep
// the eval-gate honest. Tunable via "chat_graph_routing_enabled".
func ChatGraphRoutingEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_graph_routing_enabled", false)
}

// ChatGraphRoutingInjectChunks reports whether the AP-C4 graph
// router should fold the matched entity's subgraph chunks into the
// retrieval pool (RRF extraLists), in addition to emitting the
// existing diagnostic trajectory event. Default off — flipping
// `chat_graph_routing_enabled` alone preserves the diagnostic-only
// semantics existing deployments rely on; this flag is the explicit
// opt-in for the chunk-injection behaviour. Tunable via
// "chat_graph_routing_inject_chunks". No-op when
// `chat_graph_routing_enabled` is false.
func ChatGraphRoutingInjectChunks(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_graph_routing_inject_chunks", false)
}

// ChatGraphRoutingMaxChunks caps the number of subgraph chunks
// folded into the retrieval pool when chunk injection is on.
// Subgraph at depth=1 typically yields 5-20 chunks per top entity;
// 15 stays under the typical RRF candidate pool size and avoids
// swamping the BM25 / vector signal. Clamped to [1, 50]. Tunable
// via "chat_graph_routing_max_chunks".
func ChatGraphRoutingMaxChunks(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_graph_routing_max_chunks", 15, 1, 50)
}

// ChatPlanExecuteToolAware reports whether the AP-B3 tool-aware
// planner runs in place of the legacy DAG planner. When true AND
// the plan-execute orchestrator picks the DAG path, the planner
// LLM call uses the tool-catalog system prompt and the executor
// dispatches each node through the MCP registry's ToolDispatcher
// (built-in + remote tools). Default off — existing deployments
// keep their kb_search-only behaviour until the operator opts in.
// Tunable via "chat_plan_execute_tool_aware".
func ChatPlanExecuteToolAware(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_plan_execute_tool_aware", false)
}

// ChatCodeExecEnabled gates the AP-B2 code_exec MCP tool. Default
// false. Tunable via "chat_code_exec_enabled". The site_config gate
// is evaluated at every tool invocation (not at registry
// construction) so an admin can flip it at runtime without restart.
// Operators MUST verify the gVisor runtime + container image are
// configured before flipping this — the tool returns "disabled"
// errors on each call until then, but enabling it without the
// runtime in place will surface a docker-run failure to the user.
func ChatCodeExecEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_code_exec_enabled", false)
}

// ChatAnswerHistoryEnabled gates passing recent conversation turns to the
// answer LLM (all paths: standard stream/JSON, orchestrators, answer-tools).
// Default TRUE — this is a correctness fix, not an opt-in feature: without
// it the answer model is single-turn and cannot resolve follow-ups that
// reference the previous answer. Kill switch via
// "chat_answer_history_enabled".
func ChatAnswerHistoryEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_answer_history_enabled", true)
}

// ChatAnswerHistoryMessages is how many recent messages are passed to the
// answer LLM. Default 6 (3 turns), clamped to [1, 50]. Tunable via
// "chat_answer_history_messages".
func ChatAnswerHistoryMessages(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_answer_history_messages", 6, 1, 50)
}

// ChatAnswerHistoryMaxChars caps each history message passed to the answer
// LLM (rune count). Default 4000, clamped to [200, 32000]. Tunable via
// "chat_answer_history_max_chars".
func ChatAnswerHistoryMaxChars(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_answer_history_max_chars", 4000, 200, 32000)
}

// ChatTransformFollowupEnabled gates the transform-follow-up route: messages
// like "kannst du das als Tabelle erstellen?" skip retrieval and reformat
// the previous answer instead of re-searching the corpus. Default TRUE
// (correctness fix — see IsTransformFollowUpQuery). Kill switch via
// "chat_transform_followup_enabled".
func ChatTransformFollowupEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_transform_followup_enabled", true)
}

// ChatTabularQueryEnabled gates the tabular-data Q&A feature. Default
// false. Tunable via "chat_tabular_query_enabled".
func ChatTabularQueryEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_tabular_query_enabled", false)
}

// ChatTabularChartsEnabled gates the tabular-charts display feature. Default
// false. Tunable via "chat_tabular_charts_enabled".
func ChatTabularChartsEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_tabular_charts_enabled", false)
}

// ChatKBRouterEnabled reports whether the AP-A4 sub-KB router runs
// when the chat request signals "auto" (via `?route=auto` query
// param). Default off — existing single-KB requests are unaffected.
// Tunable via "chat_kb_router_enabled".
func ChatKBRouterEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_kb_router_enabled", false)
}

// ChatKBRouterMinConfidence is the cosine-style 0..1 threshold above
// which the router's top-1 pick is accepted. When the top-1 score is
// below this, the outcome is `fallback_all` and the router does NOT
// override the user's URL kb_id. Range [0, 1]; default 0.6. Tunable
// via "chat_kb_router_min_confidence".
func ChatKBRouterMinConfidence(ctx context.Context, reader SiteConfigReader) float64 {
	return readFloat(ctx, reader, "chat_kb_router_min_confidence", 0.6, 0.0, 1.0)
}

// ChatKBRouterModel returns the LLM model override for the router
// classifier. Falls back through the fast-tier chain (per-task →
// `model_tier_fast` → empty); when still empty the caller uses
// the KB default chat model. Tunable via "chat_kb_router_model".
func ChatKBRouterModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "chat_kb_router_model")
}

// ChatTurnBudgetToolCalls is the AP-A3 tool-call cap per chat turn.
// 0 (default) = unlimited. Each Registry.Dispatch invocation counts
// as one tool call. Range [0, 100]; out-of-range falls back to default.
// Tunable via "chat_turn_budget_tool_calls".
func ChatTurnBudgetToolCalls(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_turn_budget_tool_calls", 0, 0, 100)
}

// ChatSupervisorEnabled reports whether the Phase 3 §3.2 supervisor
// + specialist agents path should fire on streaming complex_reasoning
// queries. When true the dispatcher prefers RunSupervisorChat over
// RunPlanExecuteChat / RunAgenticChat / RunDeepChat. Default off.
// Tunable via site_configs key "chat_supervisor_enabled".
func ChatSupervisorEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_supervisor_enabled", false)
}

// ChatSupervisorMultiSpecialist reports whether the supervisor should
// dispatch multiple specialists in parallel (RunMulti) instead of the
// single-specialist Run. When true, enumeration-intent queries fan out
// to the retriever AND enumerator concurrently and merge their results
// via RRF — improving compound queries that have both a list aspect and
// a reasoning aspect. No-op unless chat_supervisor_enabled is also on.
// Default off. Tunable via site_configs key
// "chat_supervisor_multi_specialist".
func ChatSupervisorMultiSpecialist(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_supervisor_multi_specialist", false)
}

// ChatPlanExecuteDAG reports whether the Phase 3 §3.1 DAG-shaped plan
// stage is active. When true, plan-execute calls ai.PlanQueriesDAG
// (which returns a Plan{Nodes,Rationale} with explicit dependency
// edges) and dispatches through chat.ExecuteDAG instead of the legacy
// flat-list searcher.SubQueries path. Default off — the flat-list
// planner is the production baseline; operators flip the DAG planner
// after eval-gate sign-off. Tunable via site_configs key
// "chat_plan_execute_dag".
func ChatPlanExecuteDAG(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_plan_execute_dag", false)
}

// ChatPlanExecuteMaxDAGDepth returns the longest-chain cap on DAG
// plans. Default 3. Valid range [1, 5]; out-of-range or unparseable
// falls back to 3. The cap is a circuit breaker — a planner that
// emits depth-7 chains is almost always misbehaving and the recall
// payoff for very long chains is bounded.
// Tunable via site_configs key "chat_plan_execute_max_dag_depth".
func ChatPlanExecuteMaxDAGDepth(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_plan_execute_max_dag_depth", 3, 1, 5)
}

// ChatPlanExecuteMaxDAGNodes returns the hard cap on DAG plan node
// count. Default 6. Valid range [1, 12]; out-of-range falls back.
// Tunable via site_configs key "chat_plan_execute_max_dag_nodes".
func ChatPlanExecuteMaxDAGNodes(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_plan_execute_max_dag_nodes", 6, 1, 12)
}

// ChatSessionMemoryEnabled reports whether the Phase 2 §2.3 chat-session
// memory feature is active. When true and the handler was wired with a
// SessionMemory store, every chat turn (a) loads the session's notes
// and prepends them to the orchestrator's system prompt as a
// `Conversation memory:` block, and (b) lets the LLM call memory_write
// via the MCP registry to persist new durable facts. Default off.
// Tunable via site_configs key "chat_session_memory_enabled".
func ChatSessionMemoryEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_session_memory_enabled", false)
}

// ChatUseMCPTools reports whether the Phase 2 §2.1 MCP tool dispatch path
// is active for the agentic / plan-execute orchestrators. Default off —
// the legacy direct-search path stays as the production default until
// the MCP refactor matches it on `cmd/eval --trajectory`. Tunable via
// site_configs key "chat_use_mcp_tools".
//
// When false, the orchestrator dispatches to vector.SearchService.Search
// directly (the pre-MCP shape) and any registered MCP tools are unused.
// When true, the orchestrator routes the iterate stage's "search" action
// through mcp.Registry.Dispatch(kbID, "kb_search", ...) — observable as
// `agentToolCall` trajectory events and the `rag_mcp_tool_call_total`
// counter.
func ChatUseMCPTools(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_use_mcp_tools", false)
}

// ChatAnswerToolsEnabled reports whether the answer-generation LLM may
// call MCP tools mid-turn via native OpenAI function calling. Default
// false. See docs/superpowers/specs/2026-05-13-llm-answer-tool-calling-design.md.
func ChatAnswerToolsEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_answer_tools_enabled", false)
}

// ChatAnswerToolsMaxRounds caps how many tool-call rounds the answer loop
// will run before forcing a no-tool finish. Default 5; valid range [1, 10].
// Out-of-range or unparseable values fall back to the default.
func ChatAnswerToolsMaxRounds(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_answer_tools_max_rounds", 5, 1, 10)
}

// ChatAgenticPlateauStop reports whether the Phase 1 §1.3 quality-plateau
// early-stop check is active. When true, the agentic / plan-execute loops
// track per-step `topScore` and `chunksAdded` deltas; two consecutive
// low-yield rounds (≤ chunks_threshold new chunks AND score delta below
// score_delta) trigger a graceful break with outcome `early_stop_plateau`.
// Default off — the existing zero-progress break (`added==0`) already
// catches the worst case. The plateau check is the looser superset.
// Tunable via site_configs key "chat_agentic_plateau_stop".
func ChatAgenticPlateauStop(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_agentic_plateau_stop", false)
}

// ChatAgenticPlateauChunksThreshold returns the per-round chunk-add ceiling
// below which a hop counts as plateaued. Default 1 ("≤1 new chunk added").
// Valid range [0, 5]; out-of-range or unparseable falls back to 1. A value
// of 0 reduces the plateau check to the existing zero-progress break.
// Tunable via site_configs key "chat_agentic_plateau_chunks_threshold".
func ChatAgenticPlateauChunksThreshold(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "chat_agentic_plateau_chunks_threshold", 1, 0, 5)
}

// ChatAgenticPlateauScoreDelta returns the per-round top-score improvement
// floor; if the highest-scored chunk improves by less than this between
// rounds the round counts as plateaued. Default 0.02 ("2 % of the [0,1]
// score range"). Valid range [0.0, 1.0]; out-of-range or unparseable falls
// back to 0.02. Tunable via site_configs key
// "chat_agentic_plateau_score_delta".
func ChatAgenticPlateauScoreDelta(ctx context.Context, reader SiteConfigReader) float64 {
	return readFloat(ctx, reader, "chat_agentic_plateau_score_delta", 0.02, 0.0, 1.0)
}

// PlateauConfig is the resolved plateau-stop knob set passed into the
// agentic / plan-execute loops. The threshold semantics are intentionally
// "≤ Chunks AND <ScoreDelta" — both must be unsatisfied for a round to
// reset the streak. PlateauStreakLimit is hard-coded to 2 so a single
// flat round doesn't terminate the loop; only two-in-a-row does.
type PlateauConfig struct {
	Enabled    bool
	Chunks     int
	ScoreDelta float64
}

// PlateauStreakLimit is the number of consecutive plateau rounds required
// to trigger early-stop. Two is intentional: one flat round can be a
// reranker hiccup or a near-duplicate seed; two flat rounds in a row is
// real signal that the loop has run out of new evidence.
const PlateauStreakLimit = 2

// ResolvePlateauConfig batch-loads the three plateau-stop site_configs into
// a PlateauConfig. Caller is the orchestrator; called once per chat run so
// changes propagate without serializing through the per-key cache.
func ResolvePlateauConfig(ctx context.Context, reader SiteConfigReader) PlateauConfig {
	return PlateauConfig{
		Enabled:    ChatAgenticPlateauStop(ctx, reader),
		Chunks:     ChatAgenticPlateauChunksThreshold(ctx, reader),
		ScoreDelta: ChatAgenticPlateauScoreDelta(ctx, reader),
	}
}

// MultipassExtractionEnabled gates the G5 per-chunk extraction pre-pass.
// When true AND the retrieved chunk count meets MultipassMinChunks AND the
// turn isn't abstaining, PrepareChatContext fans out one fast-tier LLM call
// per chunk to extract only the query-relevant sentences before assembling
// the answer prompt. Chunks where extraction returns "NONE" are dropped
// from the context.
//
// Trade-off: N parallel fast-tier LLM calls add roughly 0.5-2.0s to the
// turn (concurrency-limited) but mitigate "lost in the middle" position
// bias on long contexts by converting the linear chunk list into N
// independent extractions + 1 synthesis. Default off; admins enable per
// KB. Tunable via "multipass_extraction_enabled".
func MultipassExtractionEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "multipass_extraction_enabled", false)
}

// MultipassMinChunks is the chunk-count floor that activates the
// extraction pre-pass. Below this threshold the per-chunk LLM cost is not
// justified — the linear context is short enough that the answer model
// can still attend to every chunk. Default 12; clamped to [3, 100].
// Tunable via "multipass_min_chunks".
func MultipassMinChunks(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "multipass_min_chunks", 12, 3, 100)
}

// MultipassExtractionModel returns the LLM model override for the per-chunk
// extraction call. Falls back through the fast-tier resolution chain
// (per-task -> "model_tier_fast" -> empty); when still empty the caller
// uses the KB chat model. Extraction is short, structured ("output only
// the facts directly relevant to the query"), so a small fast model does
// well. Tunable via "multipass_extraction_model".
func MultipassExtractionModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "multipass_extraction_model")
}

// RaptorEnabled gates the Phase F per-file RAPTOR summary-tree build
// at ingest. When true AND ParentChildEnabled is false AND the file's
// leaf count is >= RaptorMinChunks, the processor recursively
// clusters leaves with K-means, summarises each cluster with the
// fast-tier LLM, embeds the summary as a regular chunk row (tagged
// node_kind='summary', tree_level=N), and back-patches
// raptor_parent_id on the children. Leaves and summaries compete in
// the unchanged BM25 + vector + reranker pipeline ("collapsed-tree
// retrieval"). Zero query-time LLM cost. Default off.
//
// Mutually exclusive with parent_child_enabled — the processor logs
// + skips RAPTOR when both flags are on. Tunable via site_configs
// key "raptor_enabled".
func RaptorEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "raptor_enabled", false)
}

// RaptorMinChunks is the leaf-count floor below which the builder
// skips the file. Cluster math (k-means / k-means++) degenerates at
// small N: with branching=5 a file with <10 chunks would produce
// 1-2 clusters and a flat tree that adds no synthesis signal.
// Default 25; clamped to [5, 200]. Tunable via "raptor_min_chunks".
func RaptorMinChunks(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "raptor_min_chunks", 25, 5, 200)
}

// RaptorMaxLevels caps the tree depth. Most files reach a single
// root long before this — at branching=5, a 1000-chunk file needs
// log_5(1000)≈4.3 levels. The cap is a circuit breaker against a
// pathological branching configuration or a re-ingest loop that
// hasn't filtered summaries out. Default 4; clamped to [2, 8].
// Tunable via "raptor_max_levels".
func RaptorMaxLevels(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "raptor_max_levels", 4, 2, 8)
}

// RaptorBranchingFactor is the target cluster size at each level:
// k = ceil(current_node_count / branching). Higher values produce
// wider, shallower trees (fewer levels, more chunks per level);
// lower values produce taller trees. The RAPTOR paper uses 5;
// preserve that default. Clamped to [2, 20]. Tunable via
// "raptor_branching_factor".
func RaptorBranchingFactor(ctx context.Context, reader SiteConfigReader) int {
	return readInt(ctx, reader, "raptor_branching_factor", 5, 2, 20)
}

// RaptorSummaryModel returns the LLM model override for the
// per-cluster summary call. Falls back through the fast-tier
// resolution chain (per-task → "model_tier_fast" → empty). When
// still empty the caller uses the KB chat model. Summary is
// constrained-output (≤4 sentences, preserve entities/dates) — a
// small fast model does well. Tunable via "raptor_summary_model".
func RaptorSummaryModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "raptor_summary_model")
}

// RaptorClusteringAlgorithm selects the per-level clustering pass
// for the RAPTOR builder. Values:
//   - "" or "kmeans" (default): fixed-k Lloyd's algorithm with
//     k = ceil(N / branching_factor). The v1 behaviour.
//   - "leiden" (T2-2): modularity-based community detection over
//     a k-NN cosine-similarity graph. Cluster count is determined
//     by graph topology — heterogeneous documents yield more,
//     tighter clusters; homogeneous ones yield fewer, larger.
//     branching_factor is ignored on this path.
//
// Unknown values normalise to "kmeans" so a typo never falls off
// the v1 safe path. Tunable via "raptor_clustering_algorithm".
func RaptorClusteringAlgorithm(ctx context.Context, reader SiteConfigReader) string {
	v := strings.ToLower(strings.TrimSpace(readString(ctx, reader, "raptor_clustering_algorithm")))
	if v == "leiden" {
		return "leiden"
	}
	return "kmeans"
}

// RaptorLeidenResolution is γ for the modularity formula in
// ClusterByLeiden. γ > 1 produces more, smaller communities;
// γ < 1 produces fewer, larger ones. Default 1.0 matches the
// classical Newman-Girvan modularity and the 20 % accuracy lift
// reported in the 2025 Frontiers adaptive-RAPTOR paper. Only
// consulted when RaptorClusteringAlgorithm == "leiden". Range
// (0, 10]. Tunable via "raptor_leiden_resolution".
func RaptorLeidenResolution(ctx context.Context, reader SiteConfigReader) float64 {
	return readFloat(ctx, reader, "raptor_leiden_resolution", 1.0, 0.01, 10.0)
}

// ChatFeedbackBoostEnabled gates the retrieval-time feedback boost. Default off.
// When true, retrieval scoring applies a tanh-shaped score adjustment derived
// from accumulated thumbs-up/thumbs-down signals stored for each chunk. The
// maximum adjustment magnitude is bounded by FeedbackBoostWeight so feedback
// can nudge but never dominate relevance. Tunable via
// "chat_feedback_boost_enabled".
func ChatFeedbackBoostEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "chat_feedback_boost_enabled", false)
}

// FeedbackBoostWeight is the maximum absolute score adjustment applied from
// feedback signals (the bounded tanh is scaled by this). Default 0.05,
// clamped to [0, 0.5] so feedback can nudge but never dominate relevance.
// Tunable via "feedback_boost_weight".
func FeedbackBoostWeight(ctx context.Context, reader SiteConfigReader) float64 {
	return readFloat(ctx, reader, "feedback_boost_weight", 0.05, 0.0, 0.5)
}

// HyPEEnabled gates ingest-time generation of hypothetical questions
// per chunk (the HyPE index build). Default false. Orthogonal to
// HyPESearchEnabled — build the index first, then turn on retrieval.
// Tunable via "hype_enabled".
func HyPEEnabled(ctx context.Context, r SiteConfigReader) bool {
	return readBool(ctx, r, "hype_enabled", false)
}

// HyPESearchEnabled gates the query-time HyPE search arm. Default
// false. A no-op (fails open to empty) when the HyPE table is missing
// or unpopulated. Tunable via "hype_search_enabled".
func HyPESearchEnabled(ctx context.Context, r SiteConfigReader) bool {
	return readBool(ctx, r, "hype_search_enabled", false)
}

// HyPEQuestionsPerChunk caps how many hypothetical questions are
// generated per chunk at ingest. Default 3, range [1,20].
// Tunable via "hype_questions_per_chunk".
func HyPEQuestionsPerChunk(ctx context.Context, r SiteConfigReader) int {
	return readInt(ctx, r, "hype_questions_per_chunk", 3, 1, 20)
}

// DescribeImageEnabled gates the POST /api/describe-image endpoint. Default off.
// Tunable via "describe_image_enabled".
func DescribeImageEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return readBool(ctx, reader, "describe_image_enabled", false)
}

// DescribeImageModel resolves the vision model for image description:
// describe_image_model → model_tier_fast → "" (caller errors when empty,
// since the KB chat model is not assumed vision-capable).
func DescribeImageModel(ctx context.Context, reader SiteConfigReader) string {
	return ResolveFastTierModel(ctx, reader, "describe_image_model")
}

// ChatCorpusTableEnabled reports whether the corpus-comparison-table feature
// is active. When true, the chat handler detects "compare/list all X across
// these documents" queries and answers with a map-reduce structured table
// (one fast-tier LLM call per in-scope file). Default off. Tunable via
// "chat_corpus_table_enabled".
func ChatCorpusTableEnabled(ctx context.Context, r SiteConfigReader) bool {
	return readBool(ctx, r, "chat_corpus_table_enabled", false)
}

// ChatCorpusTableMaxFiles is the hard cap on the number of files processed
// per corpus-table query. Excess files are dropped and the table is flagged
// truncated. Default 50; clamped to [1, 500]. Tunable via
// "chat_corpus_table_max_files".
func ChatCorpusTableMaxFiles(ctx context.Context, r SiteConfigReader) int {
	return readInt(ctx, r, "chat_corpus_table_max_files", 50, 1, 500)
}

// ChatCorpusTableConcurrency is the maximum number of parallel per-file
// extraction calls. Default 6; clamped to [1, 50]. Tunable via
// "chat_corpus_table_concurrency".
func ChatCorpusTableConcurrency(ctx context.Context, r SiteConfigReader) int {
	return readInt(ctx, r, "chat_corpus_table_concurrency", 6, 1, 50)
}

// ChatCorpusTableModel returns the LLM model override for column planning
// and per-file extraction. Resolution chain: per-task override →
// model_tier_fast → "" (caller falls back to the KB chat model). Tunable
// via "chat_corpus_table_model".
func ChatCorpusTableModel(ctx context.Context, r SiteConfigReader) string {
	return ResolveFastTierModel(ctx, r, "chat_corpus_table_model")
}

// ChatCorpusTableRouterLLMEnabled reports whether, after the keyword router
// fires, a fast-tier yes/no LLM confirmation call runs before the expensive
// map-reduce. Default on — turn off only to audit raw keyword-router firing
// rate. Tunable via "chat_corpus_table_router_llm_enabled".
func ChatCorpusTableRouterLLMEnabled(ctx context.Context, r SiteConfigReader) bool {
	return readBool(ctx, r, "chat_corpus_table_router_llm_enabled", true)
}

// ---------------------------------------------------------------------------
// In-chat document comparison (chat_compare_*)
// ---------------------------------------------------------------------------

// CompareEnabled gates the in-chat document comparison feature. Default off.
// Tunable via "chat_compare_enabled".
func CompareEnabled(ctx context.Context, r SiteConfigReader) bool {
	return readBool(ctx, r, "chat_compare_enabled", false)
}

// CompareModel returns the LLM model for the comparison section-synthesis
// calls. Falls back through the fast-tier chain (per-task → model_tier_fast →
// empty); when still empty the caller uses the KB default chat model. Tunable
// via "chat_compare_model".
func CompareModel(ctx context.Context, r SiteConfigReader) string {
	return ResolveFastTierModel(ctx, r, "chat_compare_model")
}

// CompareMaxSections caps how many sections the comparison decomposes a
// document into. Default 60; clamped to [1, 500]. Tunable via
// "chat_compare_max_sections".
func CompareMaxSections(ctx context.Context, r SiteConfigReader) int {
	return readInt(ctx, r, "chat_compare_max_sections", 60, 1, 500)
}

// CompareConcurrency caps the number of concurrent per-section LLM calls
// during comparison. Default 6; clamped to [1, 32]. Tunable via
// "chat_compare_concurrency".
func CompareConcurrency(ctx context.Context, r SiteConfigReader) int {
	return readInt(ctx, r, "chat_compare_concurrency", 6, 1, 32)
}

// ComparePeersPerSection caps how many peer chunks each section is compared
// against. Default 5; clamped to [1, 20]. Tunable via
// "chat_compare_peers_per_section".
func ComparePeersPerSection(ctx context.Context, r SiteConfigReader) int {
	return readInt(ctx, r, "chat_compare_peers_per_section", 5, 1, 20)
}

// CompareAttachmentTTLHours is the lifetime of a compare attachment before
// it is eligible for cleanup. Default 24 hours; clamped to [1, 720]. Tunable
// via "chat_compare_attachment_ttl_hours".
func CompareAttachmentTTLHours(ctx context.Context, r SiteConfigReader) int {
	return readInt(ctx, r, "chat_compare_attachment_ttl_hours", 24, 1, 720)
}

// CompareMaxFileBytes caps the size of a comparison attachment file. Default
// 10485760 (10 MiB); clamped to [1024, 104857600]. Tunable via
// "chat_compare_max_file_bytes".
func CompareMaxFileBytes(ctx context.Context, r SiteConfigReader) int {
	return readInt(ctx, r, "chat_compare_max_file_bytes", 10485760, 1024, 104857600)
}
