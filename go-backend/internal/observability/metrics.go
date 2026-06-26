// Package observability defines RAG-specific Prometheus instruments and the
// thin Record* helpers pipeline code calls. Cardinality discipline: only
// low-cardinality labels (stage, language, verified, stream). High-cardinality
// data (kb_id, user_id, request_id, query) lives in structured logs via
// internal/logctx, not here.
package observability

import (
	"context"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var commonLabels = prometheus.Labels{"app": "justrag"}

// goroutineCount reports the live runtime.NumGoroutine() at scrape time.
// Polled (not instrumented per-spawn) so it adds nothing to the safego.Go
// hot path; the value matters because runaway spawns (multi-query storms,
// embedding retry loops) otherwise accumulate silently — this gauge surfaces
// them on the existing Prometheus dashboard.
var goroutineCount = promauto.NewGaugeFunc(
	prometheus.GaugeOpts{
		Name:        "go_goroutines_active",
		Help:        "Live goroutine count sampled at scrape time.",
		ConstLabels: commonLabels,
	},
	func() float64 { return float64(runtime.NumGoroutine()) },
)
var _ = goroutineCount // gauge is registered for its side effect; the var keeps the linter quiet

// --- Search -----------------------------------------------------------------

var (
	searchTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_search_total",
			Help:        "Total RAG search calls, labeled by KB language.",
			ConstLabels: commonLabels,
		},
		[]string{"language"},
	)

	searchDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_search_duration_seconds",
			Help:        "End-to-end RAG search latency in seconds.",
			Buckets:     []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5},
			ConstLabels: commonLabels,
		},
	)

	searchChunksReturned = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_search_chunks_returned",
			Help:        "Number of chunks returned by a RAG search call.",
			Buckets:     []float64{0, 1, 3, 5, 10, 15, 25, 50},
			ConstLabels: commonLabels,
		},
	)

	searchTopScore = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_search_top_score",
			Help:        "Top vector score of the highest-ranked chunk per RAG search call.",
			Buckets:     []float64{0.0, 0.2, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
			ConstLabels: commonLabels,
		},
	)
)

// RecordSearch records one completed RAG search.
// An empty language is normalized to "unknown" to keep the label space bounded.
func RecordSearch(language string, chunksReturned int, topScore float64, durationSeconds float64) {
	if language == "" {
		language = "unknown"
	}
	searchTotal.WithLabelValues(language).Inc()
	searchDuration.Observe(durationSeconds)
	searchChunksReturned.Observe(float64(chunksReturned))
	searchTopScore.Observe(topScore)
}

// --- Factcheck --------------------------------------------------------------

var (
	factcheckTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_factcheck_total",
			Help:        "Total factcheck runs, labeled by outcome.",
			ConstLabels: commonLabels,
		},
		[]string{"verified"},
	)

	factcheckScore = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_factcheck_score",
			Help:        "Factcheck verification score (0..100).",
			Buckets:     []float64{0, 25, 50, 75, 90, 100},
			ConstLabels: commonLabels,
		},
	)

	factcheckDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_factcheck_duration_seconds",
			Help:        "Factcheck call latency in seconds.",
			Buckets:     []float64{0.1, 0.25, 0.5, 1, 2, 5},
			ConstLabels: commonLabels,
		},
	)
)

// RecordFactcheck records one completed factcheck run.
func RecordFactcheck(verified bool, score float64, durationSeconds float64) {
	factcheckTotal.WithLabelValues(strconv.FormatBool(verified)).Inc()
	factcheckScore.Observe(score)
	factcheckDuration.Observe(durationSeconds)
}

// --- Completion -------------------------------------------------------------

var completionDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:        "rag_completion_duration_seconds",
		Help:        "LLM completion latency in seconds for the chat path.",
		Buckets:     []float64{0.5, 1, 2, 5, 10, 20, 60},
		ConstLabels: commonLabels,
	},
	[]string{"stream"},
)

// RecordCompletion records one completed LLM call (streaming or not).
func RecordCompletion(stream bool, durationSeconds float64) {
	completionDuration.WithLabelValues(strconv.FormatBool(stream)).Observe(durationSeconds)
}

// --- Low-confidence answers + cache hit/miss --------------------------------

var (
	lowConfidenceTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name:        "rag_low_confidence_answers_total",
			Help:        "Total chat answers served with fewer than 3 source chunks.",
			ConstLabels: commonLabels,
		},
	)

	embeddingCacheHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Name:        "rag_embedding_cache_hits_total",
			Help:        "Total embedding cache hits.",
			ConstLabels: commonLabels,
		},
	)

	embeddingCacheMisses = promauto.NewCounter(
		prometheus.CounterOpts{
			Name:        "rag_embedding_cache_misses_total",
			Help:        "Total embedding cache misses.",
			ConstLabels: commonLabels,
		},
	)
)

// RecordLowConfidence increments the low-confidence-answer counter.
func RecordLowConfidence() {
	lowConfidenceTotal.Inc()
}

// RecordEmbeddingCacheHit increments the cache-hit counter.
func RecordEmbeddingCacheHit() {
	embeddingCacheHits.Inc()
}

// RecordEmbeddingCacheMiss increments the cache-miss counter.
func RecordEmbeddingCacheMiss() {
	embeddingCacheMisses.Inc()
}

// --- CRAG (Corrective RAG) --------------------------------------------------

var cragDecisionTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:        "rag_crag_decision_total",
		Help:        "Total CRAG decisions taken after relevance grading, labeled by branch.",
		ConstLabels: commonLabels,
	},
	[]string{"action"},
)

// CRAG action labels — kept as constants so callers can't introduce label
// typos that would balloon Prometheus cardinality on the action axis.
const (
	CRAGActionProceed = "proceed"
	CRAGActionRewrite = "rewrite"
	CRAGActionAbstain = "abstain"
)

// --- In-app eval runner -----------------------------------------------------

var (
	EvalRunsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_eval_runs_total",
			Help:        "Total in-app eval runs, labeled by outcome status and KB id.",
			ConstLabels: commonLabels,
		},
		[]string{"status", "kb"},
	)

	EvalRunDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "rag_eval_run_duration_seconds",
			Help:        "Wall-time duration of in-app eval runs.",
			Buckets:     prometheus.ExponentialBuckets(60, 2, 8),
			ConstLabels: commonLabels,
		},
		[]string{"judge", "status"},
	)

	EvalRunActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name:        "rag_eval_run_active",
			Help:        "1 when an in-app eval run is in progress, 0 otherwise.",
			ConstLabels: commonLabels,
		},
	)
)

// RecordCRAGDecision increments the CRAG decision counter for one branch
// taken (proceed, rewrite, abstain). Unknown labels are coerced to "proceed"
// to keep the cardinality on the action axis bounded; a warning is logged
// so misuse is visible instead of silently skewing the proceed bucket.
func RecordCRAGDecision(ctx context.Context, action string) {
	switch action {
	case CRAGActionRewrite, CRAGActionAbstain, CRAGActionProceed:
		cragDecisionTotal.WithLabelValues(action).Inc()
	default:
		logctx.From(ctx).Warn("observability.crag.unknown_action",
			"action", action,
			"coerced_to", CRAGActionProceed)
		cragDecisionTotal.WithLabelValues(CRAGActionProceed).Inc()
	}
}

// --- Chat feedback ----------------------------------------------------------

var (
	chatFeedbackTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "chat_feedback_submitted_total",
			Help:        "Total user-submitted feedback events on AI messages, labeled by rating (positive/negative/cleared).",
			ConstLabels: commonLabels,
		},
		[]string{"rating"},
	)
)

// RecordChatFeedback records one user feedback submission. rating is one of
// "positive", "negative", "cleared". Higher-cardinality dimensions (kb_id,
// user_id, message_id) belong in the structured log, not as label values.
func RecordChatFeedback(rating string) {
	chatFeedbackTotal.WithLabelValues(rating).Inc()
}

// --- Cascade deletion ------------------------------------------------------

var cascadeDeletionErrorTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:        "rag_cascade_deletion_error_total",
		Help:        "Best-effort cascade-delete failures, labeled by orphaned resource. The DB row is removed regardless, so a non-zero rate signals orphaned vector chunks or storage objects.",
		ConstLabels: commonLabels,
	},
	[]string{"resource"},
)

// Cascade resource labels — kept as constants to bound Prometheus cardinality.
const (
	CascadeResourceVector  = "vector"
	CascadeResourceStorage = "storage"
)

// RecordCascadeDeletionError increments the cascade-deletion failure counter
// for the given resource ("vector" or "storage").
func RecordCascadeDeletionError(resource string) {
	switch resource {
	case CascadeResourceVector, CascadeResourceStorage:
		cascadeDeletionErrorTotal.WithLabelValues(resource).Inc()
	default:
		cascadeDeletionErrorTotal.WithLabelValues("other").Inc()
	}
}

// --- Rate limiter -----------------------------------------------------------

var rateLimitFallbackTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:        "rag_rate_limit_fallback_total",
		Help:        "Redis-backed rate limiter fail-open events, labeled by category. Non-zero indicates Redis was unavailable and rate limiting was bypassed.",
		ConstLabels: commonLabels,
	},
	[]string{"category"},
)

// RecordRateLimitFallback increments the fail-open counter for the given
// rate-limit category (e.g. "login", "chat").
func RecordRateLimitFallback(category string) {
	if category == "" {
		category = "unknown"
	}
	rateLimitFallbackTotal.WithLabelValues(category).Inc()
}

var rateLimitRejectedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_rate_limit_rejected_total",
		Help: "429s issued by the rate limiters, labeled by limiter source " +
			"(e.g. redis:chat, redis:login, memory). A rising per-source rate " +
			"is the abuse-detection / capacity-planning signal that the " +
			"existing fail-open counter (rag_rate_limit_fallback_total) and " +
			"per-rejection warn log don't surface as a time series. Cardinality " +
			"is bounded by the fixed per-deployment limiter list.",
		ConstLabels: commonLabels,
	},
	[]string{"limiter"},
)

// RecordRateLimitRejected increments the per-limiter 429 counter. limiter is
// the same source string the rejection log carries (e.g. "redis:chat",
// "memory_check_only"); empty input normalizes to "unknown".
func RecordRateLimitRejected(limiter string) {
	if limiter == "" {
		limiter = "unknown"
	}
	rateLimitRejectedTotal.WithLabelValues(limiter).Inc()
}

var xffUntrustedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "rag_xff_untrusted_total",
		Help: "Requests that carried an X-Forwarded-For / X-Real-IP header " +
			"while TRUSTED_PROXY_HOP_COUNT=0, so the header was ignored and the " +
			"rate-limit / audit key fell back to the TCP peer. A non-zero rate " +
			"means the server is behind a proxy but not configured for it — rate " +
			"limiting and audit logs are recording the proxy IP, not the client. " +
			"Surfaces the misconfiguration the one-shot startup warning can miss.",
		ConstLabels: commonLabels,
	},
)

// RecordXFFUntrusted increments the untrusted-forwarding counter. Called from
// the IP extractor when a forwarding header is present but no proxy hops are
// trusted.
func RecordXFFUntrusted() {
	xffUntrustedTotal.Inc()
}

// --- Vector search (MRL two-pass) -------------------------------------------

var (
	vectorSearchDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "rag_vector_search_duration_seconds",
			Help:        "Latency of the vector-search SQL stage, labeled by retrieval mode (single_pass | two_pass).",
			Buckets:     []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2},
			ConstLabels: commonLabels,
		},
		[]string{"mode"},
	)

	mrlTwoPassUsedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name:        "rag_mrl_two_pass_used_total",
			Help:        "Total invocations of the MRL two-pass retrieval path. Per-KB breakdown lives in the rag.search.stages structured log.",
			ConstLabels: commonLabels,
		},
	)

	keywordSearchFailedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_keyword_search_failed_total",
			Help:        "BM25/keyword search failures that degraded the hybrid pipeline to vector-only results. arm=primary is the main runPrimarySearches goroutine; arm=multi_query is per-alt-query BM25 fan-out (RAG-Fusion / step-back / sub-queries).",
			ConstLabels: commonLabels,
		},
		[]string{"arm"},
	)

	searchDimMismatchTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "rag_search_dim_mismatch_total",
			Help: "Searches that targeted an empty dim-keyed chunk table while another " +
				"chunk table holds rows for the same KB — i.e. the query embedder's " +
				"dimension no longer matches the dimension the corpus was indexed " +
				"under. Every affected search increments this; a sustained rate means " +
				"users are silently getting zero results and the KB needs a re-ingest " +
				"(see the dim-mismatch warning log for kb_id/table detail).",
			ConstLabels: commonLabels,
		},
	)
)

// RecordVectorSearch records one vector-search SQL invocation.
// mode is one of "single_pass" or "two_pass".
func RecordVectorSearch(mode string, durationSeconds float64) {
	vectorSearchDuration.WithLabelValues(mode).Observe(durationSeconds)
}

// RecordMRLTwoPassUsed increments the two-pass invocation counter.
func RecordMRLTwoPassUsed() {
	mrlTwoPassUsedTotal.Inc()
}

// RecordKeywordSearchFailed increments the BM25 fallback counter. arm is one
// of "primary" (main pipeline degraded to vector-only) or "multi_query"
// (per-alt-query BM25 fan-out where the failed arm is dropped from RRF).
// Callers should also emit a structured log line — the metric is the
// dashboard signal, the log carries the error and query context.
func RecordKeywordSearchFailed(arm string) {
	keywordSearchFailedTotal.WithLabelValues(arm).Inc()
}

// RecordSearchDimMismatch increments the embedder/corpus dimension-mismatch
// counter. Unlike the once-per-(kb,table) log warning, this fires on every
// affected search so the mismatch shows up as a rate on dashboards.
func RecordSearchDimMismatch() {
	searchDimMismatchTotal.Inc()
}

// --- Reranker health --------------------------------------------------------

// RerankerDegenerateStddevThreshold is the stddev floor below which the
// reranker score distribution is considered collapsed/degenerate. Set at
// 2.5x the empirically observed Qwen3-Reranker-via-vLLM-pooling collapse
// band (~0.02), tight enough that bge-v2-m3 healthy distributions never
// trigger, loose enough to catch other models exhibiting the same failure.
const RerankerDegenerateStddevThreshold = 0.05

var (
	rerankerScoreStddev = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_reranker_score_stddev",
			Help:        "Population standard deviation of cross-encoder rerank scores per search call. Healthy bge-v2-m3 ~0.2-0.4; collapsed Qwen3-Reranker ~0.01-0.02.",
			Buckets:     []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.3, 0.4, 0.5},
			ConstLabels: commonLabels,
		},
	)

	rerankerDegenerateTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name:        "rag_reranker_degenerate_total",
			Help:        "Searches where the reranker score distribution stddev fell below RerankerDegenerateStddevThreshold (calibration collapse).",
			ConstLabels: commonLabels,
		},
	)
)

// RecordRerankerHealth observes a reranker-score stddev measurement and
// increments the degenerate counter if it falls below the threshold.
// Caller is responsible for emitting a structured log line — this helper
// only handles the metric side.
func RecordRerankerHealth(stddev float64) {
	rerankerScoreStddev.Observe(stddev)
	if stddev < RerankerDegenerateStddevThreshold {
		rerankerDegenerateTotal.Inc()
	}
}

// --- Worker task lifecycle --------------------------------------------------

var (
	workerTaskTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_task_total",
			Help: "Asynq worker task completions, labeled by task_type and status. " +
				"Status is a closed enum: success, error.",
			ConstLabels: commonLabels,
		},
		[]string{"task_type", "status"},
	)

	workerTaskDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "worker_task_duration_seconds",
			Help: "Wall-time duration of one Asynq worker task invocation, " +
				"labeled by task_type. Buckets cover sub-second telemetry " +
				"jobs through long-running file ingestion.",
			Buckets:     []float64{0.05, 0.25, 1, 5, 15, 60, 300, 900},
			ConstLabels: commonLabels,
		},
		[]string{"task_type"},
	)

	workerTaskExhaustedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_task_exhausted_total",
			Help: "Asynq tasks that failed their final retry and were moved to " +
				"the asynq archive (dead-letter set in Redis), labeled by " +
				"task_type. Archived tasks are inspectable via the asynq CLI / " +
				"Inspector; a non-zero rate warrants looking at the matching " +
				"'task exhausted retries' error logs.",
			ConstLabels: commonLabels,
		},
		[]string{"task_type"},
	)
)

// RecordWorkerTask is the worker package's single point of telemetry emission
// for task lifecycle. status must be either "success" or "error"; any other
// value is normalised to "error" so the cardinality on the status axis stays
// bounded. taskType is provider-defined and uses the asynq Task.Type() value.
func RecordWorkerTask(taskType, status string, duration time.Duration) {
	switch status {
	case "success", "error":
	default:
		status = "error"
	}
	workerTaskTotal.WithLabelValues(taskType, status).Inc()
	workerTaskDuration.WithLabelValues(taskType).Observe(duration.Seconds())
}

// RecordWorkerTaskExhausted increments the retry-exhaustion counter for a
// task type. Called from the asynq ErrorHandler on the final failed attempt.
func RecordWorkerTaskExhausted(taskType string) {
	workerTaskExhaustedTotal.WithLabelValues(taskType).Inc()
}

// WorkerTaskTotalForTest exposes the worker task counter to other test
// packages so they can assert per-(task_type, status) deltas without
// reaching into private state. Mirrors RAGASSampleTotalForTest.
func WorkerTaskTotalForTest() *prometheus.CounterVec {
	return workerTaskTotal
}

// --- Model fallback ---------------------------------------------------------

var modelFallbackTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_model_fallback_total",
		Help: "Times a requested chat model override was unavailable in the " +
			"resolved provider config and the call fell back to the default " +
			"chat model. Non-zero indicates production traffic running against " +
			"a weaker/different model than the caller intended. Label 'reason' " +
			"is a closed enum: not_in_chat_models (override not in the resolved " +
			"ChatModels allowlist).",
		ConstLabels: commonLabels,
	},
	[]string{"reason"},
)

// RecordModelFallback increments the model-fallback counter for the given
// reason. Empty input normalizes to "not_in_chat_models" since that is
// currently the only fallback path; future fallback reasons should add
// constants here rather than letting callers pass arbitrary strings.
func RecordModelFallback(reason string) {
	if reason == "" {
		reason = "not_in_chat_models"
	}
	modelFallbackTotal.WithLabelValues(reason).Inc()
}

// --- Pipeline stage timing --------------------------------------------------

var stageDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "rag_pipeline_stage_duration_seconds",
		Help: "Per-stage RAG pipeline latency in seconds. Stage labels are " +
			"a closed enum: embed, primary_search, multi_query, rrf, rerank, " +
			"score_filter, dedup, mmr, bm25_floor.",
		Buckets:     []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		ConstLabels: commonLabels,
	},
	[]string{"stage"},
)

// RecordStageDuration observes one stage transition. The stage label is
// expected to be one of the closed enum listed in the histogram Help text;
// any other value is accepted but pollutes the cardinality budget.
func RecordStageDuration(stage string, d time.Duration) {
	stageDuration.WithLabelValues(stage).Observe(d.Seconds())
}

// --- Query type classification --------------------------------------------

var queryTypeTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_query_type_total",
		Help: "RAG search calls by query-type classification. Label values " +
			"are a closed enum: lookup, enumeration, complex_reasoning, unknown.",
		ConstLabels: commonLabels,
	},
	[]string{"query_type"},
)

// RecordQueryType increments the per-classification counter. Empty input
// is normalized to "unknown" to keep the label space bounded.
func RecordQueryType(queryType string) {
	if queryType == "" {
		queryType = "unknown"
	}
	queryTypeTotal.WithLabelValues(queryType).Inc()
}

// --- Adaptive routing (Phase 2 §C) ----------------------------------------

var adaptiveRoutingDecisionTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_adaptive_routing_decision_total",
		Help: "Adaptive-routing decisions per chat request. Label values " +
			"are a closed enum: disabled_crag (routing fired and turned " +
			"CRAG off for this request), kept (routing was on but the " +
			"classifier didn't trigger a disable), n_a (routing was off, " +
			"or CRAG was already off, so no decision to record).",
		ConstLabels: commonLabels,
	},
	[]string{"action"},
)

// RecordAdaptiveRoutingDecision increments the per-action counter. Empty
// input is normalized to "n_a" to keep the label space bounded — the
// caller never has a fourth meaningful state.
func RecordAdaptiveRoutingDecision(action string) {
	if action == "" {
		action = "n_a"
	}
	adaptiveRoutingDecisionTotal.WithLabelValues(action).Inc()
}

// --- Step-back prompting (Feature 2) -----------------------------------------

var stepBackDecisionTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:        "rag_stepback_decision_total",
		Help:        "Per-action step-back outcome counter. Outcomes: fired (LLM call succeeded and result joined the candidate pool), skipped_route (query type is not complex_reasoning), skipped_disabled (step_back_enabled site_config is false), llm_error (the broader-query LLM call failed), empty_output (LLM returned no usable content).",
		ConstLabels: commonLabels,
	},
	[]string{"outcome"},
)

var StepBackSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_stepback_seconds",
		Help:        "Wall-time of the step-back LLM call (excluding the extra vector search, which is captured by the existing search-stage histograms).",
		Buckets:     prometheus.DefBuckets,
		ConstLabels: commonLabels,
	},
)

// RecordStepBackDecision increments the per-outcome counter. Empty input
// is normalized to "llm_error" to keep the label space bounded — an empty
// outcome typically means the caller hit a code path that didn't classify
// the failure, which is itself a degenerate-LLM-error signal.
func RecordStepBackDecision(outcome string) {
	if outcome == "" {
		outcome = "llm_error"
	}
	stepBackDecisionTotal.WithLabelValues(outcome).Inc()
}

// --- Long-context routing (T2-1) ------------------------------------------

var longContextRouteTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:        "rag_longcontext_route_total",
		Help:        "Per-outcome counter for the T2-1 long-context (System 2) routing. Outcomes: fired (the keyword classifier matched and SearchOptions.LongContextMode was set), considered (the operator gate is on but the classifier did not match this query — useful to audit firing rate against typical traffic), skipped_disabled (the operator gate is off — only emitted from the per-turn audit path when explicitly enabled). Production deployments expect `fired ≪ considered` at chat_longcontext_enabled=true.",
		ConstLabels: commonLabels,
	},
	[]string{"outcome"},
)

// RecordLongContextRoute increments the per-outcome counter.
func RecordLongContextRoute(outcome string) {
	if outcome == "" {
		outcome = "skipped_disabled"
	}
	longContextRouteTotal.WithLabelValues(outcome).Inc()
}

// --- Evidentiality compression (ECoRAG, T2-3) ------------------------------

var contextCompressionDecisionTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:        "rag_context_compression_decision_total",
		Help:        "Per-action ECoRAG compression outcome counter. Outcomes: fired (≥1 chunk dropped), no_drops (LLM scored everything above threshold), all_dropped (defensive fallback fired — every chunk scored below threshold, so the original set was kept), llm_error (classifier call failed), empty (LLM returned no parseable scores).",
		ConstLabels: commonLabels,
	},
	[]string{"outcome"},
)

var contextCompressionDroppedCount = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_context_compression_dropped",
		Help:        "Number of chunks dropped per chat turn that successfully fired the ECoRAG compressor (i.e. outcome=fired). 0-drop runs are recorded in the decision counter as no_drops instead.",
		Buckets:     []float64{1, 2, 3, 5, 8, 13, 21, 34},
		ConstLabels: commonLabels,
	},
)

// RecordContextCompressionDecision increments the per-outcome
// counter. Empty input normalises to llm_error.
func RecordContextCompressionDecision(outcome string) {
	if outcome == "" {
		outcome = "llm_error"
	}
	contextCompressionDecisionTotal.WithLabelValues(outcome).Inc()
}

// RecordContextCompressionDropped observes the per-turn drop count.
func RecordContextCompressionDropped(n int) {
	contextCompressionDroppedCount.Observe(float64(n))
}

// --- Sub-question decomposition (DecomposeRAG) -----------------------------

var queryDecomposeDecisionTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:        "rag_query_decompose_decision_total",
		Help:        "Per-action sub-question decomposition outcome counter. Outcomes: fired (LLM produced 1+ sub-questions that joined SubQueries), skipped_route (query type is not complex_reasoning), skipped_disabled (query_decompose_enabled site_config is false), skipped_existing (caller pre-populated SubQueries — e.g. Plan-Execute orchestrator), llm_error (decomposition LLM call failed), empty_output (LLM returned no sub-questions — single-aspect query).",
		ConstLabels: commonLabels,
	},
	[]string{"outcome"},
)

var QueryDecomposeSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_query_decompose_seconds",
		Help:        "Wall-time of the decomposition LLM call (excluding the extra vector searches for the sub-questions, which are captured by the existing search-stage histograms).",
		Buckets:     prometheus.DefBuckets,
		ConstLabels: commonLabels,
	},
)

var queryDecomposeSubQueriesCount = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_query_decompose_subqueries",
		Help:        "Number of sub-questions returned by a successful decomposition LLM call. Capped at maxDecomposedSubQueries (4) on the parse side.",
		Buckets:     []float64{0, 1, 2, 3, 4, 5},
		ConstLabels: commonLabels,
	},
)

// RecordQueryDecomposeDecision increments the per-outcome counter, with
// the same empty→"llm_error" normalization as RecordStepBackDecision.
func RecordQueryDecomposeDecision(outcome string) {
	if outcome == "" {
		outcome = "llm_error"
	}
	queryDecomposeDecisionTotal.WithLabelValues(outcome).Inc()
}

// RecordQueryDecomposeSubQueries observes the count of sub-questions
// the decomposition produced (post-trim, post-cap).
func RecordQueryDecomposeSubQueries(n int) {
	queryDecomposeSubQueriesCount.Observe(float64(n))
}

// --- Semantic query cache (Feature 3) ---------------------------------------

var queryCacheHitTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:        "rag_query_cache_hit_total",
		Help:        "Per-tier query cache hits. Tiers: exact (cosine distance < 0.001 — effectively the same query), semantic (paraphrase match in [0.001, 1-threshold]).",
		ConstLabels: commonLabels,
	},
	[]string{"tier"},
)

var queryCacheMissTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name:        "rag_query_cache_miss_total",
		Help:        "Total query-cache misses. Cardinality stays bounded by not labeling per-KB; per-KB hit-rate is derivable by sampling rag.query_cache.miss log events.",
		ConstLabels: commonLabels,
	},
)

var queryCacheInvalidationsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name:        "rag_query_cache_invalidations_total",
		Help:        "Total per-KB nuke events triggered by file ingestion / deletion / re-ingestion.",
		ConstLabels: commonLabels,
	},
)

var queryCacheWriteDroppedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name:        "rag_query_cache_write_dropped_total",
		Help:        "Total async cache-write attempts dropped because the bounded write channel was full. A non-zero rate means the cache is undersized vs. miss volume.",
		ConstLabels: commonLabels,
	},
)

var researchEventsDroppedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name:        "rag_research_events_dropped_total",
		Help:        "Total research-agent progress events dropped because the bounded SSE buffer was full. A non-zero rate means a slow client (or SSE relay) is falling behind the agent and the client's event stream has gaps.",
		ConstLabels: commonLabels,
	},
)

var queryCacheDimSkippedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "rag_query_cache_dim_skipped_total",
		Help: "Query-cache lookups/stores skipped because the query embedding's " +
			"dimension does not match the fixed vector(2560) query_cache column " +
			"(migration 0011). A sustained rate means the deployment's embedder " +
			"changed dimension and the cache is effectively disabled until the " +
			"query_cache table is rebuilt for the new dimension.",
		ConstLabels: commonLabels,
	},
)

var QueryCacheSize = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name:        "rag_query_cache_size",
		Help:        "Total query_cache rows. Set by the eviction sweeper after each run.",
		ConstLabels: commonLabels,
	},
)

var QueryCacheLookupSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_query_cache_lookup_seconds",
		Help:        "Wall-time of one query-cache lookup (single ANN query). Excludes the embedding step (covered by rag_embedding_cache hits/misses).",
		Buckets:     prometheus.DefBuckets,
		ConstLabels: commonLabels,
	},
)

func RecordQueryCacheHit(tier string) {
	queryCacheHitTotal.WithLabelValues(tier).Inc()
}

func RecordQueryCacheMiss() {
	queryCacheMissTotal.Inc()
}

func RecordQueryCacheInvalidation() {
	queryCacheInvalidationsTotal.Inc()
}

func RecordQueryCacheWriteDropped() {
	queryCacheWriteDroppedTotal.Inc()
}

// RecordResearchEventDropped counts a research-agent progress event dropped
// because its bounded SSE buffer was full.
func RecordResearchEventDropped() {
	researchEventsDroppedTotal.Inc()
}

func RecordQueryCacheDimSkipped() {
	queryCacheDimSkippedTotal.Inc()
}

// --- RAGAS sampling (Phase 3 §G) ------------------------------------------

var (
	ragasFaithfulnessScore = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_ragas_faithfulness_score",
			Help:        "Per-sample faithfulness score (fraction of answer claims supported by retrieved context). Range [0, 1]. Histograms allow alerting on rolling p50/p95 drift.",
			Buckets:     []float64{0.0, 0.25, 0.5, 0.7, 0.8, 0.9, 0.95, 1.0},
			ConstLabels: commonLabels,
		},
	)
	ragasAnswerRelevanceScore = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_ragas_answer_relevance_score",
			Help:        "Per-sample answer-relevance score (1-5 LLM rating, normalized to [0, 1]).",
			Buckets:     []float64{0.0, 0.25, 0.5, 0.75, 1.0},
			ConstLabels: commonLabels,
		},
	)
	ragasContextPrecisionScore = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_ragas_context_precision_score",
			Help:        "Per-sample context-precision score (fraction of retrieved chunks judged relevant). Range [0, 1].",
			Buckets:     []float64{0.0, 0.25, 0.5, 0.7, 0.8, 0.9, 0.95, 1.0},
			ConstLabels: commonLabels,
		},
	)
	ragasSampleTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_ragas_sample_total",
			Help: "RAGAS sampling outcomes per Asynq task. Label 'outcome' is a closed enum: " +
				"completed (judge ran and at least one metric was emitted), " +
				"error (worker failed before emitting any metric).",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)
)

// RecordRAGASSample is the worker's single point of telemetry emission. It
// observes whichever of the three judge metrics are non-nil (a nil metric
// means the judge skipped or failed that prompt — observing 0.0 would
// pollute the histograms with false negatives), then increments the
// outcome counter exactly once. Empty outcome normalizes to "error" so
// the counter cardinality stays bounded even if the caller forgets.
func RecordRAGASSample(outcome string, faithfulness, answerRelevance, contextPrecision *float64) {
	if outcome == "" {
		outcome = "error"
	}
	if faithfulness != nil {
		ragasFaithfulnessScore.Observe(*faithfulness)
	}
	if answerRelevance != nil {
		ragasAnswerRelevanceScore.Observe(*answerRelevance)
	}
	if contextPrecision != nil {
		ragasContextPrecisionScore.Observe(*contextPrecision)
	}
	ragasSampleTotal.WithLabelValues(outcome).Inc()
}

// RAGASSampleTotalForTest exposes the underlying counter to other test
// packages so handler tests can assert per-outcome deltas without a
// separate accounting mechanism. Returning the *CounterVec lets the
// caller use testutil.ToFloat64(... .WithLabelValues(...)) directly.
func RAGASSampleTotalForTest() *prometheus.CounterVec {
	return ragasSampleTotal
}

// --- Agentic chat (Phase 3 §F) --------------------------------------------

var (
	agenticHops = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_agentic_hops",
			Help:        "Number of search hops the agentic chat loop performed. 1 = no follow-ups (judge never invoked or fired off after first hop). Range [1, 5] (clamped by chat_agentic_max_hops range guard).",
			Buckets:     []float64{1, 2, 3, 4, 5},
			ConstLabels: commonLabels,
		},
	)
	agenticDecisionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_agentic_decision_total",
			Help: "Why each agentic chat run stopped. Label 'outcome' is a closed enum: " +
				"max_hops_reached (used the full hop budget), " +
				"early_stop_sufficient (judge said current chunks suffice), " +
				"early_stop_no_progress (last hop added zero new chunks after dedup), " +
				"early_stop_no_query (judge said insufficient but didn't propose a follow-up query), " +
				"early_stop_plateau (≥2 consecutive low-yield hops; chat_agentic_plateau_stop on), " +
				"judge_error (LLM critique call failed; loop bailed gracefully), " +
				"search_error (search call failed mid-loop; loop bailed gracefully).",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)
)

// RecordAgenticChatDecision is the agentic-chat orchestrator's single point
// of telemetry emission. It observes the final hop count on the histogram
// and increments the outcome counter exactly once. Empty outcome normalizes
// to "max_hops_reached" (the most likely default state for callers that
// reach the end of the loop without stopping early).
func RecordAgenticChatDecision(outcome string, hops int) {
	if outcome == "" {
		outcome = "max_hops_reached"
	}
	if hops > 0 {
		agenticHops.Observe(float64(hops))
	}
	agenticDecisionTotal.WithLabelValues(outcome).Inc()
}

// AgenticDecisionTotalForTest exposes the agentic-chat decision counter
// to other test packages so handler tests can assert per-outcome deltas
// without a separate accounting mechanism. Mirrors RAGASSampleTotalForTest.
func AgenticDecisionTotalForTest() *prometheus.CounterVec {
	return agenticDecisionTotal
}

// --- Parent-child swap (Phase 3 §D) ---------------------------------------

var parentSwapTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_parent_swap_total",
		Help: "Parent-child swap outcomes per search call. Label 'outcome' " +
			"is a closed enum: swapped (count of child→parent content swaps " +
			"performed in this search), lookup_failed (parent table query " +
			"errored — search degrades to child content). The Add value is " +
			"the magnitude (number of rows in this search affected).",
		ConstLabels: commonLabels,
	},
	[]string{"outcome"},
)

// RecordParentSwap increments the counter by `count`. count is the number
// of rows in the current search affected by the named outcome. Empty
// outcome normalizes to "lookup_failed" so misuse is visible without
// silently inflating the swapped bucket.
func RecordParentSwap(outcome string, count int) {
	if outcome == "" {
		outcome = "lookup_failed"
	}
	if count <= 0 {
		return
	}
	parentSwapTotal.WithLabelValues(outcome).Add(float64(count))
}

// --- BM25 floor reinsertion -----------------------------------------------

var bm25FloorReinserted = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name: "rag_bm25_floor_reinserted",
		Help: "Number of chunks ApplyBM25Floor reinserted per search call. " +
			"A rising distribution is a leading indicator the reranker is " +
			"miscalibrated against current ingestion (the floor is having " +
			"to fight harder to rescue keyword-strong evidence).",
		Buckets:     []float64{0, 1, 2, 3, 5, 8, 12},
		ConstLabels: commonLabels,
	},
)

// RecordBM25FloorReinserted records the per-search count of chunks that
// ApplyBM25Floor rescued. Always called (count=0 is the healthy steady
// state) so the histogram captures the full distribution rather than only
// the firing tail.
func RecordBM25FloorReinserted(count int) {
	bm25FloorReinserted.Observe(float64(count))
}

// --- Plan-and-Execute (Phase 1) -------------------------------------------

var (
	planExecuteRounds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_plan_execute_rounds",
			Help:        "Number of iteration rounds (excluding Plan) the plan-and-execute loop performed. 0 = answered straight from Plan; up to chat_plan_execute_max_iterations (default 3).",
			Buckets:     []float64{0, 1, 2, 3, 4, 5},
			ConstLabels: commonLabels,
		},
	)
	planExecuteSubqueries = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "rag_plan_execute_subqueries_per_call",
			Help:        "Number of sub-queries emitted per LLM call. Label 'stage' = 'plan' (1..max_sub_queries) or 'iterate' (0..3 — 0 means action=answer).",
			Buckets:     []float64{0, 1, 2, 3, 4, 5},
			ConstLabels: commonLabels,
		},
		[]string{"stage"},
	)
	planExecuteLLMSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "rag_plan_execute_llm_seconds",
			Help:        "Wall time per Plan or Iterate LLM call. Label 'stage' = 'plan' or 'iterate'.",
			Buckets:     []float64{0.1, 0.3, 0.5, 1, 2, 3, 5, 10},
			ConstLabels: commonLabels,
		},
		[]string{"stage"},
	)
	planExecuteDecisionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_plan_execute_decision_total",
			Help: "Why each plan-and-execute run stopped. Label 'outcome' is a closed enum: " +
				"answered_after_plan (no iter rounds needed), " +
				"answered_after_iter (LLM said action=answer), " +
				"max_iter_reached, " +
				"no_progress (>=2 rounds added 0 chunks), " +
				"early_stop_plateau (≥2 consecutive low-yield rounds; chat_agentic_plateau_stop on), " +
				"budget_exhausted (token budget hit), " +
				"plan_error (Plan LLM call failed; fell back to single-query baseline), " +
				"iter_error (Iterate LLM call failed mid-loop; bailed gracefully).",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)
)

// RecordPlanExecuteDecision is the plan-execute orchestrator's single
// telemetry emission point. Observes the iteration-rounds histogram,
// observes one sub-query histogram entry per perRoundSubqueries entry
// (one per LLM call this run made), and increments the outcome counter
// once. Empty outcome normalizes to "max_iter_reached".
func RecordPlanExecuteDecision(outcome string, rounds int, perRoundSubqueries []int) {
	if outcome == "" {
		outcome = "max_iter_reached"
	}
	if rounds >= 0 {
		planExecuteRounds.Observe(float64(rounds))
	}
	planExecuteDecisionTotal.WithLabelValues(outcome).Inc()
	// perRoundSubqueries[0] is the Plan call; remaining entries are Iterate calls.
	for i, n := range perRoundSubqueries {
		stage := "iterate"
		if i == 0 {
			stage = "plan"
		}
		planExecuteSubqueries.WithLabelValues(stage).Observe(float64(n))
	}
}

// ObservePlanExecuteLLMSeconds records the latency of a single Plan or
// Iterate LLM call. stage MUST be "plan" or "iterate".
func ObservePlanExecuteLLMSeconds(stage string, seconds float64) {
	planExecuteLLMSeconds.WithLabelValues(stage).Observe(seconds)
}

// PlanExecuteDecisionTotalForTest exposes the counter to other test
// packages so handler tests can assert per-outcome deltas. Mirrors
// AgenticDecisionTotalForTest.
func PlanExecuteDecisionTotalForTest() *prometheus.CounterVec {
	return planExecuteDecisionTotal
}

// --- Agentic trajectory (Phase 1 §1.1) ------------------------------------

var trajectoryEventsEmittedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_trajectory_events_emitted_total",
		Help: "Per-stage agent-trajectory events emitted to streaming clients. " +
			"Stage values: plan, iterate, hop, decision, answer. Empty input " +
			"normalizes to 'unknown' so a regression that stops setting the " +
			"stage label surfaces visibly instead of silently inflating one bucket.",
		ConstLabels: commonLabels,
	},
	[]string{"stage"},
)

// RecordTrajectoryEmit increments the per-stage trajectory-event counter.
// This is a sanity gauge — every orchestrator decision point should emit
// at least one event, so a flat counter signals a wiring regression.
func RecordTrajectoryEmit(stage string) {
	if stage == "" {
		stage = "unknown"
	}
	trajectoryEventsEmittedTotal.WithLabelValues(stage).Inc()
}

// TrajectoryEventsEmittedTotalForTest exposes the counter to other test
// packages so trajectory-emission tests can assert per-stage deltas.
func TrajectoryEventsEmittedTotalForTest() *prometheus.CounterVec {
	return trajectoryEventsEmittedTotal
}

// --- MCP tool calls (Phase 2 §2.1) ----------------------------------------

var (
	mcpToolCallTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_mcp_tool_call_total",
			Help: "Per-tool MCP dispatch outcomes. Labels: tool (closed-enum " +
				"per registered tool — built-ins like kb_search/web_search " +
				"plus admin-configured remote tools), status (ok | unknown | " +
				"schema_error | server_error | timeout). Cardinality is " +
				"bounded by the per-deployment tool list; a misconfigured " +
				"server adds at most one new label value per `name` field.",
			ConstLabels: commonLabels,
		},
		[]string{"tool", "status"},
	)

	mcpToolCallSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "rag_mcp_tool_call_seconds",
			Help: "Per-tool MCP dispatch latency in seconds. Buckets cover " +
				"in-process built-ins (sub-100ms) through HTTP-backed remote " +
				"tools that may take seconds. The 10s tail aligns with " +
				"DefaultToolCallTimeout — anything beyond hits the timeout " +
				"branch in the counter and is recorded as 10s here.",
			Buckets:     []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
			ConstLabels: commonLabels,
		},
		[]string{"tool"},
	)
)

// RecordMCPToolCall increments the per-(tool, status) MCP dispatch
// counter. Empty inputs normalize to "unknown" so a regression in the
// dispatch layer is visible instead of silently inflating one bucket.
func RecordMCPToolCall(tool, status string) {
	if tool == "" {
		tool = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	mcpToolCallTotal.WithLabelValues(tool, status).Inc()
}

// ObserveMCPToolCallSeconds records the latency of one MCP dispatch.
func ObserveMCPToolCallSeconds(tool string, seconds float64) {
	if tool == "" {
		tool = "unknown"
	}
	mcpToolCallSeconds.WithLabelValues(tool).Observe(seconds)
}

// MCPToolCallTotalForTest exposes the counter to other test packages so
// dispatch tests can assert per-(tool, status) deltas.
func MCPToolCallTotalForTest() *prometheus.CounterVec {
	return mcpToolCallTotal
}

// --- DAG planner (Phase 3 §3.1) -------------------------------------------

var (
	planDAGNodeTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_plan_dag_nodes_total",
			Help: "Per-result counter for DAG plan nodes. Result labels: " +
				"succeeded (node ran and produced chunks), " +
				"depends_failed (parent node errored before this child ran), " +
				"search_error (node's own retrieval call returned an error).",
			ConstLabels: commonLabels,
		},
		[]string{"result"},
	)

	planDAGMaxDepth = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_plan_dag_max_depth",
			Help:        "Longest dependency chain in successfully-executed DAG plans. 1 = flat (no dependencies); 3 = the plan's hard cap.",
			Buckets:     []float64{1, 2, 3, 4, 5},
			ConstLabels: commonLabels,
		},
	)

	planDAGParallelNodes = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_plan_dag_parallel_nodes",
			Help:        "Maximum number of nodes that ran in parallel within one DAG plan execution. 1 = serial; >1 = the planner produced independent sub-queries the executor fanned out via errgroup.",
			Buckets:     []float64{1, 2, 3, 4, 5, 6},
			ConstLabels: commonLabels,
		},
	)

	planDAGInvalidTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_plan_dag_invalid_total",
			Help: "Per-reason counter for DAG plans that failed validation and " +
				"fell back to flat-list execution. Reasons: cycle, too_deep, " +
				"too_wide, missing_parent. Operators monitor this to decide " +
				"whether the planner prompt needs tightening.",
			ConstLabels: commonLabels,
		},
		[]string{"reason"},
	)
)

// RecordPlanDAGNode increments the per-result counter for one DAG node.
func RecordPlanDAGNode(result string) {
	if result == "" {
		result = "search_error"
	}
	planDAGNodeTotal.WithLabelValues(result).Inc()
}

// ObservePlanDAGShape records the depth and parallelism of one
// successfully-executed plan. Caller must NOT call this when the plan
// fell back to flat execution (RecordPlanDAGInvalid covers that path).
func ObservePlanDAGShape(maxDepth, parallelNodes int) {
	if maxDepth > 0 {
		planDAGMaxDepth.Observe(float64(maxDepth))
	}
	if parallelNodes > 0 {
		planDAGParallelNodes.Observe(float64(parallelNodes))
	}
}

// RecordPlanDAGInvalid increments the validation-failure counter for
// the given reason. Empty input normalizes to "missing_parent" so a
// caller-side typo surfaces rather than inflating the cycle bucket.
func RecordPlanDAGInvalid(reason string) {
	switch reason {
	case "cycle", "too_deep", "too_wide", "missing_parent":
	default:
		reason = "missing_parent"
	}
	planDAGInvalidTotal.WithLabelValues(reason).Inc()
}

// PlanDAGNodeTotalForTest exposes the node counter for test assertions.
func PlanDAGNodeTotalForTest() *prometheus.CounterVec {
	return planDAGNodeTotal
}

// --- Specialist agents (Phase 3 §3.2) -------------------------------------

var (
	agentDispatchTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_agent_dispatch_total",
			Help: "Per-(specialist,status) supervisor-dispatch counter. " +
				"Specialists: retriever, enumerator, synthesizer (the latter " +
				"reserved — v1 keeps synthesis in the chat orchestrator). " +
				"Status: ok | error.",
			ConstLabels: commonLabels,
		},
		[]string{"specialist", "status"},
	)

	agentExecutionSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "rag_agent_execution_seconds",
			Help:        "Per-specialist execution wall-time. The 12s tail aligns with SupervisorTimeout — slower calls hit the timeout branch in the dispatch counter.",
			Buckets:     []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 12},
			ConstLabels: commonLabels,
		},
		[]string{"specialist"},
	)
)

// RecordAgentDispatch increments the per-(specialist, status) counter.
// Empty inputs normalize to "unknown" so a regression at the dispatch
// layer surfaces visibly.
func RecordAgentDispatch(specialist, status string) {
	if specialist == "" {
		specialist = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	agentDispatchTotal.WithLabelValues(specialist, status).Inc()
}

// ObserveAgentExecutionSeconds records one specialist execution latency.
func ObserveAgentExecutionSeconds(specialist string, seconds float64) {
	if specialist == "" {
		specialist = "unknown"
	}
	agentExecutionSeconds.WithLabelValues(specialist).Observe(seconds)
}

// AgentDispatchTotalForTest exposes the counter for test assertions.
func AgentDispatchTotalForTest() *prometheus.CounterVec {
	return agentDispatchTotal
}

// --- Factuality verifier (Phase 3 §3.3) -----------------------------------

var (
	factualityVerifierTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_factuality_verifier_total",
			Help: "Per-result counter for factuality-verifier runs. Result " +
				"values: clean (no claims flagged), flagged (>=1 claim), " +
				"error (LLM call or parse failure), skipped_no_citation_warn " +
				"(cost gate: citation validator did not raise a suspect).",
			ConstLabels: commonLabels,
		},
		[]string{"result"},
	)

	factualityVerifierSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_factuality_verifier_seconds",
			Help:        "Wall-time of one factuality-verifier LLM call.",
			Buckets:     []float64{0.1, 0.25, 0.5, 1, 2, 5, 10},
			ConstLabels: commonLabels,
		},
	)
)

// RecordFactualityVerifier increments the per-result counter. Empty
// inputs normalize to "error" so caller-side typos surface visibly.
func RecordFactualityVerifier(result string) {
	switch result {
	case "clean", "flagged", "error", "skipped_no_citation_warn":
	default:
		result = "error"
	}
	factualityVerifierTotal.WithLabelValues(result).Inc()
}

// ObserveFactualityVerifierSeconds records the LLM call latency.
func ObserveFactualityVerifierSeconds(seconds float64) {
	factualityVerifierSeconds.Observe(seconds)
}

// FactualityVerifierTotalForTest exposes the counter for test assertions.
func FactualityVerifierTotalForTest() *prometheus.CounterVec {
	return factualityVerifierTotal
}

// --- Citation attribution (per-citation validator outcomes) ----------------

// citationAttributionsTotal counts individual citation MARKERS, unlike the
// per-answer factuality/citation outcome counters. verified/(verified+
// unverified) is the attribution rate — the fraction of [N] markers the
// validator could ground in their cited source. method records which tier
// grounded it (ngram / semantic) or "none" for an unverified marker.
var citationAttributionsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_citation_attributions_total",
		Help: "Per-citation-marker validator outcomes. result=verified|unverified; " +
			"method=ngram|semantic|none. verified/(verified+unverified) over a " +
			"window is the attribution rate.",
		ConstLabels: commonLabels,
	},
	[]string{"result", "method"},
)

// RecordCitationAttribution increments the per-citation counter once per
// citation marker. verified reflects CitationStatus.Verified; method is
// CitationStatus.Method ("" or unknown normalizes to "none").
func RecordCitationAttribution(verified bool, method string) {
	result := "unverified"
	if verified {
		result = "verified"
	}
	switch method {
	case "ngram", "semantic":
	default:
		method = "none"
	}
	citationAttributionsTotal.WithLabelValues(result, method).Inc()
}

// CitationAttributionsTotalForTest exposes the counter for test assertions.
func CitationAttributionsTotalForTest() *prometheus.CounterVec {
	return citationAttributionsTotal
}

// --- Factuality refine gate (AP-A1) ---------------------------------------

var (
	factualityRefineTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_factuality_refine_total",
			Help: "Per-result counter for factuality-refine gate runs. " +
				"Result values: refined (LLM produced new content), " +
				"unchanged (LLM returned identical text — verifier was " +
				"likely a false positive), error (LLM call or empty " +
				"completion).",
			ConstLabels: commonLabels,
		},
		[]string{"result"},
	)

	factualityRefineSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_factuality_refine_seconds",
			Help:        "Wall-time of one factuality-refine LLM call.",
			Buckets:     []float64{0.25, 0.5, 1, 2, 5, 10, 20},
			ConstLabels: commonLabels,
		},
	)
)

// RecordFactualityRefine increments the per-result counter. Empty or
// unknown inputs normalize to "error" so caller-side typos surface.
func RecordFactualityRefine(result string) {
	switch result {
	case "refined", "unchanged", "error":
	default:
		result = "error"
	}
	factualityRefineTotal.WithLabelValues(result).Inc()
}

// ObserveFactualityRefineSeconds records the LLM-call latency.
func ObserveFactualityRefineSeconds(seconds float64) {
	factualityRefineSeconds.Observe(seconds)
}

// FactualityRefineTotalForTest exposes the counter for test assertions.
func FactualityRefineTotalForTest() *prometheus.CounterVec {
	return factualityRefineTotal
}

// --- Turn budget (AP-A3) --------------------------------------------------

var turnBudgetExceededTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_turn_budget_exceeded_total",
		Help: "Per-(kind,orchestrator) counter for AP-A3 turn-budget exhaustion. " +
			"kind ∈ {time, tokens, tool_calls}; orchestrator ∈ " +
			"{agentic, plan_execute, supervisor, standard}. Empty kind " +
			"normalises to 'unknown' so a label-less regression surfaces.",
		ConstLabels: commonLabels,
	},
	[]string{"kind", "orchestrator"},
)

// RecordTurnBudgetExceeded increments the counter when an orchestrator
// detects budget exhaustion before an expensive step. It does NOT
// fire when the budget runs out asymptotically — only at the moment
// the orchestrator decides "I can't do another LLM/tool call".
func RecordTurnBudgetExceeded(kind, orchestrator string) {
	if kind == "" {
		kind = "unknown"
	}
	if orchestrator == "" {
		orchestrator = "unknown"
	}
	turnBudgetExceededTotal.WithLabelValues(kind, orchestrator).Inc()
}

// TurnBudgetExceededTotalForTest exposes the counter for test assertions.
func TurnBudgetExceededTotalForTest() *prometheus.CounterVec {
	return turnBudgetExceededTotal
}

// --- Sub-KB router (AP-A4) ------------------------------------------------

var (
	kbRouterDecisionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_kb_router_decision_total",
			Help: "Per-outcome counter for AP-A4 sub-KB router decisions. " +
				"outcome ∈ {single, fanout, fallback_all, llm_error, " +
				"skipped_disabled, skipped_no_auto, skipped_no_candidates}.",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)

	kbRouterSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_kb_router_seconds",
			Help:        "Wall-time of one AP-A4 KB-router LLM call.",
			Buckets:     []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5},
			ConstLabels: commonLabels,
		},
	)
)

// RecordKBRouterDecision increments the per-outcome counter.
func RecordKBRouterDecision(outcome string) {
	switch outcome {
	case "single", "fanout", "fallback_all", "llm_error",
		"skipped_disabled", "skipped_no_auto", "skipped_no_candidates":
	default:
		outcome = "llm_error"
	}
	kbRouterDecisionTotal.WithLabelValues(outcome).Inc()
}

// ObserveKBRouterSeconds records the LLM-call latency.
func ObserveKBRouterSeconds(seconds float64) {
	kbRouterSeconds.Observe(seconds)
}

// KBRouterDecisionTotalForTest exposes the counter for test assertions.
func KBRouterDecisionTotalForTest() *prometheus.CounterVec {
	return kbRouterDecisionTotal
}

// --- Tool-aware planner (AP-B3) -------------------------------------------

var planDAGToolDistributionTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_plan_dag_tool_distribution_total",
		Help: "Per-tool counter for AP-B3 tool-aware DAG planner picks. " +
			"Empty / unknown tool labels normalize to 'kb_search' so the " +
			"legacy non-tool-aware planner contributes to the kb_search " +
			"bucket — without this, dashboards comparing tool-aware vs. " +
			"legacy traffic would have phantom unlabeled rows.",
		ConstLabels: commonLabels,
	},
	[]string{"tool"},
)

// RecordPlanDAGToolDistribution increments the per-tool counter
// when ExecuteDAG dispatches a node. Empty input normalizes to
// "kb_search".
func RecordPlanDAGToolDistribution(tool string) {
	if tool == "" {
		tool = "kb_search"
	}
	planDAGToolDistributionTotal.WithLabelValues(tool).Inc()
}

// PlanDAGToolDistributionTotalForTest exposes the counter for test
// assertions.
func PlanDAGToolDistributionTotalForTest() *prometheus.CounterVec {
	return planDAGToolDistributionTotal
}

// --- KG extraction (AP-C1) ------------------------------------------------

var (
	kgExtractionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_kg_extraction_total",
			Help: "Per-result counter for the AP-C1 KG extractor. " +
				"Result values: ok (entities or relations produced), " +
				"empty (chunk had no named entities), error (LLM call " +
				"or parse failure).",
			ConstLabels: commonLabels,
		},
		[]string{"result"},
	)

	kgExtractionSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_kg_extraction_seconds",
			Help:        "Wall-time of one KG extractor LLM call.",
			Buckets:     []float64{0.25, 0.5, 1, 2, 5, 10, 20},
			ConstLabels: commonLabels,
		},
	)

	kgExtractionRelationDroppedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "rag_kg_extraction_relation_dropped_total",
			Help: "Counter for relations the AP-C1 extractor emitted that " +
				"reference an entity not in the per-chunk entity list. " +
				"Spikes here indicate the model is hallucinating cross-" +
				"chunk edges and the prompt may need tightening.",
			ConstLabels: commonLabels,
		},
	)

	kgDisambiguationTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_kg_disambiguation_total",
			Help: "Per-outcome counter for the AP-C1 entity dedupe path. " +
				"Outcome values: dedupe (existing entity matched on " +
				"canonical_name+type, aliases possibly extended), " +
				"create (new entity row inserted), merge (forward-compat " +
				"for embedding-similarity merge; not yet implemented in " +
				"the C1 cut so always 0).",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)
)

// RecordKGExtraction increments the per-result counter. Empty input
// normalizes to "error" so caller-side typos surface visibly.
func RecordKGExtraction(result string) {
	switch result {
	case "ok", "empty", "error":
	default:
		result = "error"
	}
	kgExtractionTotal.WithLabelValues(result).Inc()
}

// ObserveKGExtractionSeconds records one extractor LLM-call latency.
func ObserveKGExtractionSeconds(seconds float64) {
	kgExtractionSeconds.Observe(seconds)
}

// RecordKGExtractionRelationDropped counts relations the extractor
// dropped because they referenced a non-listed entity.
func RecordKGExtractionRelationDropped() {
	kgExtractionRelationDroppedTotal.Inc()
}

// RecordKGDisambiguation increments the dedupe-outcome counter.
// Empty input normalizes to "create".
func RecordKGDisambiguation(outcome string) {
	switch outcome {
	case "dedupe", "create", "merge":
	default:
		outcome = "create"
	}
	kgDisambiguationTotal.WithLabelValues(outcome).Inc()
}

// KGExtractionTotalForTest exposes the counter for test assertions.
func KGExtractionTotalForTest() *prometheus.CounterVec {
	return kgExtractionTotal
}

// KGDisambiguationTotalForTest exposes the counter for test
// assertions.
func KGDisambiguationTotalForTest() *prometheus.CounterVec {
	return kgDisambiguationTotal
}

// --- KG read-side (AP-C3 / AP-C4) ----------------------------------------

var (
	kgLookupSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_kg_lookup_seconds",
			Help:        "Wall-time of one kg.Store entity / subgraph lookup.",
			Buckets:     []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25},
			ConstLabels: commonLabels,
		},
	)

	graphTraversalTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_graph_traversal_total",
			Help: "Per-outcome counter for the AP-C4 graph-routing heuristic. " +
				"Outcome values: fired (NeedsGraphTraversal returned true and " +
				"the subgraph was fetched), skipped_no_entity (heuristic ran " +
				"but no aliases matched), skipped_disabled (gate off), " +
				"skipped_route (query type wasn't complex_reasoning), " +
				"db_error (kg lookup failed; pipeline proceeded without graph " +
				"chunks).",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)

	graphSubgraphSize = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_graph_traversal_subgraph_size",
			Help:        "Distribution of subgraph sizes (entities + edges) returned by graph_search dispatches that fired.",
			Buckets:     []float64{1, 5, 10, 25, 50, 100, 250},
			ConstLabels: commonLabels,
		},
	)

	graphRoutingChunksInjected = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_graph_routing_chunks_injected_total",
			Help: "Per-outcome counter for the AP-C4 graph-routing chunk-injection " +
				"path (chat_graph_routing_inject_chunks). Outcome values: " +
				"fetched (subgraph chunks were resolved and folded into the " +
				"RRF pool — value is incremented once per turn that injected, " +
				"not per chunk), skipped_disabled (the inject_chunks sub-flag " +
				"was off), no_chunks (router fired but the resolved subgraph " +
				"had zero chunk_ids), fetch_failed (DB fetch of the resolved " +
				"chunk IDs errored — pipeline proceeded without injection).",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)

	graphRoutingChunksInjectedSize = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_graph_routing_chunks_injected_size",
			Help:        "Number of chunks actually folded into RRF per turn that injected (post-cap).",
			Buckets:     []float64{1, 3, 5, 10, 15, 25, 50},
			ConstLabels: commonLabels,
		},
	)
)

// ObserveKGLookupSeconds records one kg.Store lookup duration.
func ObserveKGLookupSeconds(seconds float64) {
	kgLookupSeconds.Observe(seconds)
}

// --- KG Personalized PageRank (T1-4) ---------------------------------------

var kgPPRSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_kg_ppr_seconds",
		Help:        "Wall-time of a Personalized PageRank traversal over a KB's kg_edges, including the edge load and the in-memory iteration.",
		Buckets:     []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		ConstLabels: commonLabels,
	},
)

var kgPPRConvergedIter = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_kg_ppr_converged_iterations",
		Help:        "Iteration count at which PPR's L1 score delta dropped below the convergence threshold. Runs that hit MaxIter without converging are NOT recorded here (the value would be misleading); their cost shows up only in rag_kg_ppr_seconds.",
		Buckets:     []float64{1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 200},
		ConstLabels: commonLabels,
	},
)

// ObserveKGPPRSeconds records the wall-time of one PPR call.
func ObserveKGPPRSeconds(seconds float64) {
	kgPPRSeconds.Observe(seconds)
}

// RecordKGPPRConverged records the iteration count at which PPR
// declared convergence. Callers that exit without converging (hit
// MaxIter) should NOT call this — the metric is for healthy runs.
func RecordKGPPRConverged(iterations int) {
	kgPPRConvergedIter.Observe(float64(iterations))
}

// --- KG relational paths (T1-5) -------------------------------------------

var kgPathsSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_kg_paths_seconds",
		Help:        "Wall-time of PathRAG-style relational path enumeration (FindRelationalPaths) including the edge load and BFS.",
		Buckets:     []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		ConstLabels: commonLabels,
	},
)

var kgPathsFoundCount = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_kg_paths_found",
		Help:        "Number of paths returned by one FindRelationalPaths call. 0 means src and dst are disconnected within maxPathLen — common on sparse graphs.",
		Buckets:     []float64{0, 1, 2, 3, 5, 8, 13, 21, 50},
		ConstLabels: commonLabels,
	},
)

// ObserveKGPathsSeconds records the wall-time of one path search.
func ObserveKGPathsSeconds(seconds float64) {
	kgPathsSeconds.Observe(seconds)
}

// RecordKGPathsFound records how many paths a search returned.
func RecordKGPathsFound(n int) {
	kgPathsFoundCount.Observe(float64(n))
}

// RecordGraphTraversal increments the per-outcome counter.
func RecordGraphTraversal(outcome string) {
	switch outcome {
	case "fired", "skipped_no_entity", "skipped_disabled", "skipped_route", "db_error":
	default:
		outcome = "db_error"
	}
	graphTraversalTotal.WithLabelValues(outcome).Inc()
}

// ObserveGraphSubgraphSize records the (entities + edges) size of
// one fired subgraph.
func ObserveGraphSubgraphSize(size int) {
	graphSubgraphSize.Observe(float64(size))
}

// GraphTraversalTotalForTest exposes the counter for test assertions.
func GraphTraversalTotalForTest() *prometheus.CounterVec {
	return graphTraversalTotal
}

// RecordGraphRoutingChunksInjected increments the AP-C4 chunk-
// injection per-outcome counter. When outcome=="fetched", also
// records the chunk count via the size histogram so operators can
// see the distribution (post-cap).
func RecordGraphRoutingChunksInjected(outcome string, count int) {
	switch outcome {
	case "fetched", "skipped_disabled", "no_chunks", "fetch_failed":
	default:
		outcome = "fetch_failed"
	}
	graphRoutingChunksInjected.WithLabelValues(outcome).Inc()
	if outcome == "fetched" && count > 0 {
		graphRoutingChunksInjectedSize.Observe(float64(count))
	}
}

// GraphRoutingChunksInjectedForTest exposes the counter for test assertions.
func GraphRoutingChunksInjectedForTest() *prometheus.CounterVec {
	return graphRoutingChunksInjected
}

// --- Community-primed global search ---------------------------------------

var communityPrimerTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "rag_community_primer_total",
		Help: "Community summaries injected into the answer pool for " +
			"global-synthesis queries. Outcome values: injected (≥1 " +
			"community summary was folded into the pool — value is the " +
			"number of summaries added), empty (the primer fired but no " +
			"community summaries were available to inject).",
		ConstLabels: commonLabels,
	},
	[]string{"outcome"},
)

// RecordCommunityPrimer records a community-primer injection outcome
// ("injected"/"empty"), adding n to the per-outcome counter.
func RecordCommunityPrimer(outcome string, n int) {
	communityPrimerTotal.WithLabelValues(outcome).Add(float64(n))
}

// --- Long-term memory (AP-D1) --------------------------------------------

var (
	longmemExtractTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_longmem_extract_total",
			Help: "Per-result counter for AP-D1 salient-fact extraction. " +
				"Result values: ok (≥1 fact passed normalisation), empty " +
				"(LLM said nothing salient OR the turn was empty), error " +
				"(LLM call or parse failure).",
			ConstLabels: commonLabels,
		},
		[]string{"result"},
	)

	longmemExtractSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_longmem_extract_seconds",
			Help:        "Wall-time of one salient-fact extractor LLM call.",
			Buckets:     []float64{0.1, 0.25, 0.5, 1, 2, 5, 10},
			ConstLabels: commonLabels,
		},
	)

	longmemWriteTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_longmem_write_total",
			Help: "Per-action counter for the AP-D1 longmem.Store write path. " +
				"Action values: create (new memory row inserted), " +
				"skip_low_salience (extractor produced a fact below " +
				"chat_longmem_min_salience), error (DB error).",
			ConstLabels: commonLabels,
		},
		[]string{"action"},
	)

	longmemRecallTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_longmem_recall_total",
			Help: "Per-result counter for the AP-D1 longmem.Recall read path. " +
				"`used` reports whether the recalled facts were actually " +
				"prepended to the system prompt: '0' (recall ran but produced " +
				"zero rows), '1' (1 fact prepended), '>1' (multiple).",
			ConstLabels: commonLabels,
		},
		[]string{"used"},
	)
)

// RecordLongmemExtract increments the per-result counter.
func RecordLongmemExtract(result string) {
	switch result {
	case "ok", "empty", "error":
	default:
		result = "error"
	}
	longmemExtractTotal.WithLabelValues(result).Inc()
}

// ObserveLongmemExtractSeconds records one extractor LLM-call duration.
func ObserveLongmemExtractSeconds(seconds float64) {
	longmemExtractSeconds.Observe(seconds)
}

// RecordLongmemWrite increments the per-action counter.
func RecordLongmemWrite(action string) {
	switch action {
	case "create", "skip_low_salience", "error":
	default:
		action = "error"
	}
	longmemWriteTotal.WithLabelValues(action).Inc()
}

// RecordLongmemRecall increments the per-result counter, bucketing
// the recalled-fact count.
func RecordLongmemRecall(count int) {
	bucket := "0"
	switch {
	case count == 1:
		bucket = "1"
	case count > 1:
		bucket = ">1"
	}
	longmemRecallTotal.WithLabelValues(bucket).Inc()
}

// LongmemExtractTotalForTest exposes the counter for test assertions.
func LongmemExtractTotalForTest() *prometheus.CounterVec {
	return longmemExtractTotal
}

// LongmemWriteTotalForTest exposes the counter for test assertions.
func LongmemWriteTotalForTest() *prometheus.CounterVec {
	return longmemWriteTotal
}

// --- Self-RAG (AP-D2) ----------------------------------------------------

var (
	selfRAGTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_self_rag_total",
			Help: "Per-result counter for the AP-D2 unified Self-RAG verifier. " +
				"Result values: clean (no flags on any axis), " +
				"flagged_issup (≥1 unsupported / contradicted claim — feeds " +
				"the AP-A1 refine gate), flagged_isuse_no (answer doesn't " +
				"address the question — triggers a holistic refine), " +
				"flagged_isrel (sources marked as not-fully-relevant — " +
				"diagnostic only in the v1 cut), error (LLM call or parse " +
				"failure), skipped_no_citation_warn (cost gate: validator " +
				"did not raise a suspect citation).",
			ConstLabels: commonLabels,
		},
		[]string{"result"},
	)

	selfRAGSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_self_rag_seconds",
			Help:        "Wall-time of one Self-RAG verifier LLM call.",
			Buckets:     []float64{0.1, 0.25, 0.5, 1, 2, 5, 10},
			ConstLabels: commonLabels,
		},
	)
)

// RecordSelfRAG increments the per-result counter. Empty input
// normalizes to "error" so caller-side typos surface visibly.
func RecordSelfRAG(result string) {
	switch result {
	case "clean", "flagged_issup", "flagged_isuse_no", "flagged_isrel",
		"error", "skipped_no_citation_warn":
	default:
		result = "error"
	}
	selfRAGTotal.WithLabelValues(result).Inc()
}

// ObserveSelfRAGSeconds records one Self-RAG LLM-call duration.
func ObserveSelfRAGSeconds(seconds float64) {
	selfRAGSeconds.Observe(seconds)
}

// SelfRAGTotalForTest exposes the counter for test assertions.
func SelfRAGTotalForTest() *prometheus.CounterVec {
	return selfRAGTotal
}

// --- Iterative DAG critic (AP-D3) ----------------------------------------

var (
	critiqueDAGActionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rag_plan_dag_iterative_action_total",
			Help: "Per-action counter for AP-D3 inter-level DAG critic decisions. " +
				"Action values: continue (no-op, the default), add_nodes (new " +
				"sub-queries appended), prune_nodes (pending nodes dropped), " +
				"answer_now (executor stops, synthesis starts), error (LLM " +
				"call or parse failure — treated as continue), cap_violation " +
				"(add_nodes / prune_nodes rejected by width / depth cap).",
			ConstLabels: commonLabels,
		},
		[]string{"action"},
	)

	critiqueDAGSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_plan_dag_iterative_seconds",
			Help:        "Wall-time of one AP-D3 critic LLM call.",
			Buckets:     []float64{0.1, 0.25, 0.5, 1, 2, 5},
			ConstLabels: commonLabels,
		},
	)

	critiqueDAGAddedNodesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name:        "rag_plan_dag_added_nodes_total",
			Help:        "Cumulative count of nodes added by the AP-D3 critic across all turns.",
			ConstLabels: commonLabels,
		},
	)

	critiqueDAGPrunedNodesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name:        "rag_plan_dag_pruned_nodes_total",
			Help:        "Cumulative count of nodes pruned by the AP-D3 critic across all turns.",
			ConstLabels: commonLabels,
		},
	)
)

// RecordCritiqueDAGAction increments the per-action counter.
// Empty / unknown values normalize to "error".
func RecordCritiqueDAGAction(action string) {
	switch action {
	case "continue", "add_nodes", "prune_nodes", "answer_now", "error", "cap_violation":
	default:
		action = "error"
	}
	critiqueDAGActionTotal.WithLabelValues(action).Inc()
}

// ObserveCritiqueDAGSeconds records one critic LLM-call duration.
func ObserveCritiqueDAGSeconds(seconds float64) {
	critiqueDAGSeconds.Observe(seconds)
}

// AddCritiqueDAGNodesAdded bumps the cumulative added-nodes counter.
func AddCritiqueDAGNodesAdded(n int) {
	if n <= 0 {
		return
	}
	critiqueDAGAddedNodesTotal.Add(float64(n))
}

// AddCritiqueDAGNodesPruned bumps the cumulative pruned-nodes counter.
func AddCritiqueDAGNodesPruned(n int) {
	if n <= 0 {
		return
	}
	critiqueDAGPrunedNodesTotal.Add(float64(n))
}

// CritiqueDAGActionTotalForTest exposes the counter for test
// assertions.
func CritiqueDAGActionTotalForTest() *prometheus.CounterVec {
	return critiqueDAGActionTotal
}

// --- Online faithfulness (AP-E2) -----------------------------------------

var onlineFaithfulness = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "rag_online_faithfulness",
		Help: "Per-turn faithfulness approximation derived from the AP-A1 " +
			"factuality verifier or AP-D2 Self-RAG output. NOTE: this is " +
			"a subtractive proxy, not the strict Ragas definition (count_" +
			"supported_claims / count_total_claims) — neither verifier " +
			"surfaces the total claim count, only the flagged subset. " +
			"Score: 1.0 - 0.2*flagged_count - 0.5*(isuse=='no') - 0.2*" +
			"(isuse=='partial'), clamped to [0, 1]. Use for trend / " +
			"alerting (Grafana p50 < 0.85 over 10-min window catches " +
			"both gradual-flag-creep and ISUSE failures).",
		Buckets:     []float64{0.0, 0.5, 0.7, 0.8, 0.85, 0.9, 0.95, 1.0},
		ConstLabels: commonLabels,
	},
	[]string{"kb_id"},
)

// onlineFaithfulnessMaxKBs caps the number of distinct kb_id label values
// the histogram will ever emit. Production deployments rarely exceed
// hundreds of KBs, but nothing upstream enforces that — a runaway KB
// creator (scripted API use, a misbehaving test tenant) would otherwise
// grow Prometheus series without bound on the one metric in this package
// that doesn't use a closed-enum label. KBs observed after the cap fold
// into the "overflow" label; per-KB resolution for those lives in
// structured logs as usual.
const onlineFaithfulnessMaxKBs = 500

var (
	onlineFaithfulnessKBSeen  sync.Map // kb_id → struct{}
	onlineFaithfulnessKBCount atomic.Int64
)

// ObserveOnlineFaithfulness records one per-turn faithfulness
// observation. score is clamped to [0, 1] defensively. kbID is emitted
// as-is up to onlineFaithfulnessMaxKBs distinct values, then folds into
// "overflow". The Load-then-LoadOrStore pair is best-effort: concurrent
// first observations of different KBs can overshoot the cap by a few
// series, which is fine — the cap guards against unbounded growth, not
// an exact census.
func ObserveOnlineFaithfulness(kbID string, score float64) {
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	if _, seen := onlineFaithfulnessKBSeen.Load(kbID); !seen {
		if onlineFaithfulnessKBCount.Load() >= onlineFaithfulnessMaxKBs {
			kbID = "overflow"
		} else if _, loaded := onlineFaithfulnessKBSeen.LoadOrStore(kbID, struct{}{}); !loaded {
			onlineFaithfulnessKBCount.Add(1)
		}
	}
	onlineFaithfulness.WithLabelValues(kbID).Observe(score)
}

// OnlineFaithfulnessForTest exposes the histogram for test
// assertions.
func OnlineFaithfulnessForTest() *prometheus.HistogramVec {
	return onlineFaithfulness
}

// --- RAPTOR -----------------------------------------------------------------

var (
	raptorBuildTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_raptor_build_total",
			Help:        "Phase F RAPTOR tree-build outcomes per file. Labels: built / skipped_min_chunks / skipped_parent_child / failed.",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)
	raptorBuildSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_raptor_build_seconds",
			Help:        "Phase F RAPTOR build wall-time per file (seconds). Dominated by per-cluster LLM summary latency.",
			Buckets:     []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
			ConstLabels: commonLabels,
		},
	)
	raptorTreeDepth = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_raptor_tree_depth",
			Help:        "Number of levels built per Phase F RAPTOR tree. 0 = skipped.",
			Buckets:     []float64{0, 1, 2, 3, 4, 5, 6, 7, 8},
			ConstLabels: commonLabels,
		},
	)
	raptorLLMCalls = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_raptor_llm_calls_total",
			Help:        "Phase F RAPTOR summary-LLM call count, labeled by result (ok / error).",
			ConstLabels: commonLabels,
		},
		[]string{"result"},
	)
	raptorRetrievedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_raptor_retrieved_total",
			Help:        "Chunks surfaced to chat from retrieval, split by node_kind (leaf / summary). Quick read on whether summaries are competing for attention in the final pool.",
			ConstLabels: commonLabels,
		},
		[]string{"node_kind"},
	)
	raptorCitedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_raptor_cited_total",
			Help:        "Chunks actually cited in answers (verified by the citation validator), split by node_kind.",
			ConstLabels: commonLabels,
		},
		[]string{"node_kind"},
	)
)

// RecordRaptorBuild increments the per-outcome counter once per file ingest.
func RecordRaptorBuild(outcome string) { raptorBuildTotal.WithLabelValues(outcome).Inc() }

// ObserveRaptorBuildSeconds records the wall-time of one tree build.
func ObserveRaptorBuildSeconds(seconds float64) { raptorBuildSeconds.Observe(seconds) }

// ObserveRaptorTreeDepth records the depth of one finished tree.
func ObserveRaptorTreeDepth(levels int) { raptorTreeDepth.Observe(float64(levels)) }

// RecordRaptorLLMCall increments the summary-LLM result counter.
// result is "ok" or "error".
func RecordRaptorLLMCall(result string) { raptorLLMCalls.WithLabelValues(result).Inc() }

// RecordRaptorRetrieved increments the retrieved-by-kind counter.
// Empty / unknown nodeKind normalises to "leaf" so old code paths
// that don't carry the column still produce a coherent label set.
func RecordRaptorRetrieved(nodeKind string) {
	if nodeKind == "" {
		nodeKind = "leaf"
	}
	raptorRetrievedTotal.WithLabelValues(nodeKind).Inc()
}

// RecordRaptorCited increments the cited-by-kind counter.
func RecordRaptorCited(nodeKind string) {
	if nodeKind == "" {
		nodeKind = "leaf"
	}
	raptorCitedTotal.WithLabelValues(nodeKind).Inc()
}

// --- Answer-time tool loop (answer_tools.go) ---------------------------------

var answerToolLoopRounds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_answer_tool_loop_rounds",
		Help:        "Number of answer-time tool-call rounds consumed per chat turn (0 when no tool call happened, capped by chat_answer_tools_max_rounds).",
		Buckets:     []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		ConstLabels: commonLabels,
	},
)

var answerToolLoopExhausted = promauto.NewCounter(
	prometheus.CounterOpts{
		Name:        "rag_answer_tool_loop_exhausted_total",
		Help:        "Number of chat turns where the answer-time tool loop hit MaxRounds and forced a no-tool finish.",
		ConstLabels: commonLabels,
	},
)

// RecordAnswerToolLoopRounds observes one chat turn's tool-loop length.
// Pass 0 when the answer was produced without any tool call.
func RecordAnswerToolLoopRounds(rounds int) {
	answerToolLoopRounds.Observe(float64(rounds))
}

// RecordAnswerToolLoopExhausted increments once per turn that hit the
// MaxRounds cap and had to force a no-tool finish.
func RecordAnswerToolLoopExhausted() {
	answerToolLoopExhausted.Inc()
}

// --- Corpus-comparison table ------------------------------------------------

var corpusTableTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name:        "rag_corpus_table_total",
		Help:        "Corpus-comparison-table turns started.",
		ConstLabels: commonLabels,
	},
)

var corpusTableFiles = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:        "rag_corpus_table_files",
		Help:        "Files processed per corpus-table turn.",
		Buckets:     []float64{1, 5, 10, 20, 30, 50, 100},
		ConstLabels: commonLabels,
	},
)

// RecordCorpusTableStart increments the corpus-table turn counter.
func RecordCorpusTableStart() {
	corpusTableTotal.Inc()
}

// RecordCorpusTableComplete observes the number of files processed in a
// completed corpus-table turn.
func RecordCorpusTableComplete(n int) {
	corpusTableFiles.Observe(float64(n))
}

// --- Multi-pass extraction (G5 / chat/multipass.go) -------------------------

var (
	multipassCallTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "rag_multipass_call_total",
			Help:        "Per-chunk multi-pass extraction LLM-call outcomes. Labels: kept (chunk surfaced relevant facts) / dropped (LLM emitted NONE) / error (LLM call failed, chunk passed through unchanged).",
			ConstLabels: commonLabels,
		},
		[]string{"outcome"},
	)
	multipassTurnSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_multipass_turn_seconds",
			Help:        "Wall-time of one multi-pass extraction pre-pass over an entire chunk slice. Dominated by per-chunk fast-tier LLM latency, parallelised at concurrency=5.",
			Buckets:     []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
			ConstLabels: commonLabels,
		},
	)
	multipassDropRate = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:        "rag_multipass_drop_rate",
			Help:        "Fraction of chunks dropped by the multi-pass pre-pass per turn (0.0-1.0). Persistently >0.7 suggests retrieval is over-recalling for the query type and the pre-pass is masking it; persistently 0.0 suggests the pre-pass is not earning its latency.",
			Buckets:     []float64{0.0, 0.1, 0.25, 0.5, 0.75, 0.9, 1.0},
			ConstLabels: commonLabels,
		},
	)
)

// RecordMultipassCall increments the per-chunk call counter.
// outcome is "kept", "dropped", or "error".
func RecordMultipassCall(outcome string) {
	multipassCallTotal.WithLabelValues(outcome).Inc()
}

// ObserveMultipassTurnSeconds records the wall-time of one extraction pre-pass.
func ObserveMultipassTurnSeconds(seconds float64) {
	multipassTurnSeconds.Observe(seconds)
}

// ObserveMultipassDropRate records the fraction of chunks dropped by one pre-pass.
func ObserveMultipassDropRate(rate float64) {
	multipassDropRate.Observe(rate)
}
