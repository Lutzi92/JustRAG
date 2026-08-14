package pipeline

import (
	"context"
	"encoding/json"
	"testing"
)

// keysOf returns the top-level JSON object keys of v.
func keysOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func assertKeys(t *testing.T, what string, got map[string]bool, want []string) {
	t.Helper()
	for _, k := range want {
		if !got[k] {
			t.Errorf("%s: missing JSON key %q (got %v)", what, k, got)
		}
	}
	for k := range got {
		found := false
		for _, w := range want {
			if k == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: unexpected JSON key %q — the wire contract is camelCase; "+
				"an untagged embedded struct field is the usual cause", what, k)
		}
	}
}

// TestWireFormatIsCamelCase pins the endpoint's JSON key set. The frontend types
// against exactly these names; a casing change here silently breaks the canvas
// with no compile error on either side.
func TestWireFormatIsCamelCase(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":            "true",
		"chat_drift_enabled":      "true",
		"chat_supervisor_enabled": "true",
	}}
	g, err := Project(context.Background(), r, fakeReader{vals: map[string]string{}}, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	assertKeys(t, "ProjectedGraph", keysOf(t, g),
		[]string{"lane", "nodes", "edges", "orchestrators", "estLlmCalls", "estLatencyMs"})

	if len(g.Nodes) == 0 {
		t.Fatal("projection returned no nodes")
	}
	// Pick a node that has keys, a reason, a condition and values populated so no field is
	// omitted by omitempty and silently escapes the check. condition is operationally critical
	// (orchestrator_bypass carries the most surprising explanation) so it must not drift.
	var sample *ProjectedNode
	for i := range g.Nodes {
		if len(g.Nodes[i].Keys) > 0 && g.Nodes[i].Reason != "" && g.Nodes[i].Condition != "" {
			sample = &g.Nodes[i]
			break
		}
	}
	if sample == nil {
		t.Fatal("no node with Keys, Reason, and Condition all non-empty — adjust the fixture")
	}
	assertKeys(t, "ProjectedNode", keysOf(t, sample),
		[]string{"id", "label", "group", "help", "keys", "alwaysOn", "llmCalls",
			"latencyMs", "activation", "reason", "condition", "values", "origins", "editable"})

	if len(g.Edges) == 0 {
		t.Fatal("projection returned no edges")
	}
	var loopEdge *EdgeSpec
	for i := range g.Edges {
		if g.Edges[i].Loop {
			loopEdge = &g.Edges[i]
			break
		}
	}
	if loopEdge == nil {
		t.Fatal("no loop edge in topology")
	}
	assertKeys(t, "EdgeSpec(loop)", keysOf(t, loopEdge),
		[]string{"from", "to", "label", "loop", "maxIterations"})

	if len(g.Orchestrators) == 0 {
		t.Fatal("projection returned no orchestrator candidates")
	}
	// Verify the fallback (active) candidate has no condition (omitempty elides the key).
	fallback := &g.Orchestrators[len(g.Orchestrators)-1]
	assertKeys(t, "OrchestratorCandidate(fallback)", keysOf(t, fallback),
		[]string{"orchestrator", "activation"})

	// Find a conditional candidate (drift, when supervisor is the fallback) to verify
	// condition is present when non-empty.
	var condCandidate *OrchestratorCandidate
	for i := range g.Orchestrators {
		if g.Orchestrators[i].Condition != "" {
			condCandidate = &g.Orchestrators[i]
			break
		}
	}
	if condCandidate == nil {
		t.Fatal("no orchestrator candidate with a Condition — adjust the fixture")
	}
	assertKeys(t, "OrchestratorCandidate(conditional)", keysOf(t, condCandidate),
		[]string{"orchestrator", "activation", "condition"})
}
