package chat

import "github.com/justrag/go-backend/internal/vector"

// Orchestrator names the dispatch target that wins one chat turn.
//
// The string values are a wire contract: they appear in the
// rag.deep_chat.dispatch log line, in agent_decisions rows, and in the
// workflow projection API (internal/pipeline). Do not rename without a
// migration note.
type Orchestrator string

const (
	OrchComparison  Orchestrator = "comparison"
	OrchTeam        Orchestrator = "team"
	OrchCorpusTable Orchestrator = "corpus_table"
	OrchDrift       Orchestrator = "drift"
	OrchSupervisor  Orchestrator = "supervisor"
	OrchPlanExecute Orchestrator = "plan_execute"
	OrchAgentic     Orchestrator = "agentic"
	OrchStandard    Orchestrator = "standard"
)

// OrchestratorInputs is every signal the precedence ladder consults, resolved
// up front — EXCEPT the corpus-table LLM confirmation, which stays behind the
// confirmCorpus callback so it keeps its short-circuit (see SelectOrchestrator).
//
// Fields mirror the original inline expression in http_send.go 1:1 so the
// extraction is auditable against git history.
type OrchestratorInputs struct {
	// QueryType is the classifier verdict (vector.QueryType* constants).
	QueryType string
	// EnhanceRequested mirrors body.Enhance != "" — an explicit user
	// enhancement suppresses every classifier-driven orchestrator.
	EnhanceRequested bool

	// ComparisonReady is the fully-resolved in-chat comparison gate:
	// attachment store present AND willRunComparison(...) true.
	ComparisonReady bool
	// TeamSelected reports an explicit user-created team/agent selection.
	TeamSelected bool

	CorpusTableEnabled    bool
	CorpusChunksAvailable bool
	// IsCorpusQuery is the cheap keyword classifier only. The optional LLM
	// confirmation is the confirmCorpus callback.
	IsCorpusQuery     bool
	CorpusRouterLLMOn bool

	DriftEnabled      bool
	IsGlobalSynthesis bool

	SupervisorEnabled  bool
	PlanExecuteEnabled bool
	AgenticEnabled     bool
}

// complexAndUnenhanced is the shared precondition of drift, supervisor,
// plan-execute and agentic.
func (in OrchestratorInputs) complexAndUnenhanced() bool {
	return in.QueryType == vector.QueryTypeComplexReasoning && !in.EnhanceRequested
}

// SelectOrchestrator returns the orchestrator that wins this turn.
//
// confirmCorpus is invoked AT MOST ONCE, and only at the exact point the
// original inline expression would have called ai.ConfirmCorpusComparison —
// after every higher-priority gate has already failed and the cheap keyword
// classifier has already matched. This preserves the short-circuit that keeps
// that LLM call off the hot path. Callers that cannot make an LLM call (the
// workflow projection in internal/pipeline) pass a constant function.
func SelectOrchestrator(in OrchestratorInputs, confirmCorpus func() bool) Orchestrator {
	if in.ComparisonReady {
		return OrchComparison
	}
	if in.TeamSelected && !in.EnhanceRequested {
		return OrchTeam
	}
	if in.CorpusTableEnabled && in.CorpusChunksAvailable && !in.EnhanceRequested &&
		in.IsCorpusQuery && (!in.CorpusRouterLLMOn || confirmCorpus()) {
		return OrchCorpusTable
	}
	if in.DriftEnabled && in.complexAndUnenhanced() && in.IsGlobalSynthesis {
		return OrchDrift
	}
	if in.SupervisorEnabled && in.complexAndUnenhanced() {
		return OrchSupervisor
	}
	if in.PlanExecuteEnabled && in.complexAndUnenhanced() {
		return OrchPlanExecute
	}
	if in.AgenticEnabled && in.complexAndUnenhanced() {
		return OrchAgentic
	}
	return OrchStandard
}
