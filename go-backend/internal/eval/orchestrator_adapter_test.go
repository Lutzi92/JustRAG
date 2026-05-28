package eval

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

// stubSiteCfg returns hard-coded values for the gate keys. Any other key
// returns nil so the gate helpers' defaults fire.
type stubSiteCfg struct{ values map[string]string }

func (s *stubSiteCfg) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := s.values[key]; ok {
		return &v, nil
	}
	return nil, nil
}

func TestSelectOrchestrator_StandardWhenAllGatesOff(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorStandard {
		t.Fatalf("got %q, want %q", got, OrchestratorStandard)
	}
	if reason != "fallback_no_orchestrator_enabled" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_StandardWhenNotComplexReasoning(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_supervisor_enabled":   "true",
		"chat_plan_execute_enabled": "true",
		"chat_agentic_enabled":      "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeLookup)
	if got != OrchestratorStandard {
		t.Fatalf("got %q, want %q", got, OrchestratorStandard)
	}
	if reason != "fallback_query_type_lookup" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_SupervisorWinsWhenEnabled(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_supervisor_enabled":   "true",
		"chat_plan_execute_enabled": "true",
		"chat_agentic_enabled":      "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorSupervisor {
		t.Fatalf("got %q, want %q", got, OrchestratorSupervisor)
	}
	if reason != "complex_reasoning_supervisor_gate" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_PlanExecuteDAGWhenSupervisorOff(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_plan_execute_enabled": "true",
		"chat_plan_execute_dag":     "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorPlanExecuteDAG {
		t.Fatalf("got %q, want %q", got, OrchestratorPlanExecuteDAG)
	}
	if reason != "complex_reasoning_plan_execute_dag_gate" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_PlanExecuteFlatWhenDAGOff(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_plan_execute_enabled": "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorPlanExecute {
		t.Fatalf("got %q, want %q", got, OrchestratorPlanExecute)
	}
	if reason != "complex_reasoning_plan_execute_gate" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_AgenticLast(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_agentic_enabled": "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorAgentic {
		t.Fatalf("got %q, want %q", got, OrchestratorAgentic)
	}
	if reason != "complex_reasoning_agentic_gate" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestBuildAgentTrace_PlanExecuteDAG(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "plan", Queries: []string{"a", "b", "c"}},
		{Stage: "search", Step: 1, Query: "a"},
		{Stage: "search", Step: 2, Query: "b"},
		{Stage: "answer", Decision: "answered"},
	}
	got := BuildAgentTrace(OrchestratorPlanExecuteDAG, "complex_reasoning_plan_execute_dag_gate", events, PlanShapeInputs{DAG: true, MaxDepth: 4, Iterative: true})
	if got.Orchestrator != OrchestratorPlanExecuteDAG {
		t.Fatalf("orchestrator: got %q", got.Orchestrator)
	}
	if got.Plan == nil {
		t.Fatal("Plan must be non-nil for plan_execute*")
	}
	if got.Plan.NodeCount != 3 || got.Plan.MaxDepth != 4 || !got.Plan.DAG || !got.Plan.Iterative {
		t.Fatalf("Plan = %+v", *got.Plan)
	}
	if got.Tools["kb_search"] != 2 {
		t.Fatalf("Tools[kb_search] = %d, want 2", got.Tools["kb_search"])
	}
}

func TestBuildAgentTrace_Supervisor(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "decision", Decision: "agent_dispatch", Reason: "retriever", Findings: 5},
		{Stage: "search", Step: 1},
	}
	got := BuildAgentTrace(OrchestratorSupervisor, "complex_reasoning_supervisor_gate", events, PlanShapeInputs{})
	if got.Specialist != "retriever" {
		t.Fatalf("Specialist = %q", got.Specialist)
	}
	if got.Plan != nil {
		t.Fatalf("Plan must be nil for supervisor; got %+v", *got.Plan)
	}
}

func TestBuildAgentTrace_Agentic(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "hop", Step: 1},
		{Stage: "hop", Step: 2},
		{Stage: "answer"},
	}
	got := BuildAgentTrace(OrchestratorAgentic, "complex_reasoning_agentic_gate", events, PlanShapeInputs{})
	if got.Hops != 2 {
		t.Fatalf("Hops = %d, want 2", got.Hops)
	}
	if got.Plan != nil {
		t.Fatalf("Plan must be nil for agentic; got %+v", *got.Plan)
	}
}

func TestBuildAgentTrace_Standard(t *testing.T) {
	// Standard branch typically passes no events (the standard path
	// doesn't go through CollectEmit). The trace records only the
	// orchestrator label + dispatch reason.
	got := BuildAgentTrace(OrchestratorStandard, "fallback_query_type_lookup", nil, PlanShapeInputs{})
	if got.Orchestrator != OrchestratorStandard {
		t.Fatalf("orchestrator: got %q", got.Orchestrator)
	}
	if got.DispatchReason != "fallback_query_type_lookup" {
		t.Fatalf("DispatchReason: got %q", got.DispatchReason)
	}
	if got.Plan != nil || got.Hops != 0 || got.Specialist != "" {
		t.Fatalf("standard trace must not populate orchestrator-specific fields; got %+v", got)
	}
}
