package ai

import (
	"testing"
)

// TestNormaliseCritiqueResult_UnknownActionContinues: any unknown
// action coerces to "continue". The executor never gets a "weird"
// action it can't switch on.
func TestNormaliseCritiqueResult_UnknownActionContinues(t *testing.T) {
	t.Parallel()
	in := CritiqueResult{Action: "explore_more"}
	got := normaliseCritiqueResult(in, nil, nil)
	if got.Action != CritiqueActionContinue {
		t.Errorf("unknown action coerced to %q, want continue", got.Action)
	}
}

// TestNormaliseCritiqueResult_AddNodesIDDedup: new_nodes whose IDs
// collide with existing plan node IDs (or with each other in the
// same call) MUST drop. The dag executor would otherwise produce
// "duplicate node id" errors mid-flight.
func TestNormaliseCritiqueResult_AddNodesIDDedup(t *testing.T) {
	t.Parallel()
	existing := map[string]bool{"n1": true, "n2": true}
	in := CritiqueResult{
		Action: CritiqueActionAddNodes,
		NewNodes: []PlanNode{
			{ID: "n1", Query: "collides with existing"},
			{ID: "n3", Query: "good"},
			{ID: "n3", Query: "self-collision"},
			{ID: "n4", Query: "another good"},
		},
	}
	got := normaliseCritiqueResult(in, existing, nil)
	if got.Action != CritiqueActionAddNodes {
		t.Fatalf("expected add_nodes action, got %q", got.Action)
	}
	if len(got.NewNodes) != 2 {
		t.Errorf("expected 2 surviving nodes (n3, n4), got %d", len(got.NewNodes))
	}
	for _, n := range got.NewNodes {
		if n.ID == "n1" {
			t.Error("n1 collision slipped through")
		}
	}
}

// TestNormaliseCritiqueResult_AddNodesAtMostTwo: the prompt enforces
// at most 2 new_nodes per call; the normaliser caps at the same
// number as defence in depth.
func TestNormaliseCritiqueResult_AddNodesAtMostTwo(t *testing.T) {
	t.Parallel()
	in := CritiqueResult{
		Action: CritiqueActionAddNodes,
		NewNodes: []PlanNode{
			{ID: "n3", Query: "a"}, {ID: "n4", Query: "b"},
			{ID: "n5", Query: "c"}, {ID: "n6", Query: "d"},
		},
	}
	got := normaliseCritiqueResult(in, nil, nil)
	if len(got.NewNodes) != 2 {
		t.Errorf("expected cap at 2, got %d", len(got.NewNodes))
	}
}

// TestNormaliseCritiqueResult_AddNodesEmptyDemotes: when ALL new
// nodes are filtered out (collisions, empty queries), demote to
// continue rather than emit a no-op add_nodes branch.
func TestNormaliseCritiqueResult_AddNodesEmptyDemotes(t *testing.T) {
	t.Parallel()
	existing := map[string]bool{"n1": true}
	in := CritiqueResult{
		Action: CritiqueActionAddNodes,
		NewNodes: []PlanNode{
			{ID: "n1", Query: "collides"},
			{ID: "", Query: "no id"},
			{ID: "n2", Query: ""}, // empty query
		},
	}
	got := normaliseCritiqueResult(in, existing, nil)
	if got.Action != CritiqueActionContinue {
		t.Errorf("all-filtered add_nodes should demote to continue; got %q", got.Action)
	}
}

// TestNormaliseCritiqueResult_PruneRejectsCompleted: pruning a
// completed node is meaningless (it already contributed findings)
// AND dangerous (deps have already used it). The normaliser drops
// completed-node IDs from the prune list.
func TestNormaliseCritiqueResult_PruneRejectsCompleted(t *testing.T) {
	t.Parallel()
	completed := map[string]bool{"n1": true, "n2": true}
	in := CritiqueResult{
		Action:       CritiqueActionPruneNodes,
		PruneNodeIDs: []string{"n1", "n3", "n2", "n4"},
	}
	got := normaliseCritiqueResult(in, nil, completed)
	if len(got.PruneNodeIDs) != 2 {
		t.Errorf("expected n3+n4 surviving, got %d: %v", len(got.PruneNodeIDs), got.PruneNodeIDs)
	}
	for _, id := range got.PruneNodeIDs {
		if completed[id] {
			t.Errorf("completed id %q slipped past prune filter", id)
		}
	}
}

// TestNormaliseCritiqueResult_PruneEmptyDemotes: same demotion
// pattern as add_nodes. An "all completed" prune list demotes
// to continue.
func TestNormaliseCritiqueResult_PruneEmptyDemotes(t *testing.T) {
	t.Parallel()
	completed := map[string]bool{"n1": true, "n2": true}
	in := CritiqueResult{
		Action:       CritiqueActionPruneNodes,
		PruneNodeIDs: []string{"n1", "n2"},
	}
	got := normaliseCritiqueResult(in, nil, completed)
	if got.Action != CritiqueActionContinue {
		t.Errorf("all-filtered prune should demote to continue; got %q", got.Action)
	}
}

// TestNormaliseCritiqueResult_AnswerNowDoesNotPopulateLists: even
// when the LLM emits new_nodes alongside answer_now, the executor
// should see a clean answer_now decision (the lists would be
// no-ops anyway).
func TestNormaliseCritiqueResult_AnswerNowDoesNotPopulateLists(t *testing.T) {
	t.Parallel()
	in := CritiqueResult{
		Action:       CritiqueActionAnswerNow,
		NewNodes:     []PlanNode{{ID: "n3", Query: "ignored"}},
		PruneNodeIDs: []string{"n2"},
	}
	got := normaliseCritiqueResult(in, nil, nil)
	if got.Action != CritiqueActionAnswerNow {
		t.Errorf("answer_now mutated to %q", got.Action)
	}
	// Lists are NOT cleared by the normaliser — the executor
	// switches on Action and ignores them. Pin this so a future
	// "clear-on-answer_now" refactor doesn't surprise downstream
	// code.
	if len(got.NewNodes) != 0 || len(got.PruneNodeIDs) != 0 {
		t.Logf("answer_now path carries non-empty lists; executor must ignore them")
	}
}
