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
	r := fakeReader{vals: map[string]string{"crag_enabled": "true"}}
	g, err := Project(context.Background(), r, fakeReader{vals: map[string]string{}}, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	assertKeys(t, "ProjectedGraph", keysOf(t, g),
		[]string{"lane", "nodes", "edges", "orchestrators", "estLlmCalls", "estLatencyMs"})

	if len(g.Nodes) == 0 {
		t.Fatal("projection returned no nodes")
	}
	// Pick a node that has keys, a reason and values populated so no field is
	// omitted by omitempty and silently escapes the check.
	var sample *ProjectedNode
	for i := range g.Nodes {
		if len(g.Nodes[i].Keys) > 0 && g.Nodes[i].Reason != "" {
			sample = &g.Nodes[i]
			break
		}
	}
	if sample == nil {
		t.Fatal("no node with both Keys and a Reason — adjust the fixture")
	}
	assertKeys(t, "ProjectedNode", keysOf(t, sample),
		[]string{"id", "label", "group", "help", "keys", "alwaysOn", "llmCalls",
			"latencyMs", "activation", "reason", "values", "origins", "editable"})

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
	assertKeys(t, "OrchestratorCandidate", keysOf(t, &g.Orchestrators[0]),
		[]string{"orchestrator", "activation"})
}
