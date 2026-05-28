package eval

import (
	"reflect"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

func TestExtractSpecialist(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "plan", Reason: "supervisor.dispatch"},
		{Stage: "decision", Decision: "agent_dispatch", Reason: "retriever", Findings: 7},
	}
	if got := ExtractSpecialist(events); got != "retriever" {
		t.Fatalf("got %q, want %q", got, "retriever")
	}
}

func TestExtractSpecialist_NoneWhenAbsent(t *testing.T) {
	events := []chat.TrajectoryEvent{{Stage: "hop", Step: 1, Query: "q"}}
	if got := ExtractSpecialist(events); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestExtractToolCounts_TopicalCalls(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "decision", Decision: "agent_tool_call", Reason: "kb_search"},
		{Stage: "decision", Decision: "agent_tool_call", Reason: "kb_search"},
		{Stage: "decision", Decision: "agent_tool_call", Reason: "graph_search"},
	}
	got := ExtractToolCounts(events)
	want := map[string]int{"kb_search": 2, "graph_search": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractToolCounts_FallbackFromSearchStage(t *testing.T) {
	// No agent_tool_call events => fall back to counting search stages
	// as kb_search invocations. This is the eval path today, where the
	// orchestrators run without an MCP dispatcher.
	events := []chat.TrajectoryEvent{
		{Stage: "search", Step: 1, Query: "q1"},
		{Stage: "search", Step: 2, Query: "q2"},
	}
	got := ExtractToolCounts(events)
	want := map[string]int{"kb_search": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractToolCounts_NilWhenEmpty(t *testing.T) {
	got := ExtractToolCounts(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestExtractHops(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "hop", Step: 1},
		{Stage: "hop", Step: 2, Reason: "judge_followup"},
		{Stage: "hop", Step: 3},
		{Stage: "answer", Step: 3, Decision: "answered"},
	}
	if got := ExtractHops(events); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestExtractPlanNodeCount(t *testing.T) {
	events := []chat.TrajectoryEvent{
		// First plan event has no queries (starting event); should be skipped.
		{Stage: "plan", Reason: "starting"},
		{Stage: "plan", Queries: []string{"sub1", "sub2", "sub3"}},
		{Stage: "iterate", Step: 1},
	}
	if got := ExtractPlanNodeCount(events); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestExtractPlanNodeCount_NoneWhenAbsent(t *testing.T) {
	events := []chat.TrajectoryEvent{{Stage: "hop", Step: 1}}
	if got := ExtractPlanNodeCount(events); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}
