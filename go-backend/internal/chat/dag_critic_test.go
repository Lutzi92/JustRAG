package chat

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// fakeDAGCritic captures every Critique call and returns scripted
// decisions per call number (or repeats the last decision if the
// script runs out).
type fakeDAGCritic struct {
	scripted []DAGCritiqueDecision
	calls    int
}

func (f *fakeDAGCritic) Critique(_ context.Context, _ string, _ []DAGCompletedNode, _ []DAGPendingNode, _, _ map[string]bool) (DAGCritiqueDecision, error) {
	idx := f.calls
	f.calls++
	if idx >= len(f.scripted) {
		idx = len(f.scripted) - 1
	}
	return f.scripted[idx], nil
}

// TestExecuteDAG_CriticAnswerNow: when the critic returns
// answer_now after level 0, levels 1+ MUST NOT execute.
func TestExecuteDAG_CriticAnswerNow(t *testing.T) {
	t.Parallel()
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "a", Query: "qa"},
		{ID: "b", Query: "qb", DependsOn: []string{"a"}},
		{ID: "c", Query: "qc", DependsOn: []string{"b"}},
	}}
	run, runCount := countingRun([]vector.SearchChunk{{ID: "x", Score: 0.5}})
	critic := &fakeDAGCritic{
		scripted: []DAGCritiqueDecision{{Action: "answer_now"}},
	}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{
		Plan: plan, KbID: "kb", MaxNodes: 6, MaxDepth: 3,
		Critic: critic, Question: "q?",
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCount.Load(); got != 1 {
		t.Errorf("answer_now should stop after level 0 (1 run), got %d runs", got)
	}
}

// TestExecuteDAG_CriticAddNodes: when the critic returns
// add_nodes, the new nodes execute on the next iteration. The
// total run count includes them.
func TestExecuteDAG_CriticAddNodes(t *testing.T) {
	t.Parallel()
	plan := ai.Plan{Nodes: []ai.PlanNode{{ID: "a", Query: "qa"}}}
	run, runCount := countingRun([]vector.SearchChunk{{ID: "x", Score: 0.5}})
	critic := &fakeDAGCritic{
		scripted: []DAGCritiqueDecision{
			{
				Action: "add_nodes",
				NewNodes: []ai.PlanNode{
					{ID: "z", Query: "follow-up", DependsOn: []string{"a"}},
				},
			},
			// Subsequent calls return continue (no more critic
			// surgery after the inserted level).
			{Action: "continue"},
		},
	}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{
		Plan: plan, KbID: "kb", MaxNodes: 6, MaxDepth: 3,
		Critic: critic, Question: "q?",
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCount.Load(); got != 2 {
		t.Errorf("expected 2 runs (a + z added), got %d", got)
	}
}

// TestExecuteDAG_CriticAddNodesViolatesWidth: when add_nodes would
// push past MaxNodes, the addition is rejected and execution
// continues with the original plan.
func TestExecuteDAG_CriticAddNodesViolatesWidth(t *testing.T) {
	t.Parallel()
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "a", Query: "qa"},
		{ID: "b", Query: "qb"},
		{ID: "c", Query: "qc"},
	}}
	run, runCount := countingRun(nil)
	critic := &fakeDAGCritic{
		scripted: []DAGCritiqueDecision{
			{
				Action: "add_nodes",
				NewNodes: []ai.PlanNode{
					{ID: "x", Query: "extra"},
					{ID: "y", Query: "more extra"},
				},
			},
		},
	}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{
		Plan: plan, KbID: "kb", MaxNodes: 4, MaxDepth: 3, // cap=4, plan=3, +2 → cap_violation
		Critic: critic, Question: "q?",
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCount.Load(); got != 3 {
		t.Errorf("cap violation should leave plan unchanged (3 runs); got %d", got)
	}
}

// TestExecuteDAG_CriticPruneNodes: prune_nodes removes the named
// IDs from upcoming levels. Pruned nodes never execute.
func TestExecuteDAG_CriticPruneNodes(t *testing.T) {
	t.Parallel()
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "a", Query: "qa"},
		{ID: "b", Query: "qb", DependsOn: []string{"a"}},
		{ID: "c", Query: "qc", DependsOn: []string{"a"}},
	}}
	// b and c depend on a but not on each other, so the executor
	// runs them in parallel at level 1 — both the counter AND the
	// ranIDs map must be synchronised.
	var runCount atomic.Int32
	var ranIDsMu sync.Mutex
	ranIDs := make(map[string]bool)
	run := func(_ context.Context, query string, _ []string, _ string) ([]vector.SearchChunk, error) {
		runCount.Add(1)
		ranIDsMu.Lock()
		ranIDs[query] = true
		ranIDsMu.Unlock()
		return nil, nil
	}
	critic := &fakeDAGCritic{
		scripted: []DAGCritiqueDecision{
			{Action: "prune_nodes", PruneNodeIDs: []string{"c"}},
			{Action: "continue"},
		},
	}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{
		Plan: plan, KbID: "kb", MaxNodes: 6, MaxDepth: 3,
		Critic: critic, Question: "q?",
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCount.Load(); got != 2 {
		t.Errorf("expected 2 runs (a + b; c pruned), got %d", got)
	}
	// Note: query passes through buildNodeQuery which prefixes
	// "Given that <excerpt>" for nodes with deps. Check substring.
	bRan := false
	ranIDsMu.Lock()
	defer ranIDsMu.Unlock()
	for q := range ranIDs {
		if q == "qb" || (len(q) > 4 && q[len(q)-3:] == " qb") {
			bRan = true
		}
	}
	if !bRan {
		t.Errorf("node b did not run; ranIDs=%v", ranIDs)
	}
}

// TestExecuteDAG_NoCriticUnchangedBehaviour: when no Critic is
// wired, the executor behaves exactly as it did before AP-D3.
// Important regression guard for legacy (non-iterative) callers.
func TestExecuteDAG_NoCriticUnchangedBehaviour(t *testing.T) {
	t.Parallel()
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "a", Query: "qa"},
		{ID: "b", Query: "qb", DependsOn: []string{"a"}},
	}}
	run, runCount := countingRun(nil)
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{
		Plan: plan, KbID: "kb", MaxNodes: 6, MaxDepth: 3,
		// Critic intentionally nil
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCount.Load(); got != 2 {
		t.Errorf("expected 2 runs (a + b), got %d", got)
	}
}
