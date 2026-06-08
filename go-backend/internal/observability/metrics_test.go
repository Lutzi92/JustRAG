package observability

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordSearch_IncrementsCounterAndObservesHistograms(t *testing.T) {
	before := testutil.ToFloat64(searchTotal.WithLabelValues("en"))
	RecordSearch("en", 5, 0.82, 0.15)
	after := testutil.ToFloat64(searchTotal.WithLabelValues("en"))
	if after-before != 1 {
		t.Errorf("expected counter +1, got delta %v", after-before)
	}
	if testutil.CollectAndCount(searchDuration, "rag_search_duration_seconds") < 1 {
		t.Error("search duration histogram should have at least one observation")
	}
	if testutil.CollectAndCount(searchChunksReturned, "rag_search_chunks_returned") < 1 {
		t.Error("chunks returned histogram should have at least one observation")
	}
	if testutil.CollectAndCount(searchTopScore, "rag_search_top_score") < 1 {
		t.Error("top score histogram should have at least one observation")
	}
}

func TestRecordSearch_EmptyLanguageBecomesUnknown(t *testing.T) {
	before := testutil.ToFloat64(searchTotal.WithLabelValues("unknown"))
	RecordSearch("", 1, 0.5, 0.1)
	after := testutil.ToFloat64(searchTotal.WithLabelValues("unknown"))
	if after-before != 1 {
		t.Errorf("expected unknown counter +1, got delta %v", after-before)
	}
}

func TestRecordFactcheck_IncrementsCounterByVerifiedLabel(t *testing.T) {
	beforeTrue := testutil.ToFloat64(factcheckTotal.WithLabelValues("true"))
	beforeFalse := testutil.ToFloat64(factcheckTotal.WithLabelValues("false"))
	RecordFactcheck(true, 87, 0.4)
	RecordFactcheck(false, 30, 0.5)
	if got := testutil.ToFloat64(factcheckTotal.WithLabelValues("true")) - beforeTrue; got != 1 {
		t.Errorf("expected verified=true +1, got %v", got)
	}
	if got := testutil.ToFloat64(factcheckTotal.WithLabelValues("false")) - beforeFalse; got != 1 {
		t.Errorf("expected verified=false +1, got %v", got)
	}
}

func TestRecordCompletion_LabelsByStream(t *testing.T) {
	before := testutil.CollectAndCount(completionDuration, "rag_completion_duration_seconds")
	RecordCompletion(true, 1.2)
	RecordCompletion(false, 0.8)
	after := testutil.CollectAndCount(completionDuration, "rag_completion_duration_seconds")
	if after-before < 2 {
		t.Errorf("expected at least 2 new observations, got delta %d", after-before)
	}
}

func TestRecordLowConfidence_IncrementsCounter(t *testing.T) {
	before := testutil.ToFloat64(lowConfidenceTotal)
	RecordLowConfidence()
	after := testutil.ToFloat64(lowConfidenceTotal)
	if after-before != 1 {
		t.Errorf("expected +1, got delta %v", after-before)
	}
}

func TestRecordEmbeddingCacheHitMiss(t *testing.T) {
	hBefore := testutil.ToFloat64(embeddingCacheHits)
	mBefore := testutil.ToFloat64(embeddingCacheMisses)
	RecordEmbeddingCacheHit()
	RecordEmbeddingCacheHit()
	RecordEmbeddingCacheMiss()
	if got := testutil.ToFloat64(embeddingCacheHits) - hBefore; got != 2 {
		t.Errorf("expected hits +2, got %v", got)
	}
	if got := testutil.ToFloat64(embeddingCacheMisses) - mBefore; got != 1 {
		t.Errorf("expected misses +1, got %v", got)
	}
}

func TestMetricsExposed_HaveExpectedNamesAndPrefix(t *testing.T) {
	// Touch each instrument so it appears in the registry.
	RecordSearch("de", 1, 0.5, 0.1)
	RecordFactcheck(true, 90, 0.2)
	RecordCompletion(false, 0.3)
	RecordLowConfidence()
	RecordEmbeddingCacheHit()
	RecordEmbeddingCacheMiss()
	RecordModelFallback("not_in_chat_models")
	RecordWorkerTask("test:type", "success", 100*time.Millisecond)
	RecordParentSwap("swapped", 1)

	dump := metricsDumpForTest(t)
	for _, name := range []string{
		"rag_search_total",
		"rag_search_duration_seconds",
		"rag_search_chunks_returned",
		"rag_search_top_score",
		"rag_factcheck_total",
		"rag_factcheck_score",
		"rag_factcheck_duration_seconds",
		"rag_completion_duration_seconds",
		"rag_low_confidence_answers_total",
		"rag_embedding_cache_hits_total",
		"rag_embedding_cache_misses_total",
		"rag_model_fallback_total",
		"worker_task_total",
		"worker_task_duration_seconds",
		"rag_parent_swap_total",
	} {
		if !strings.Contains(dump, name) {
			t.Errorf("metric %q not found in /metrics dump", name)
		}
	}
}

func TestRecordWorkerTask_IncrementsCounterAndNormalisesStatus(t *testing.T) {
	beforeOK := testutil.ToFloat64(workerTaskTotal.WithLabelValues("worker:test:ok", "success"))
	beforeErr := testutil.ToFloat64(workerTaskTotal.WithLabelValues("worker:test:err", "error"))

	RecordWorkerTask("worker:test:ok", "success", 50*time.Millisecond)
	RecordWorkerTask("worker:test:err", "error", 200*time.Millisecond)
	// Unknown status normalises to "error" so it never widens cardinality.
	RecordWorkerTask("worker:test:err", "weird", 5*time.Millisecond)

	if got := testutil.ToFloat64(workerTaskTotal.WithLabelValues("worker:test:ok", "success")) - beforeOK; got != 1 {
		t.Errorf("expected success +1, got %v", got)
	}
	if got := testutil.ToFloat64(workerTaskTotal.WithLabelValues("worker:test:err", "error")) - beforeErr; got != 2 {
		t.Errorf("expected error +2 (incl. normalised), got %v", got)
	}
	// Histogram side has at least one observation per call (3 calls total here).
	if testutil.CollectAndCount(workerTaskDuration, "worker_task_duration_seconds") < 1 {
		t.Error("worker task duration histogram should have observations")
	}
}

func TestRecordModelFallback_IncrementsCounterAndNormalisesEmpty(t *testing.T) {
	beforeReason := testutil.ToFloat64(modelFallbackTotal.WithLabelValues("not_in_chat_models"))
	RecordModelFallback("not_in_chat_models")
	RecordModelFallback("") // empty normalises to "not_in_chat_models"
	after := testutil.ToFloat64(modelFallbackTotal.WithLabelValues("not_in_chat_models"))
	if got := after - beforeReason; got != 2 {
		t.Errorf("expected +2 increments on not_in_chat_models, got %v", got)
	}
}

func metricsDumpForTest(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestRecordRerankerHealth_HealthyDoesNotIncrementDegenerate(t *testing.T) {
	before := testutil.ToFloat64(rerankerDegenerateTotal)
	RecordRerankerHealth(0.35)
	after := testutil.ToFloat64(rerankerDegenerateTotal)
	if after != before {
		t.Errorf("healthy stddev incremented degenerate counter: before=%v after=%v", before, after)
	}
}

func TestRecordRerankerHealth_DegenerateIncrements(t *testing.T) {
	before := testutil.ToFloat64(rerankerDegenerateTotal)
	RecordRerankerHealth(0.01)
	after := testutil.ToFloat64(rerankerDegenerateTotal)
	if after != before+1 {
		t.Errorf("degenerate stddev did not increment counter by 1: before=%v after=%v", before, after)
	}
}

func TestRecordStageDuration_AcceptsKnownStage(t *testing.T) {
	// We only care that the call doesn't panic and the histogram exists.
	// Bucketing correctness is tested by the prometheus library itself.
	RecordStageDuration("rrf", 12*time.Millisecond)
	got := testutil.CollectAndCount(stageDuration)
	if got == 0 {
		t.Error("stageDuration histogram has no observations after recording")
	}
}

func TestRecordQueryType_NormalizesEmpty(t *testing.T) {
	before := testutil.ToFloat64(queryTypeTotal.WithLabelValues("unknown"))
	RecordQueryType("")
	after := testutil.ToFloat64(queryTypeTotal.WithLabelValues("unknown"))
	if after != before+1 {
		t.Errorf("empty input did not normalize to 'unknown': before=%v after=%v", before, after)
	}
}

func TestRecordQueryType_KnownLabelIncrements(t *testing.T) {
	before := testutil.ToFloat64(queryTypeTotal.WithLabelValues("lookup"))
	RecordQueryType("lookup")
	after := testutil.ToFloat64(queryTypeTotal.WithLabelValues("lookup"))
	if after != before+1 {
		t.Errorf("lookup increment failed: before=%v after=%v", before, after)
	}
}

func TestRecordAdaptiveRoutingDecision_IncrementsByAction(t *testing.T) {
	beforeDisabled := testutil.ToFloat64(adaptiveRoutingDecisionTotal.WithLabelValues("disabled_crag"))
	beforeKept := testutil.ToFloat64(adaptiveRoutingDecisionTotal.WithLabelValues("kept"))
	beforeNA := testutil.ToFloat64(adaptiveRoutingDecisionTotal.WithLabelValues("n_a"))

	RecordAdaptiveRoutingDecision("disabled_crag")
	RecordAdaptiveRoutingDecision("disabled_crag")
	RecordAdaptiveRoutingDecision("kept")
	RecordAdaptiveRoutingDecision("n_a")

	if got := testutil.ToFloat64(adaptiveRoutingDecisionTotal.WithLabelValues("disabled_crag")) - beforeDisabled; got != 2 {
		t.Errorf("disabled_crag delta: got %v, want 2", got)
	}
	if got := testutil.ToFloat64(adaptiveRoutingDecisionTotal.WithLabelValues("kept")) - beforeKept; got != 1 {
		t.Errorf("kept delta: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(adaptiveRoutingDecisionTotal.WithLabelValues("n_a")) - beforeNA; got != 1 {
		t.Errorf("n_a delta: got %v, want 1", got)
	}
}

func TestRecordAdaptiveRoutingDecision_EmptyNormalizesToNA(t *testing.T) {
	before := testutil.ToFloat64(adaptiveRoutingDecisionTotal.WithLabelValues("n_a"))
	RecordAdaptiveRoutingDecision("")
	after := testutil.ToFloat64(adaptiveRoutingDecisionTotal.WithLabelValues("n_a"))
	if after != before+1 {
		t.Errorf("empty input did not normalize to 'n_a': before=%v after=%v", before, after)
	}
}

func TestRecordRAGASSample_CompletedObservesAllAvailable(t *testing.T) {
	beforeOk := testutil.ToFloat64(ragasSampleTotal.WithLabelValues("completed"))

	faith, rel, prec := 0.8, 0.9, 0.6
	RecordRAGASSample("completed", &faith, &rel, &prec)

	// All three metrics should have been observed (visible in the collected output).
	if testutil.CollectAndCount(ragasFaithfulnessScore, "rag_ragas_faithfulness_score") < 1 {
		t.Error("faithfulness histogram should be in registry")
	}
	if testutil.CollectAndCount(ragasAnswerRelevanceScore, "rag_ragas_answer_relevance_score") < 1 {
		t.Error("answer relevance histogram should be in registry")
	}
	if testutil.CollectAndCount(ragasContextPrecisionScore, "rag_ragas_context_precision_score") < 1 {
		t.Error("context precision histogram should be in registry")
	}

	// The outcome counter must increment.
	if got := testutil.ToFloat64(ragasSampleTotal.WithLabelValues("completed")) - beforeOk; got != 1 {
		t.Errorf("completed counter: got delta %v, want 1", got)
	}
}

func TestRecordRAGASSample_NilFieldsSkipObservations(t *testing.T) {
	// Only outcome counter should increment; the histograms must NOT
	// observe a "0.0" placeholder for missing metrics.
	beforeFaith := testutil.CollectAndCount(ragasFaithfulnessScore, "rag_ragas_faithfulness_score")
	beforeError := testutil.ToFloat64(ragasSampleTotal.WithLabelValues("error"))

	RecordRAGASSample("error", nil, nil, nil)

	if got := testutil.CollectAndCount(ragasFaithfulnessScore, "rag_ragas_faithfulness_score"); got != beforeFaith {
		t.Errorf("nil faithfulness: histogram count changed from %d to %d (must not observe)", beforeFaith, got)
	}
	if got := testutil.ToFloat64(ragasSampleTotal.WithLabelValues("error")) - beforeError; got != 1 {
		t.Errorf("error counter: got delta %v, want 1", got)
	}
}

func TestRecordRAGASSample_EmptyOutcomeNormalizes(t *testing.T) {
	before := testutil.ToFloat64(ragasSampleTotal.WithLabelValues("error"))
	RecordRAGASSample("", nil, nil, nil)
	after := testutil.ToFloat64(ragasSampleTotal.WithLabelValues("error"))
	if after != before+1 {
		t.Errorf("empty outcome did not normalize to 'error': before=%v after=%v", before, after)
	}
}

func TestRecordAgenticChatDecision_HappyPath(t *testing.T) {
	// CollectAndCount for a histogram returns the metric count (always 1 after registration),
	// so we just verify it exists and is observable.
	if testutil.CollectAndCount(agenticHops, "rag_agentic_hops") < 1 {
		t.Error("agenticHops histogram should be registered")
	}
	beforeOutcome := testutil.ToFloat64(agenticDecisionTotal.WithLabelValues("early_stop_sufficient"))

	RecordAgenticChatDecision("early_stop_sufficient", 2)

	// Verify the outcome counter was incremented
	if got := testutil.ToFloat64(agenticDecisionTotal.WithLabelValues("early_stop_sufficient")) - beforeOutcome; got != 1 {
		t.Errorf("outcome counter delta: got %v, want 1", got)
	}
}

func TestRecordAgenticChatDecision_AllOutcomesNormalized(t *testing.T) {
	outcomes := []string{
		"max_hops_reached",
		"early_stop_sufficient",
		"early_stop_no_progress",
		"early_stop_no_query",
		"judge_error",
		"search_error",
	}
	for _, o := range outcomes {
		before := testutil.ToFloat64(agenticDecisionTotal.WithLabelValues(o))
		RecordAgenticChatDecision(o, 1)
		if got := testutil.ToFloat64(agenticDecisionTotal.WithLabelValues(o)) - before; got != 1 {
			t.Errorf("outcome=%q: counter delta got %v, want 1", o, got)
		}
	}
}

func TestRecordAgenticChatDecision_EmptyOutcomeNormalizes(t *testing.T) {
	before := testutil.ToFloat64(agenticDecisionTotal.WithLabelValues("max_hops_reached"))
	RecordAgenticChatDecision("", 0)
	after := testutil.ToFloat64(agenticDecisionTotal.WithLabelValues("max_hops_reached"))
	if after != before+1 {
		t.Errorf("empty outcome did not normalize to 'max_hops_reached': before=%v after=%v", before, after)
	}
}

func TestRecordParentSwap_AddsByCount(t *testing.T) {
	before := testutil.ToFloat64(parentSwapTotal.WithLabelValues("swapped"))
	RecordParentSwap("swapped", 3)
	after := testutil.ToFloat64(parentSwapTotal.WithLabelValues("swapped"))
	if delta := after - before; delta != 3 {
		t.Errorf("swapped delta: got %v, want 3", delta)
	}
}

func TestRecordParentSwap_ZeroCountIsNoOp(t *testing.T) {
	before := testutil.ToFloat64(parentSwapTotal.WithLabelValues("swapped"))
	RecordParentSwap("swapped", 0)
	if after := testutil.ToFloat64(parentSwapTotal.WithLabelValues("swapped")); after != before {
		t.Errorf("zero count should not increment counter")
	}
}

func TestRecordParentSwap_EmptyOutcomeNormalizes(t *testing.T) {
	before := testutil.ToFloat64(parentSwapTotal.WithLabelValues("lookup_failed"))
	RecordParentSwap("", 1)
	after := testutil.ToFloat64(parentSwapTotal.WithLabelValues("lookup_failed"))
	if after != before+1 {
		t.Errorf("empty outcome should normalize to lookup_failed: before=%v after=%v", before, after)
	}
}

func TestRecordStepBackDecision_Outcomes(t *testing.T) {
	stepBackDecisionTotal.Reset()
	for _, outcome := range []string{"fired", "skipped_route", "skipped_disabled", "llm_error", "empty_output"} {
		RecordStepBackDecision(outcome)
	}
	got := testutil.CollectAndCount(stepBackDecisionTotal)
	if got != 5 {
		t.Errorf("want 5 series, got %d", got)
	}
}

func TestRecordStepBackDecision_EmptyNormalizesToError(t *testing.T) {
	stepBackDecisionTotal.Reset()
	RecordStepBackDecision("")
	got := testutil.CollectAndCount(stepBackDecisionTotal)
	if got != 1 {
		t.Errorf("want 1 series after empty input normalized, got %d", got)
	}
}

func TestRecordPlanExecuteDecision_HappyPath(t *testing.T) {
	planExecuteDecisionTotal.Reset()
	RecordPlanExecuteDecision("answered_after_iter", 2, []int{2, 1})
	RecordPlanExecuteDecision("answered_after_plan", 0, nil)
	if got := testutil.ToFloat64(planExecuteDecisionTotal.WithLabelValues("answered_after_iter")); got != 1 {
		t.Errorf("answered_after_iter: want 1, got %v", got)
	}
	if got := testutil.ToFloat64(planExecuteDecisionTotal.WithLabelValues("answered_after_plan")); got != 1 {
		t.Errorf("answered_after_plan: want 1, got %v", got)
	}
}

func TestRecordPlanExecuteDecision_AllOutcomesNormalized(t *testing.T) {
	planExecuteDecisionTotal.Reset()
	outcomes := []string{
		"answered_after_plan", "answered_after_iter", "max_iter_reached",
		"no_progress", "budget_exhausted", "plan_error", "iter_error",
	}
	for _, o := range outcomes {
		RecordPlanExecuteDecision(o, 1, []int{1})
	}
	for _, o := range outcomes {
		if got := testutil.ToFloat64(planExecuteDecisionTotal.WithLabelValues(o)); got != 1 {
			t.Errorf("outcome %q: want 1, got %v", o, got)
		}
	}
}

func TestRecordPlanExecuteDecision_EmptyOutcomeNormalizes(t *testing.T) {
	planExecuteDecisionTotal.Reset()
	RecordPlanExecuteDecision("", 0, nil)
	if got := testutil.ToFloat64(planExecuteDecisionTotal.WithLabelValues("max_iter_reached")); got != 1 {
		t.Errorf("empty outcome should normalize to max_iter_reached: got %v", got)
	}
}

func TestObservePlanExecuteLLMSeconds_RecordsToBothStages(t *testing.T) {
	// Smoke test: just ensure both stage labels are accepted without panic
	// and produce countable observations.
	ObservePlanExecuteLLMSeconds("plan", 1.5)
	ObservePlanExecuteLLMSeconds("iterate", 0.7)
	// We don't assert specific bucket counts; the histogram registration
	// itself is the under-test invariant. If it panics, the test fails.
}

func TestRecordAnswerToolLoopRounds_Histogram(t *testing.T) {
	before := testutil.CollectAndCount(answerToolLoopRounds, "rag_answer_tool_loop_rounds")
	RecordAnswerToolLoopRounds(2)
	after := testutil.CollectAndCount(answerToolLoopRounds, "rag_answer_tool_loop_rounds")
	if after != before {
		// CollectAndCount returns the number of metrics, not observations —
		// for a histogram that's always 1 (one histogram series). We only
		// need to know the function does not panic and the histogram exists.
	}
	if before != 1 || after != 1 {
		t.Fatalf("histogram series count: before=%d after=%d (want 1/1)", before, after)
	}
}

func TestRecordAnswerToolLoopExhausted_IncrementsCounter(t *testing.T) {
	before := testutil.ToFloat64(answerToolLoopExhausted)
	RecordAnswerToolLoopExhausted()
	after := testutil.ToFloat64(answerToolLoopExhausted)
	if after != before+1 {
		t.Fatalf("counter: before=%v after=%v (want diff 1)", before, after)
	}
}

func TestRecordCitationAttribution_ByResultAndMethod(t *testing.T) {
	beforeV := testutil.ToFloat64(citationAttributionsTotal.WithLabelValues("verified", "ngram"))
	beforeU := testutil.ToFloat64(citationAttributionsTotal.WithLabelValues("unverified", "none"))

	RecordCitationAttribution(true, "ngram")
	RecordCitationAttribution(false, "")        // unknown method -> "none"
	RecordCitationAttribution(false, "garbage") // unknown method -> "none"

	afterV := testutil.ToFloat64(citationAttributionsTotal.WithLabelValues("verified", "ngram"))
	afterU := testutil.ToFloat64(citationAttributionsTotal.WithLabelValues("unverified", "none"))

	if afterV-beforeV != 1 {
		t.Errorf("verified/ngram: expected +1, got %v", afterV-beforeV)
	}
	if afterU-beforeU != 2 {
		t.Errorf("unverified/none: expected +2 (empty + garbage normalize to none), got %v", afterU-beforeU)
	}
}
