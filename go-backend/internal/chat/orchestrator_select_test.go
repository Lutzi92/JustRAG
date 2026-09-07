package chat

import (
	"testing"

	"github.com/justrag/go-backend/internal/chatpolicy"
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
		LongContextEnabled:    true,
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
		// W3-R5: long-context sits directly below DRIFT and above the
		// supervisor. Mutation that this pins: swapping the drift and
		// longcontext arms of the ladder in SelectOrchestrator makes the
		// "drift wins when corpus query does not match" row above return
		// OrchLongContext and fail.
		{"long-context wins when drift is off", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
			in.DriftEnabled = false
		}, OrchLongContext},
		{"supervisor wins when long-context is off too", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
			in.DriftEnabled, in.LongContextEnabled = false, false
		}, OrchSupervisor},
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

// ---------------------------------------------------------------------------
// W6-R6 / W6-R10: the per-query orchestrator policy hook
// ---------------------------------------------------------------------------

// policyRule is a one-rule policy naming orch with the given mode and an
// (optionally empty) condition.
func policyRule(when chatpolicy.When, orch, mode string) chatpolicy.OrchestratorPolicy {
	return chatpolicy.OrchestratorPolicy{{When: when, Orchestrator: orch, Mode: mode}}
}

// ladderCases enumerates the whole flag ladder: every arm on/off, each query
// type, enhance on/off, and the three always-first arms. Used by the
// byte-identity test below AND as the corpus the "never beats the always-first
// arms" test draws from.
func ladderCases() []struct {
	name   string
	mutate func(*OrchestratorInputs)
} {
	off := func(in *OrchestratorInputs) {
		in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
	}
	return []struct {
		name   string
		mutate func(*OrchestratorInputs)
	}{
		{"all-on", func(in *OrchestratorInputs) {}},
		{"comparison-only", func(in *OrchestratorInputs) {
			in.TeamSelected, in.IsCorpusQuery = false, false
		}},
		{"team", func(in *OrchestratorInputs) { in.ComparisonReady = false }},
		{"team-with-enhance", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.EnhanceRequested = false, true
		}},
		{"corpus-table", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected = false, false
		}},
		{"corpus-table-llm-router", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected = false, false
			in.CorpusRouterLLMOn = true
		}},
		{"corpus-table-disabled", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected = false, false
			in.CorpusTableEnabled = false
		}},
		{"corpus-chunks-missing", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected = false, false
			in.CorpusChunksAvailable = false
		}},
		{"drift", off},
		{"drift-off-longcontext", func(in *OrchestratorInputs) {
			off(in)
			in.DriftEnabled = false
		}},
		{"supervisor", func(in *OrchestratorInputs) {
			off(in)
			in.DriftEnabled, in.LongContextEnabled = false, false
		}},
		{"supervisor-not-global-synthesis", func(in *OrchestratorInputs) {
			off(in)
			in.IsGlobalSynthesis = false
		}},
		{"plan-execute", func(in *OrchestratorInputs) {
			off(in)
			in.IsGlobalSynthesis, in.SupervisorEnabled = false, false
		}},
		{"agentic", func(in *OrchestratorInputs) {
			off(in)
			in.IsGlobalSynthesis, in.SupervisorEnabled = false, false
			in.PlanExecuteEnabled = false
		}},
		{"standard-fallback", func(in *OrchestratorInputs) {
			off(in)
			in.IsGlobalSynthesis, in.SupervisorEnabled = false, false
			in.PlanExecuteEnabled, in.AgenticEnabled = false, false
		}},
		{"all-flags-off", func(in *OrchestratorInputs) {
			off(in)
			in.DriftEnabled, in.LongContextEnabled = false, false
			in.SupervisorEnabled, in.PlanExecuteEnabled, in.AgenticEnabled = false, false, false
		}},
		{"enhance-suppresses-everything", func(in *OrchestratorInputs) {
			off(in)
			in.EnhanceRequested = true
		}},
		{"lookup", func(in *OrchestratorInputs) {
			off(in)
			in.QueryType = vector.QueryTypeLookup
		}},
		{"lookup-with-corpus-query", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected = false, false
			in.QueryType = vector.QueryTypeLookup
		}},
		{"enumeration", func(in *OrchestratorInputs) {
			off(in)
			in.QueryType = vector.QueryTypeEnumeration
		}},
		{"enumeration-drift-off", func(in *OrchestratorInputs) {
			off(in)
			in.QueryType = vector.QueryTypeEnumeration
			in.DriftEnabled = false
		}},
		{"complex-drift-only", func(in *OrchestratorInputs) {
			off(in)
			in.LongContextEnabled, in.SupervisorEnabled = false, false
			in.PlanExecuteEnabled, in.AgenticEnabled = false, false
		}},
		{"complex-longcontext-only", func(in *OrchestratorInputs) {
			off(in)
			in.DriftEnabled, in.SupervisorEnabled = false, false
			in.PlanExecuteEnabled, in.AgenticEnabled = false, false
		}},
		{"complex-agentic-only", func(in *OrchestratorInputs) {
			off(in)
			in.DriftEnabled, in.LongContextEnabled, in.SupervisorEnabled = false, false, false
			in.PlanExecuteEnabled = false
		}},
		{"complex-plan-execute-only", func(in *OrchestratorInputs) {
			off(in)
			in.DriftEnabled, in.LongContextEnabled, in.SupervisorEnabled = false, false, false
			in.AgenticEnabled = false
		}},
		{"lookup-team", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.IsCorpusQuery = false, false
			in.QueryType = vector.QueryTypeLookup
		}},
		{"enumeration-enhance", func(in *OrchestratorInputs) {
			off(in)
			in.QueryType = vector.QueryTypeEnumeration
			in.EnhanceRequested = true
		}},
	}
}

// W6-R10: with an empty policy the ladder must be byte-identical — same
// orchestrator AND the same number of confirmCorpus (LLM) calls, on every
// combination the ladder distinguishes.
func TestSelectOrchestratorWithPolicy_EmptyPolicyIsByteIdentical(t *testing.T) {
	cases := ladderCases()
	if len(cases) < 24 {
		t.Fatalf("ladder table has %d cases, want >= 24", len(cases))
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			inA := complexBase()
			tt.mutate(&inA)
			callsA := 0
			wantOrch := SelectOrchestrator(inA, func() bool { callsA++; return true })

			inB := complexBase()
			tt.mutate(&inB)
			callsB := 0
			gotOrch, dec := SelectOrchestratorWithPolicy(inB, nil, chatpolicy.Signals{}, func() bool { callsB++; return true })

			if gotOrch != wantOrch {
				t.Fatalf("orchestrator = %q, want %q", gotOrch, wantOrch)
			}
			if callsA != callsB {
				t.Fatalf("confirmCorpus called %d times with an empty policy, want %d", callsB, callsA)
			}
			if dec.Matched || dec.Applied {
				t.Fatalf("empty policy reported Matched=%v Applied=%v, want both false", dec.Matched, dec.Applied)
			}
			if dec.RuleIndex != -1 {
				t.Fatalf("empty policy reported RuleIndex=%d, want -1", dec.RuleIndex)
			}
			if dec.ForceDAG {
				t.Fatal("empty policy reported ForceDAG=true")
			}
		})
	}
}

// A "force" rule selects its orchestrator even though that orchestrator's own
// feature flag is off — that is what force means.
func TestSelectOrchestratorWithPolicy_ForceBeatsLadder(t *testing.T) {
	in := complexBase()
	in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
	in.QueryType = vector.QueryTypeLookup
	in.DriftEnabled, in.LongContextEnabled = false, false
	in.SupervisorEnabled, in.PlanExecuteEnabled, in.AgenticEnabled = false, false, false

	pol := policyRule(chatpolicy.When{QueryType: []string{"lookup"}}, "supervisor", chatpolicy.ModeForce)
	got, dec := SelectOrchestratorWithPolicy(in, pol, chatpolicy.Signals{QueryType: "lookup"}, alwaysConfirm)

	if got != OrchSupervisor {
		t.Fatalf("orchestrator = %q, want %q", got, OrchSupervisor)
	}
	if !dec.Matched || !dec.Applied {
		t.Fatalf("Matched=%v Applied=%v, want both true", dec.Matched, dec.Applied)
	}
	if dec.RuleIndex != 0 {
		t.Fatalf("RuleIndex = %d, want 0", dec.RuleIndex)
	}
	if dec.Mode != chatpolicy.ModeForce {
		t.Fatalf("Mode = %q, want %q", dec.Mode, chatpolicy.ModeForce)
	}
}

// A "prefer" rule whose orchestrator flag is off is a fall-through: the ladder
// decides, but the decision still reports Matched so the trajectory event can
// distinguish it from "no rule matched".
func TestSelectOrchestratorWithPolicy_PreferNeedsFlag(t *testing.T) {
	in := complexBase()
	in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
	in.QueryType = vector.QueryTypeLookup
	in.DriftEnabled, in.LongContextEnabled = false, false
	in.SupervisorEnabled, in.PlanExecuteEnabled, in.AgenticEnabled = false, false, false

	pol := policyRule(chatpolicy.When{QueryType: []string{"lookup"}}, "supervisor", chatpolicy.ModePrefer)
	got, dec := SelectOrchestratorWithPolicy(in, pol, chatpolicy.Signals{QueryType: "lookup"}, alwaysConfirm)

	if got != OrchStandard {
		t.Fatalf("orchestrator = %q, want %q", got, OrchStandard)
	}
	if !dec.Matched {
		t.Fatal("Matched = false, want true (the rule DID match; only its flag was off)")
	}
	if dec.Applied {
		t.Fatal("Applied = true, want false (prefer + flag off must fall through)")
	}
	if dec.RuleIndex != 0 {
		t.Fatalf("RuleIndex = %d, want 0", dec.RuleIndex)
	}
}

// W6-R16: force never bypasses the comparison / team / corpus-table arms.
func TestSelectOrchestratorWithPolicy_NeverBeatsComparisonTeamCorpus(t *testing.T) {
	pol := policyRule(chatpolicy.When{}, "agentic", chatpolicy.ModeForce)

	tests := []struct {
		name   string
		mutate func(*OrchestratorInputs)
		want   Orchestrator
	}{
		{"comparison", func(in *OrchestratorInputs) {}, OrchComparison},
		{"team", func(in *OrchestratorInputs) { in.ComparisonReady = false }, OrchTeam},
		{"corpus table", func(in *OrchestratorInputs) {
			in.ComparisonReady, in.TeamSelected = false, false
		}, OrchCorpusTable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := complexBase()
			tt.mutate(&in)
			got, dec := SelectOrchestratorWithPolicy(in, pol, chatpolicy.Signals{QueryType: "complex_reasoning"}, alwaysConfirm)
			if got != tt.want {
				t.Fatalf("orchestrator = %q, want %q", got, tt.want)
			}
			if dec.Matched || dec.Applied {
				t.Fatalf("policy reported Matched=%v Applied=%v on an always-first arm, want both false", dec.Matched, dec.Applied)
			}
		})
	}
}

// "plan_execute_dag" has no chat.Orchestrator twin: it is OrchPlanExecute with
// the DAG pinned on for the turn.
func TestSelectOrchestratorWithPolicy_PlanExecuteDAGPinsDAG(t *testing.T) {
	in := complexBase()
	in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
	in.PlanExecuteEnabled = false

	pol := policyRule(chatpolicy.When{}, "plan_execute_dag", chatpolicy.ModeForce)
	got, dec := SelectOrchestratorWithPolicy(in, pol, chatpolicy.Signals{}, alwaysConfirm)

	if got != OrchPlanExecute {
		t.Fatalf("orchestrator = %q, want %q", got, OrchPlanExecute)
	}
	if !dec.ForceDAG {
		t.Fatal("ForceDAG = false, want true")
	}
	if !dec.Applied || dec.RuleIndex != 0 {
		t.Fatalf("Applied=%v RuleIndex=%d, want true/0", dec.Applied, dec.RuleIndex)
	}
}

// Forcing "standard" pins the fallback route even on a complex query with the
// supervisor on — the operator opting a query class OUT of the orchestrators.
func TestSelectOrchestratorWithPolicy_StandardForcesStandard(t *testing.T) {
	in := complexBase()
	in.ComparisonReady, in.TeamSelected, in.IsCorpusQuery = false, false, false
	in.DriftEnabled, in.LongContextEnabled = false, false

	pol := policyRule(chatpolicy.When{QueryType: []string{"complex_reasoning"}}, chatpolicy.OrchestratorStandard, chatpolicy.ModeForce)
	got, dec := SelectOrchestratorWithPolicy(in, pol, chatpolicy.Signals{QueryType: "complex_reasoning"}, alwaysConfirm)

	if got != OrchStandard {
		t.Fatalf("orchestrator = %q, want %q (supervisor is on, so the ladder alone would pick it)", got, OrchStandard)
	}
	if !dec.Applied {
		t.Fatal("Applied = false, want true")
	}
	if dec.ForceDAG {
		t.Fatal("ForceDAG = true on a standard rule")
	}
}

// The six policy names that DO have a chat.Orchestrator twin must equal the
// wire-contract constants byte for byte (the Task-5 reviewer's request:
// chatpolicy carries the names as plain strings and nothing else pins them to
// the chat package's constants). "plan_execute_dag" has no twin by design
// (W6-R16) and is asserted to be the only one without.
func TestChatPolicyOrchestratorNamesMatchChatConstants(t *testing.T) {
	want := map[string]Orchestrator{
		"drift":                         OrchDrift,
		"longcontext":                   OrchLongContext,
		"supervisor":                    OrchSupervisor,
		"plan_execute":                  OrchPlanExecute,
		"agentic":                       OrchAgentic,
		chatpolicy.OrchestratorStandard: OrchStandard,
	}
	seenWithoutTwin := []string{}
	for _, name := range chatpolicy.Orchestrators {
		twin, ok := want[name]
		if !ok {
			seenWithoutTwin = append(seenWithoutTwin, name)
			continue
		}
		if string(twin) != name {
			t.Errorf("chatpolicy name %q != chat.Orchestrator %q", name, twin)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("chatpolicy.Orchestrators is missing names with a chat twin: %v", want)
	}
	if len(seenWithoutTwin) != 1 || seenWithoutTwin[0] != "plan_execute_dag" {
		t.Errorf("names without a chat.Orchestrator twin = %v, want exactly [plan_execute_dag]", seenWithoutTwin)
	}
}
