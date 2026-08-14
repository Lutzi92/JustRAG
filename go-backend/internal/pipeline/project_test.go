package pipeline

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

// fakeReader is an in-memory siteconfig.BatchReader.
type fakeReader struct{ vals map[string]string }

func (f fakeReader) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	if v, ok := f.vals[key]; ok {
		return &v, nil
	}
	return nil, nil
}

func (f fakeReader) GetSiteConfigValues(ctx context.Context, keys []string) (map[string]*string, error) {
	out := make(map[string]*string, len(keys))
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			vv := v
			out[k] = &vv
		} else {
			out[k] = nil
		}
	}
	return out, nil
}

func nodeByIDIn(g *ProjectedGraph, id NodeID) *ProjectedNode {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

func TestProjectMarksDisabledNodeInactive(t *testing.T) {
	// factcheck_in_chat, not chat_factuality_verifier_enabled: Task 4's
	// coverage run made factcheck_in_chat NodeFactuality's Keys[0] (the real
	// master toggle; chat_factuality_verifier_enabled is a narrower,
	// separately-gated escalation path — see nodes.go's NodeFactuality
	// comment).
	r := fakeReader{vals: map[string]string{"factcheck_in_chat": "false"}}

	g, err := Project(context.Background(), r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeFactuality)
	if n == nil {
		t.Fatal("factuality node missing from projection")
	}
	if n.Activation != ActivationInactive {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationInactive)
	}
	if n.Reason != "flag_off" {
		t.Fatalf("Reason = %q, want %q", n.Reason, "flag_off")
	}
}

func TestProjectMarksEnabledNodeActive(t *testing.T) {
	r := fakeReader{vals: map[string]string{"factcheck_in_chat": "true"}}

	g, err := Project(context.Background(), r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if n := nodeByIDIn(g, NodeFactuality); n.Activation != ActivationActive {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationActive)
	}
}

// Nodes with no keys are unconditional stages and must always be active.
func TestProjectKeylessNodesAreAlwaysActive(t *testing.T) {
	g, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, LaneLookup)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for _, id := range []NodeID{NodeRetrieve, NodeRerank, NodeAnswer} {
		if n := nodeByIDIn(g, id); n.Activation != ActivationActive {
			t.Errorf("node %q Activation = %q, want %q", id, n.Activation, ActivationActive)
		}
	}
}

// On a lookup lane none of the complex-reasoning orchestrators can win, so the
// only candidate is standard.
func TestProjectLookupLaneYieldsStandardOnly(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_supervisor_enabled": "true",
		"chat_drift_enabled":      "true",
	}}

	g, err := Project(context.Background(), r, LaneLookup)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if len(g.Orchestrators) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(g.Orchestrators), g.Orchestrators)
	}
	if g.Orchestrators[0].Orchestrator != chat.OrchStandard {
		t.Fatalf("candidate = %q, want %q", g.Orchestrators[0].Orchestrator, chat.OrchStandard)
	}
}

// With drift on, a complex lane has TWO candidates: drift conditionally (for
// global-synthesis queries) and supervisor otherwise.
func TestProjectComplexLaneListsConditionalDrift(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_drift_enabled":      "true",
		"chat_supervisor_enabled": "true",
	}}

	g, err := Project(context.Background(), r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	var drift, supervisor *OrchestratorCandidate
	for i := range g.Orchestrators {
		switch g.Orchestrators[i].Orchestrator {
		case chat.OrchDrift:
			drift = &g.Orchestrators[i]
		case chat.OrchSupervisor:
			supervisor = &g.Orchestrators[i]
		}
	}

	if drift == nil {
		t.Fatal("drift missing from candidates")
	}
	if drift.Activation != ActivationConditional {
		t.Errorf("drift Activation = %q, want %q", drift.Activation, ActivationConditional)
	}
	if drift.Condition == "" {
		t.Error("drift candidate has no human-readable Condition")
	}
	if supervisor == nil {
		t.Fatal("supervisor missing from candidates")
	}
	if supervisor.Activation != ActivationActive {
		t.Errorf("supervisor Activation = %q, want %q", supervisor.Activation, ActivationActive)
	}
}

// The cost estimate must count only nodes that actually run.
func TestProjectCostEstimateExcludesInactiveNodes(t *testing.T) {
	off := fakeReader{vals: map[string]string{"factcheck_in_chat": "false"}}
	on := fakeReader{vals: map[string]string{"factcheck_in_chat": "true"}}

	gOff, err := Project(context.Background(), off, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	gOn, err := Project(context.Background(), on, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if gOn.EstLLMCalls <= gOff.EstLLMCalls {
		t.Fatalf("enabling the factuality verifier did not raise EstLLMCalls (%d vs %d)",
			gOn.EstLLMCalls, gOff.EstLLMCalls)
	}
}

// Registry membership decides whether the UI may offer an editor.
func TestProjectMarksEditability(t *testing.T) {
	g, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	// crag_enabled IS in the per-KB registry today.
	if n := nodeByIDIn(g, NodeCRAGGrade); !n.Editable {
		t.Error("crag_grade should be editable (crag_enabled is in the per-KB registry)")
	}
}

func TestProjectRejectsUnknownLane(t *testing.T) {
	if _, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, Lane("nonsense")); err == nil {
		t.Fatal("Project accepted an unknown lane")
	}
}
