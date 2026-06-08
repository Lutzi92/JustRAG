package eval

import "github.com/justrag/go-backend/internal/chat"

// AgentTrace records which orchestrator (and which specialist / tools / plan)
// handled one eval question. Populated by OrchestratorDispatchAdapter when
// the eval mirrors production dispatch; nil for adapters that don't dispatch
// through an orchestrator, so on-disk reports stay byte-stable.
type AgentTrace struct {
	Orchestrator string         `json:"orchestrator"`
	Specialist   string         `json:"specialist,omitempty"`
	Tools        map[string]int `json:"tools,omitempty"`
	Hops         int            `json:"hops,omitempty"`
	Plan         *PlanShape     `json:"plan,omitempty"`
	// ClassifiedQueryType is the query type the production classifier
	// assigned to this question (lookup / enumeration / complex_reasoning).
	// It deterministically drives orchestrator dispatch, so comparing it
	// against the golden Question.QueryType yields routing accuracy. Set by
	// OrchestratorDispatchAdapter; empty for adapters that don't classify.
	ClassifiedQueryType string `json:"classified_query_type,omitempty"`
	DispatchReason      string `json:"dispatch_reason,omitempty"`
}

// PlanShape summarizes a Plan-Execute orchestrator's plan. NodeCount is the
// number of sub-queries (flat planner) or DAG nodes. MaxDepth is the
// dispatcher-configured cap (only meaningful when DAG is true).
type PlanShape struct {
	NodeCount int  `json:"node_count"`
	MaxDepth  int  `json:"max_depth,omitempty"`
	DAG       bool `json:"dag"`
	Iterative bool `json:"iterative,omitempty"`
	ToolAware bool `json:"tool_aware,omitempty"`
}

const (
	OrchestratorSupervisor     = "supervisor"
	OrchestratorPlanExecute    = "plan_execute"
	OrchestratorPlanExecuteDAG = "plan_execute_dag"
	OrchestratorAgentic        = "agentic"
	OrchestratorStandard       = "standard"
)

// ExtractSpecialist returns the supervisor's chosen specialist name from
// the first agent_dispatch decision event, or "" when no dispatch fired.
// Mirrors the event emitted by RunSupervisorChat at
// internal/chat/supervisor_chat.go:94.
func ExtractSpecialist(events []chat.TrajectoryEvent) string {
	for _, e := range events {
		if e.Stage == "decision" && e.Decision == "agent_dispatch" {
			return e.Reason
		}
	}
	return ""
}

// ExtractToolCounts counts MCP tool invocations from the trajectory.
//
// Primary signal: decision events with Decision == "agent_tool_call" keyed
// by Reason (tool name). This is what the MCP dispatcher emits in
// production (see plan_execute_chat.go:432).
//
// Fallback: when no agent_tool_call events fire (the eval path runs
// orchestrators without an MCP dispatcher), count search-stage events as
// kb_search invocations so the trace still records search activity.
// Returns nil for an empty trace so AgentTrace.Tools stays omitempty.
func ExtractToolCounts(events []chat.TrajectoryEvent) map[string]int {
	out := map[string]int{}
	sawToolCall := false
	for _, e := range events {
		if e.Stage == "decision" && e.Decision == "agent_tool_call" && e.Reason != "" {
			out[e.Reason]++
			sawToolCall = true
		}
	}
	if sawToolCall {
		return out
	}
	for _, e := range events {
		if e.Stage == "search" {
			out["kb_search"]++
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ExtractHops counts hop-stage events. Non-agentic orchestrators emit
// none, so this returns 0 for them.
func ExtractHops(events []chat.TrajectoryEvent) int {
	n := 0
	for _, e := range events {
		if e.Stage == "hop" {
			n++
		}
	}
	return n
}

// ExtractPlanNodeCount returns the number of sub-queries / DAG nodes from
// the first plan event that carries a Queries list. Zero when no such
// event exists (e.g. supervisor or agentic orchestrators).
func ExtractPlanNodeCount(events []chat.TrajectoryEvent) int {
	for _, e := range events {
		if e.Stage == "plan" && len(e.Queries) > 0 {
			return len(e.Queries)
		}
	}
	return 0
}
