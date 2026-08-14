package pipeline

import "testing"

func TestNodesHaveUniqueIDs(t *testing.T) {
	seen := map[NodeID]bool{}
	for _, n := range Nodes() {
		if seen[n.ID] {
			t.Fatalf("duplicate node id %q", n.ID)
		}
		seen[n.ID] = true
	}
}

func TestNodesHaveLabelAndGroup(t *testing.T) {
	for _, n := range Nodes() {
		if n.Label == "" {
			t.Errorf("node %q has no Label", n.ID)
		}
		if n.Group == "" {
			t.Errorf("node %q has no Group", n.ID)
		}
	}
}

func TestEdgesReferenceKnownNodes(t *testing.T) {
	for _, e := range Edges() {
		if _, ok := NodeByID(e.From); !ok {
			t.Errorf("edge from unknown node %q", e.From)
		}
		if _, ok := NodeByID(e.To); !ok {
			t.Errorf("edge to unknown node %q", e.To)
		}
	}
}

// Every loop edge must declare its bound, or the UI cannot label it and a
// reader cannot tell a bounded correction loop from an unbounded one.
func TestLoopEdgesDeclareMaxIterations(t *testing.T) {
	for _, e := range Edges() {
		if e.Loop && e.MaxIterations == 0 {
			t.Errorf("loop edge %s->%s has no MaxIterations", e.From, e.To)
		}
	}
}

func TestNodeByIDMissing(t *testing.T) {
	if _, ok := NodeByID("does_not_exist"); ok {
		t.Fatal("NodeByID returned ok for an unknown id")
	}
}
