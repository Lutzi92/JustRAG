package chat

import (
	"github.com/justrag/go-backend/internal/chatpolicy"
	"github.com/justrag/go-backend/internal/vector"
)

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
	OrchLongContext Orchestrator = "longcontext"
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

	// LongContextEnabled mirrors chat_longcontext_enabled. Combined with
	// IsGlobalSynthesis it selects OrchLongContext (W3-R5) — the same gate
	// ShouldRouteLongContext applies inside PrepareChatContext, hoisted to
	// the ladder so the streaming path reaches the route at all.
	LongContextEnabled bool

	SupervisorEnabled  bool
	PlanExecuteEnabled bool
	AgenticEnabled     bool
}

// complexAndUnenhanced is the shared precondition of drift, supervisor,
// plan-execute and agentic.
func (in OrchestratorInputs) complexAndUnenhanced() bool {
	return in.QueryType == vector.QueryTypeComplexReasoning && !in.EnhanceRequested
}

// PolicyDecision is chatpolicy.Decision plus the DAG pin the policy name
// "plan_execute_dag" implies: that name has no chat.Orchestrator twin
// (W6-R16), so it resolves to OrchPlanExecute with the DAG forced on for this
// turn. ForceDAG is only ever true when Applied is.
type PolicyDecision struct {
	chatpolicy.Decision
	ForceDAG bool
}

// noPolicyDecision is the "the policy did not decide this turn" value. Note
// RuleIndex is -1, not 0 — chatpolicy's own no-match convention, so a
// trajectory or agent_decisions writer can never mistake it for rule 0.
func noPolicyDecision() PolicyDecision {
	return PolicyDecision{Decision: chatpolicy.Decision{RuleIndex: -1}}
}

// SelectOrchestrator returns the orchestrator that wins this turn.
//
// confirmCorpus is invoked AT MOST ONCE, and only at the exact point the
// original inline expression would have called ai.ConfirmCorpusComparison —
// after every higher-priority gate has already failed and the cheap keyword
// classifier has already matched. This preserves the short-circuit that keeps
// that LLM call off the hot path. Callers that cannot make an LLM call (the
// workflow projection in internal/pipeline) pass a constant function.
//
// Implemented as SelectOrchestratorWithPolicy with an empty policy, so W6-R10
// ("the flag ladder is byte-identical when the policy is empty") holds
// structurally rather than by two implementations agreeing — and is still
// pinned by TestSelectOrchestratorWithPolicy_EmptyPolicyIsByteIdentical.
func SelectOrchestrator(in OrchestratorInputs, confirmCorpus func() bool) Orchestrator {
	orch, _ := SelectOrchestratorWithPolicy(in, nil, chatpolicy.Signals{}, confirmCorpus)
	return orch
}

// SelectOrchestratorWithPolicy is SelectOrchestrator with the operator's
// chat_orchestrator_policy table evaluated AFTER the three always-first arms
// (comparison / team / corpus-table) and BEFORE the flag ladder (W6-R6).
//
// The ordering is the ruling, not an implementation detail: comparison, team
// and corpus-table are explicit-intent routes — an attachment, a picked team,
// a corpus question — and a routing policy must never be able to hijack them
// (W6-R16). On those three arms the policy is not even consulted, so the
// returned decision reports Matched=false and the caller emits no event.
//
// A nil/empty policy makes Decide return an immediate no-match, so this
// function's ladder is reached unchanged and confirmCorpus keeps its exact
// call count.
func SelectOrchestratorWithPolicy(in OrchestratorInputs, pol chatpolicy.OrchestratorPolicy, sig chatpolicy.Signals, confirmCorpus func() bool) (Orchestrator, PolicyDecision) {
	if in.ComparisonReady {
		return OrchComparison, noPolicyDecision()
	}
	if in.TeamSelected && !in.EnhanceRequested {
		return OrchTeam, noPolicyDecision()
	}
	if in.CorpusTableEnabled && in.CorpusChunksAvailable && !in.EnhanceRequested &&
		in.IsCorpusQuery && (!in.CorpusRouterLLMOn || confirmCorpus()) {
		return OrchCorpusTable, noPolicyDecision()
	}

	// W6-R6: the policy sits here — below explicit intent, above the flags.
	dec := PolicyDecision{Decision: chatpolicy.Decide(pol, sig, in.policyEnabled())}
	if dec.Applied {
		orch, forceDAG := orchestratorForPolicyName(dec.Orchestrator)
		dec.ForceDAG = forceDAG
		return orch, dec
	}

	return in.ladder(), dec
}

// policyEnabled reports which policy-nameable orchestrators have their feature
// flag on, keyed by chatpolicy.Orchestrators values. Only "prefer" rules read
// it; "force" applies regardless. "standard" is absent on purpose — it has no
// flag and chatpolicy treats it as always enabled.
//
// "plan_execute_dag" reads PlanExecuteEnabled: the DAG is a shape of the
// plan-execute orchestrator, not a separate one, so a preferred DAG route is
// available exactly when plan-execute is.
func (in OrchestratorInputs) policyEnabled() map[string]bool {
	return map[string]bool{
		"drift":            in.DriftEnabled,
		"longcontext":      in.LongContextEnabled,
		"supervisor":       in.SupervisorEnabled,
		"plan_execute":     in.PlanExecuteEnabled,
		"plan_execute_dag": in.PlanExecuteEnabled,
		"agentic":          in.AgenticEnabled,
	}
}

// orchestratorForPolicyName maps a validated chatpolicy orchestrator name onto
// the chat.Orchestrator wire value, plus the DAG pin for the one name that has
// no twin. An unknown name cannot reach here (the parser rejects it at save
// time and the reader re-validates), so the default is the fallback route
// rather than a panic.
func orchestratorForPolicyName(name string) (orch Orchestrator, forceDAG bool) {
	switch name {
	case "drift":
		return OrchDrift, false
	case "longcontext":
		return OrchLongContext, false
	case "supervisor":
		return OrchSupervisor, false
	case "plan_execute":
		return OrchPlanExecute, false
	case "plan_execute_dag":
		return OrchPlanExecute, true
	case "agentic":
		return OrchAgentic, false
	default:
		return OrchStandard, false
	}
}

// ladder is the flag-driven precedence chain, byte-identical to the tail of
// the pre-W6-R6 SelectOrchestrator. It takes no confirmCorpus callback: the
// only arm that needed one (corpus-table) runs before the policy hook and
// stays in SelectOrchestratorWithPolicy.
func (in OrchestratorInputs) ladder() Orchestrator {
	if in.DriftEnabled && in.complexAndUnenhanced() && in.IsGlobalSynthesis {
		return OrchDrift
	}
	// W3-R5: long-context sits directly below DRIFT (which needs KG community
	// summaries and is the more specific global-synthesis answer) and above
	// the supervisor. Same narrow gate as drift, so it rarely intercepts.
	if in.LongContextEnabled && in.complexAndUnenhanced() && in.IsGlobalSynthesis {
		return OrchLongContext
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
