package chat

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// fakeToolRunner is shared across DAG-executor tests. ExecuteDAG fans
// nodes out via errgroup, so concurrent invocations of RunTool need
// real synchronisation — a bare `map[string]int` raced under the race
// detector once the executor was parallelised.
type fakeToolRunner struct {
	mu    sync.Mutex
	calls map[string]int // tool name → count
	chunk vector.SearchChunk
}

func (f *fakeToolRunner) RunTool(_ context.Context, _, tool string, _ map[string]any, _ string) ([]vector.SearchChunk, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[tool]++
	c := f.chunk
	f.mu.Unlock()
	if c.ID == "" {
		c = vector.SearchChunk{ID: "tool-" + tool, Content: "tool result for " + tool, Score: 0.9}
	}
	return []vector.SearchChunk{c}, nil
}

// countingRun returns a runNode closure plus an atomic counter. Tests
// previously used `runCalls := 0` + `runCalls++` inside the closure —
// safe under the old serial executor, but racy after the AP-* refactor
// parallelised ExecuteDAG. Centralising the pattern keeps the call
// sites tight and the synchronisation correct.
func countingRun(out []vector.SearchChunk) (run func(context.Context, string, []string, string) ([]vector.SearchChunk, error), counter *atomic.Int32) {
	counter = new(atomic.Int32)
	run = func(_ context.Context, _ string, _ []string, _ string) ([]vector.SearchChunk, error) {
		counter.Add(1)
		return out, nil
	}
	return run, counter
}

// TestExecuteDAG_ToolRouting_Default: when ToolRunner is nil, every
// node goes through the legacy runNodeFn — backward compat for
// every existing test and deployment.
func TestExecuteDAG_ToolRouting_Default(t *testing.T) {
	t.Parallel()
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "q1"},
		{ID: "n2", Query: "q2", Tool: "calculator", Args: map[string]any{"expression": "1+2"}},
	}}
	run, runCalls := countingRun([]vector.SearchChunk{{ID: "kb", Content: "kb result"}})
	res, err := ExecuteDAG(context.Background(), DAGExecuteParams{
		Plan:     plan,
		KbID:     "kb-1",
		MaxNodes: 6, MaxDepth: 3,
		// ToolRunner: nil — every node, even the calculator one, must
		// fall back to runNodeFn.
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCalls.Load(); got != 2 {
		t.Errorf("expected runNode called for both nodes (no ToolRunner), got %d", got)
	}
	if len(res.Nodes) != 2 {
		t.Errorf("expected 2 nodes in result, got %d", len(res.Nodes))
	}
}

// TestExecuteDAG_ToolRouting_DispatchesNonKBSearch: when ToolRunner
// is wired AND a node specifies Tool != "kb_search", the executor
// must dispatch through ToolRunner instead of runNodeFn. This is
// the AP-B3 behaviour gate.
func TestExecuteDAG_ToolRouting_DispatchesNonKBSearch(t *testing.T) {
	t.Parallel()
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "q1"}, // empty Tool → runNode
		{ID: "n2", Query: "q2", Tool: "kb_search", Args: map[string]any{"query": "q2"}}, // explicit kb_search → runNode
		{ID: "n3", Query: "1+2", Tool: "calculator", Args: map[string]any{"expression": "1+2"}},
		{ID: "n4", Query: "m", Tool: "keyword_search", Args: map[string]any{"query": "m"}},
	}}
	tr := &fakeToolRunner{}
	run, runCalls := countingRun([]vector.SearchChunk{{ID: "kb", Content: "kb result"}})
	res, err := ExecuteDAG(context.Background(), DAGExecuteParams{
		Plan:     plan,
		KbID:     "kb-1",
		MaxNodes: 6, MaxDepth: 3,
		ToolRunner: tr,
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got := runCalls.Load(); got != 2 {
		t.Errorf("expected runNode called twice (n1 empty Tool + n2 kb_search), got %d", got)
	}
	tr.mu.Lock()
	calc, kw := tr.calls["calculator"], tr.calls["keyword_search"]
	tr.mu.Unlock()
	if calc != 1 {
		t.Errorf("expected calculator tool dispatched once, got %d", calc)
	}
	if kw != 1 {
		t.Errorf("expected keyword_search tool dispatched once, got %d", kw)
	}
	if len(res.Nodes) != 4 {
		t.Errorf("expected 4 nodes in result, got %d", len(res.Nodes))
	}
}
