package chat

import (
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

// complexBase returns inputs where every orchestrator flag is on and the query
// is complex_reasoning — i.e. every gate is eligible, so precedence alone
// decides. Individual tests switch single fields off to walk down the ladder.
func complexBase() OrchestratorInputs {
	return OrchestratorInputs{
		QueryType:             vector.QueryTypeComplexReasoning,
		EnhanceRequested:      false,
		ComparisonReady:       true,
		TeamSelected:          true,
		CorpusTableEnabled:    true,
		CorpusChunksAvailable: true,
		IsCorpusQuery:         true,
		CorpusRouterLLMOn:     false,
		DriftEnabled:          true,
		IsGlobalSynthesis:     true,
		SupervisorEnabled:     true,
		PlanExecuteEnabled:    true,
		AgenticEnabled:        true,
	}
}

func alwaysConfirm() bool { return true }

func TestSelectOrchestratorPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OrchestratorInputs)
		want   Orchestrator
	}{
		{"comparison wins over everything", func(in *OrchestratorInputs) {}, OrchComparison},
		{"team wins when no comparison", func(in *OrchestratorInputs) {
			in.ComparisonReady = false
		}, OrchTeam},
		{"corpus table wins when no team", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected = false, false
		}, OrchCorpusTable},
		{"drift wins when corpus query does not match", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
		}, OrchDrift},
		{"supervisor wins when not a global synthesis query", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
			in.IsGlobalSynthesis = false
		}, OrchSupervisor},
		{"plan execute wins when supervisor off", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
			in.IsGlobalSynthesis, in.SupervisorEnabled = false, false
		}, OrchPlanExecute},
		{"agentic wins when plan execute off", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
			in.IsGlobalSynthesis, in.SupervisorEnabled = false, false
			in.PlanExecuteEnabled = false
		}, OrchAgentic},
		{"standard is the fallback", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
			in.IsGlobalSynthesis, in.SupervisorEnabled = false, false
			in.PlanExecuteEnabled, in.AgenticEnabled = false, false
		}, OrchStandard},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := complexBase()
			tt.mutate(&in)
			if got := SelectOrchestrator(in, alwaysConfirm); got != tt.want {
				t.Fatalf("SelectOrchestrator() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A lookup query must never reach drift/supervisor/plan-execute/agentic —
// all four require complex_reasoning.
func TestSelectOrchestratorLookupFallsThroughToStandard(t *testing.T) {
	in := complexBase()
	in.QueryType = vector.QueryTypeLookup
	in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false

	if got := SelectOrchestrator(in, alwaysConfirm); got != OrchStandard {
		t.Fatalf("SelectOrchestrator() = %q, want %q", got, OrchStandard)
	}
}

// An explicit user enhancement suppresses every classifier-driven
// orchestrator, matching body.Enhance == "" in the original ladder.
func TestSelectOrchestratorEnhanceSuppressesClassifiedOrchestrators(t *testing.T) {
	in := complexBase()
	in.ComparisonReady = false
	in.EnhanceRequested = true

	if got := SelectOrchestrator(in, alwaysConfirm); got != OrchStandard {
		t.Fatalf("SelectOrchestrator() = %q, want %q", got, OrchStandard)
	}
}

// The corpus-table confirmation is an LLM call. It must not fire when a
// higher-priority gate already won.
func TestSelectOrchestratorDoesNotConfirmWhenHigherGateWins(t *testing.T) {
	in := complexBase()
	in.CorpusRouterLLMOn = true

	calls := 0
	got := SelectOrchestrator(in, func() bool { calls++; return true })

	if got != OrchComparison {
		t.Fatalf("SelectOrchestrator() = %q, want %q", got, OrchComparison)
	}
	if calls != 0 {
		t.Fatalf("confirmCorpus called %d times, want 0", calls)
	}
}

// When the corpus-table gate IS reached and the LLM router is on, the
// confirmation fires exactly once.
func TestSelectOrchestratorConfirmsExactlyOnce(t *testing.T) {
	in := complexBase()
	in.ComparisonReady, in.TeamSelected = false, false
	in.CorpusRouterLLMOn = true

	calls := 0
	got := SelectOrchestrator(in, func() bool { calls++; return true })

	if got != OrchCorpusTable {
		t.Fatalf("SelectOrchestrator() = %q, want %q", got, OrchCorpusTable)
	}
	if calls != 1 {
		t.Fatalf("confirmCorpus called %d times, want 1", calls)
	}
}

// A refused confirmation falls through to the next gate.
func TestSelectOrchestratorConfirmRefusalFallsThrough(t *testing.T) {
	in := complexBase()
	in.ComparisonReady, in.TeamSelected = false, false
	in.CorpusRouterLLMOn = true

	got := SelectOrchestrator(in, func() bool { return false })

	if got != OrchDrift {
		t.Fatalf("SelectOrchestrator() = %q, want %q", got, OrchDrift)
	}
}
