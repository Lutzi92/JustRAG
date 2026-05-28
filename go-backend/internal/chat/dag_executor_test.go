package chat

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// dagChunk is a small SearchChunk constructor that lets tests assert
// "the chunk for node N produced this Content".
func dagChunk(content string, score float64) vector.SearchChunk {
	return vector.SearchChunk{
		ID:       "id-" + content,
		FileID:   "f-" + content,
		FileName: "file-" + content,
		Content:  content,
		Score:    score,
	}
}

// runFromMap returns a runNodeFn that maps query → canned chunks.
func runFromMap(canned map[string][]vector.SearchChunk) runNodeFn {
	return func(_ context.Context, q string, _ []string, _ string) ([]vector.SearchChunk, error) {
		if c, ok := canned[q]; ok {
			return c, nil
		}
		return []vector.SearchChunk{}, nil
	}
}

func TestExecuteDAG_FlatPlanRunsAllNodes(t *testing.T) {
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "a"},
		{ID: "n2", Query: "b"},
	}}
	canned := map[string][]vector.SearchChunk{
		"a": {dagChunk("ca", 0.9)},
		"b": {dagChunk("cb", 0.8)},
	}
	res, err := ExecuteDAG(context.Background(), DAGExecuteParams{Plan: plan}, runFromMap(canned))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Fatalf("nodes returned: got %d, want 2", len(res.Nodes))
	}
	if res.MaxDepth != 1 {
		t.Errorf("flat plan MaxDepth = %d, want 1", res.MaxDepth)
	}
	if res.ParallelNodes != 2 {
		t.Errorf("flat plan ParallelNodes = %d, want 2 (both nodes are level 0)", res.ParallelNodes)
	}
}

// TestExecuteDAG_ParallelExecution proves that two independent nodes
// actually run concurrently — synchronizing them through a barrier
// proves both started before either returned.
func TestExecuteDAG_ParallelExecution(t *testing.T) {
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "a"},
		{ID: "n2", Query: "b"},
	}}
	started := make(chan struct{}, 2)
	gate := make(chan struct{})
	run := func(_ context.Context, q string, _ []string, _ string) ([]vector.SearchChunk, error) {
		started <- struct{}{}
		// Block until both started; then both proceed. If executor were
		// serial this would deadlock and the test would time out.
		<-gate
		return []vector.SearchChunk{dagChunk(q, 0.9)}, nil
	}
	go func() {
		// Wait for both nodes to enter, then release the gate.
		<-started
		<-started
		close(gate)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ExecuteDAG(ctx, DAGExecuteParams{Plan: plan}, run); err != nil {
		t.Fatalf("err: %v (likely serialized — gate deadlocked)", err)
	}
}

func TestExecuteDAG_DependentNodeInterpolatesParentExcerpt(t *testing.T) {
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "who lead project X"},
		{ID: "n2", Query: "what is their email", DependsOn: []string{"n1"}},
	}}
	parentChunk := dagChunk("Alice leads project X", 0.99)
	var n2Query atomic.Value // string
	run := func(_ context.Context, q string, _ []string, _ string) ([]vector.SearchChunk, error) {
		if q == "who lead project X" {
			return []vector.SearchChunk{parentChunk}, nil
		}
		// n2's query has been interpolated with n1's top excerpt by the
		// time the executor reaches us.
		n2Query.Store(q)
		return []vector.SearchChunk{dagChunk("alice@example.com", 0.9)}, nil
	}
	res, err := ExecuteDAG(context.Background(), DAGExecuteParams{Plan: plan}, run)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.MaxDepth != 2 {
		t.Errorf("MaxDepth = %d, want 2", res.MaxDepth)
	}
	got := n2Query.Load().(string)
	if !strings.Contains(got, "Alice leads project X") {
		t.Errorf("dependent node query missing parent excerpt; got %q", got)
	}
	if !strings.Contains(got, "what is their email") {
		t.Errorf("dependent node query missing original query text; got %q", got)
	}
}

func TestExecuteDAG_RejectsSelfReference(t *testing.T) {
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "loops back", DependsOn: []string{"n1"}},
	}}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{Plan: plan}, runFromMap(nil))
	if !errors.Is(err, ErrPlanCycle) {
		t.Errorf("self-reference: want ErrPlanCycle, got %v", err)
	}
}

func TestExecuteDAG_RejectsTwoCycle(t *testing.T) {
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "a", DependsOn: []string{"n2"}},
		{ID: "n2", Query: "b", DependsOn: []string{"n1"}},
	}}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{Plan: plan}, runFromMap(nil))
	if !errors.Is(err, ErrPlanCycle) {
		t.Errorf("two-cycle: want ErrPlanCycle, got %v", err)
	}
}

func TestExecuteDAG_RejectsTransitiveCycle(t *testing.T) {
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "a", DependsOn: []string{"n3"}},
		{ID: "n2", Query: "b", DependsOn: []string{"n1"}},
		{ID: "n3", Query: "c", DependsOn: []string{"n2"}},
	}}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{Plan: plan}, runFromMap(nil))
	if !errors.Is(err, ErrPlanCycle) {
		t.Errorf("transitive cycle: want ErrPlanCycle, got %v", err)
	}
}

func TestExecuteDAG_RejectsTooDeep(t *testing.T) {
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "a"},
		{ID: "n2", Query: "b", DependsOn: []string{"n1"}},
		{ID: "n3", Query: "c", DependsOn: []string{"n2"}},
		{ID: "n4", Query: "d", DependsOn: []string{"n3"}},
	}}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{Plan: plan, MaxDepth: 3}, runFromMap(nil))
	if !errors.Is(err, ErrPlanTooDeep) {
		t.Errorf("too-deep: want ErrPlanTooDeep, got %v", err)
	}
}

func TestExecuteDAG_RejectsTooWide(t *testing.T) {
	nodes := make([]ai.PlanNode, 7)
	for i := range nodes {
		nodes[i] = ai.PlanNode{ID: "n" + itoa(i), Query: "q" + itoa(i)}
	}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{Plan: ai.Plan{Nodes: nodes}, MaxNodes: 6}, runFromMap(nil))
	if !errors.Is(err, ErrPlanTooWide) {
		t.Errorf("too-wide: want ErrPlanTooWide, got %v", err)
	}
}

func TestExecuteDAG_RejectsMissingParent(t *testing.T) {
	plan := ai.Plan{Nodes: []ai.PlanNode{
		{ID: "n1", Query: "a", DependsOn: []string{"ghost"}},
	}}
	_, err := ExecuteDAG(context.Background(), DAGExecuteParams{Plan: plan}, runFromMap(nil))
	if err == nil || !strings.Contains(err.Error(), "missing parent") {
		t.Errorf("missing-parent: want error mentioning missing parent, got %v", err)
	}
}

// itoa is a small dependency-free integer formatter to keep the test
// self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
