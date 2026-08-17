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

// A node nobody can reach is a node the canvas draws floating in space. This
// caught nothing when written, but the post-answer verification block was
// rewired in the same change that added NodeFactVerifier, and an orphan there
// would have been invisible until Phase 3 rendered it.
func TestEveryNodeIsWiredIntoTheGraph(t *testing.T) {
	wired := map[NodeID]bool{}
	for _, e := range Edges() {
		wired[e.From] = true
		wired[e.To] = true
	}
	for _, n := range Nodes() {
		if !wired[n.ID] {
			t.Errorf("node %q has no edge in either direction", n.ID)
		}
	}
}

// keylessUnconditionalNodes is the allowlist of nodes that own no site_config
// key AND are genuinely unconditional — they always run, so Project's
// `len(spec.Keys) == 0` shortcut rendering them active is correct.
//
// NodeAgentBinding is deliberately ABSENT: it is keyless but NOT
// unconditional, and Project gives it its own branch (applyAgentBinding).
var keylessUnconditionalNodes = map[NodeID]bool{
	NodeClassify: true,
	NodeAnswer:   true,
}

// A keyless node gets NO coverage from any key-driven guard in this package —
// TestNodeKeysAreActuallyRead's inner loop and defaults_test.go's
// activationKeys() both skip it by construction — and it falls straight into
// Project's "keyless means unconditional, render active" shortcut. That
// combination is how a node could ship claiming every KB has a feature
// switched on, with a fully green suite.
//
// So every keyless node must be a deliberate decision recorded in one of two
// places: this allowlist (genuinely unconditional) or NodeAgentBinding's
// explicit branch in Project plus its own tests in project_test.go. A new
// keyless node lands here as a failure until someone chooses.
func TestKeylessNodesAreDeliberate(t *testing.T) {
	seen := 0
	for _, n := range Nodes() {
		if n.AlwaysOn || len(n.Keys) != 0 {
			continue
		}
		seen++
		if n.ID == NodeAgentBinding {
			continue // has its own activation branch + tests
		}
		if !keylessUnconditionalNodes[n.ID] {
			t.Errorf("node %q owns no site_config key and is not AlwaysOn, so Project "+
				"renders it active on every KB and no key-driven guard covers it. "+
				"Either add it to keylessUnconditionalNodes (it really is "+
				"unconditional) or give it an activation branch in Project and its "+
				"own tests, the way NodeAgentBinding does.", n.ID)
		}
	}
	// Without this the loop above passes when the walk finds nothing — e.g. if
	// Keys were ever changed to always be non-empty, or if the walk's skip
	// condition were inverted.
	//
	// A LITERAL, deliberately. It used to be len(keylessUnconditionalNodes)+1,
	// which rose by one every time somebody added a node to the allowlist — an
	// anti-vacuity floor that moves with the thing it is guarding cannot catch
	// an allowlist that grew. Today's three keyless nodes are NodeClassify,
	// NodeAnswer (both allowlisted) and NodeAgentBinding (its own branch in
	// Project). Adding a fourth is a decision; make it consciously, here.
	const wantKeyless = 3
	if seen != wantKeyless {
		t.Fatalf("found %d keyless nodes, expected exactly %d — either the assertions "+
			"above passed vacuously, or a keyless node was added: decide what it is "+
			"(see the comment on keylessUnconditionalNodes) and update this literal",
			seen, wantKeyless)
	}
}
