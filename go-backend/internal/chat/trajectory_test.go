package chat

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/vector"
)

// captureEmits returns a typed slice of every TrajectoryEvent that arrived
// on the SSE callback. It also records the legacy events (where the value
// is anything other than a TrajectoryEvent) so a regression that drops
// the legacy shim is visible.
func captureEmits(events *[]map[string]any) func(map[string]any) {
	return func(d map[string]any) { *events = append(*events, d) }
}

func trajectoryStages(events []map[string]any) []string {
	stages := []string{}
	for _, e := range events {
		if t, ok := e["agentTrajectory"].(TrajectoryEvent); ok {
			stages = append(stages, t.Stage)
		}
	}
	return stages
}

func trajectoryEmittedDelta(stage string, before float64) float64 {
	return testutil.ToFloat64(observability.TrajectoryEventsEmittedTotalForTest().WithLabelValues(stage)) - before
}

func TestRunAgenticChat_EmitsTrajectory(t *testing.T) {
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{
			"q":  {Chunks: []vector.SearchChunk{makeChunk("c1", "f1", "first")}},
			"q2": {Chunks: []vector.SearchChunk{makeChunk("c2", "f2", "second")}},
		},
	}
	critique := &stubCritiqueQueue{responses: []critiqueDecision{
		{sufficient: false, followUp: "q2"},
		{sufficient: true},
	}}
	beforeHop := testutil.ToFloat64(observability.TrajectoryEventsEmittedTotalForTest().WithLabelValues("hop"))
	beforeAnswer := testutil.ToFloat64(observability.TrajectoryEventsEmittedTotalForTest().WithLabelValues("answer"))

	var emits []map[string]any
	params := AgenticChatParams{KbID: "kb", Query: "q", Language: "en", MaxHops: 3}
	if _, err := runAgenticChatTestable(context.Background(), nil, searcher, critique.next, params, captureEmits(&emits)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stages := trajectoryStages(emits)
	wantStages := []string{"hop", "hop", "hop", "answer"}
	if len(stages) != len(wantStages) {
		t.Fatalf("trajectory stages: got %v, want %v", stages, wantStages)
	}
	for i, want := range wantStages {
		if stages[i] != want {
			t.Errorf("stage[%d] = %q, want %q (full: %v)", i, stages[i], want, stages)
		}
	}

	// Legacy shim: the same events must also carry an `agenticHop` /
	// `agenticDecision` key — frontends on the previous release rely on
	// these. A regression here is silent in production unless caught here.
	sawLegacyHop := false
	sawLegacyDecision := false
	for _, e := range emits {
		if _, ok := e["agenticHop"]; ok {
			sawLegacyHop = true
		}
		if _, ok := e["agenticDecision"]; ok {
			sawLegacyDecision = true
		}
	}
	if !sawLegacyHop {
		t.Errorf("legacy `agenticHop` key not emitted — old frontends will see no progress")
	}
	if !sawLegacyDecision {
		t.Errorf("legacy `agenticDecision` key not emitted — old frontends will not see the final outcome")
	}

	if d := trajectoryEmittedDelta("hop", beforeHop); d < 1 {
		t.Errorf("rag_trajectory_events_emitted_total{stage=hop} delta = %v, want >= 1", d)
	}
	if d := trajectoryEmittedDelta("answer", beforeAnswer); d != 1 {
		t.Errorf("rag_trajectory_events_emitted_total{stage=answer} delta = %v, want 1", d)
	}
}

func TestRunPlanExecuteChat_EmitsTrajectory(t *testing.T) {
	plan := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _ string) ([]string, string, error) {
		return []string{"sub1"}, "rationale", nil
	}
	iter := func(_ context.Context, _ *ai.ConfigResolver, _, _, _ string, _ int, _, _ string) (string, []string, string, error) {
		return "answer", nil, "ok", nil
	}
	searcher := &fakeSearcher{
		resp: func(_ string) []vector.SearchChunk { return []vector.SearchChunk{chunk("c1", "f1")} },
	}
	beforePlan := testutil.ToFloat64(observability.TrajectoryEventsEmittedTotalForTest().WithLabelValues("plan"))
	beforeAnswer := testutil.ToFloat64(observability.TrajectoryEventsEmittedTotalForTest().WithLabelValues("answer"))

	var emits []map[string]any
	params := PlanExecuteParams{KbID: "kb", Query: "Q?", Language: "en", MaxSubQueries: 3, MaxIterations: 3, TokenBudget: 8000}
	if _, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter, params, captureEmits(&emits)); err != nil {
		t.Fatalf("err: %v", err)
	}

	stages := trajectoryStages(emits)
	// Plan stage emits twice (starting + queries) — keep the assertion loose
	// on count and strict on what stages MUST appear.
	must := map[string]bool{"plan": false, "iterate": false, "answer": false}
	for _, s := range stages {
		if _, ok := must[s]; ok {
			must[s] = true
		}
	}
	for s, ok := range must {
		if !ok {
			t.Errorf("expected stage %q in trajectory; got %v", s, stages)
		}
	}

	sawLegacyPlan := false
	for _, e := range emits {
		if v, ok := e["planExecuteStage"]; ok && v == "plan" {
			sawLegacyPlan = true
		}
	}
	if !sawLegacyPlan {
		t.Errorf("legacy `planExecuteStage`=plan key not emitted — old frontends will see no plan stage")
	}

	if d := trajectoryEmittedDelta("plan", beforePlan); d < 1 {
		t.Errorf("rag_trajectory_events_emitted_total{stage=plan} delta = %v, want >= 1", d)
	}
	if d := trajectoryEmittedDelta("answer", beforeAnswer); d != 1 {
		t.Errorf("rag_trajectory_events_emitted_total{stage=answer} delta = %v, want 1", d)
	}
}
