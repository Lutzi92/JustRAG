package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/splitter"
)

// collectTrajectory returns an Emit func plus an accessor for the events it
// captured. emitTrajectory wraps every event as {"agentTrajectory": evt}.
func collectTrajectory() (func(map[string]any), func() []TrajectoryEvent) {
	var mu sync.Mutex
	var evts []TrajectoryEvent
	emit := func(m map[string]any) {
		evt, ok := m["agentTrajectory"].(TrajectoryEvent)
		if !ok {
			return
		}
		mu.Lock()
		evts = append(evts, evt)
		mu.Unlock()
	}
	return emit, func() []TrajectoryEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]TrajectoryEvent(nil), evts...)
	}
}

// TestConsumeLongContextReduceContextIsTokenBounded pins the reduce-stage size
// bound. The map stage's finding COUNT is unbounded by construction (top-k 500
// at group size 2 is 250 groups, each free to return many findings), so without
// this truncation the reduce prompt could exceed the very token budget the
// chunk pool was truncated against.
//
// Mutation: remove the truncateFindingsToFit call in consumeLongContextWith →
// the budget assertion below fails (and Dropped goes to 0).
func TestConsumeLongContextReduceContextIsTokenBounded(t *testing.T) {
	chunks := lcChunks(4)
	extract := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _, _ string) ([]ai.LongContextFinding, error) {
		out := make([]ai.LongContextFinding, 300)
		for i := range out {
			out[i] = ai.LongContextFinding{
				SourceIdx: i%4 + 1,
				Claim:     fmt.Sprintf("Finding %04d about the corpus wide synthesis question", i+1),
				Quote:     fmt.Sprintf("belegstelle %04d", i+1),
			}
		}
		return out, nil
	}

	p := lcParams(LongContextModeMapReduce)
	p.MaxTokens = 300 // small enough to bind on the findings, large enough to keep all 4 chunks
	emit, events := collectTrajectory()
	p.Emit = emit

	cc, err := consumeLongContextWith(context.Background(), nil, extract, p, chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cc.FinalChunks) != len(chunks) {
		t.Fatalf("pool must be untouched by the findings budget: %d of %d chunks", len(cc.FinalChunks), len(chunks))
	}
	if got := splitter.CountTokens(cc.Context); got > p.MaxTokens {
		t.Errorf("reduce context = %d tokens, over the %d-token budget", got, p.MaxTokens)
	}
	// Source order is preserved and the tail is what gets dropped.
	if !strings.Contains(cc.Context, "Finding 0001") {
		t.Errorf("the earliest finding must survive truncation:\n%s", cc.Context)
	}
	if strings.Contains(cc.Context, "Finding 0300") {
		t.Errorf("the last finding must not fit in a 300-token budget:\n%s", cc.Context)
	}

	var reduce *TrajectoryEvent
	for _, e := range events() {
		if e.Stage == "longcontext_reduce" {
			evt := e
			reduce = &evt
		}
	}
	if reduce == nil {
		t.Fatal("no longcontext_reduce trajectory event")
	}
	if reduce.Dropped <= 0 {
		t.Errorf("Dropped = %d, want > 0 so the operator sees evidence was cut", reduce.Dropped)
	}
	if reduce.Findings+reduce.Dropped != 300 {
		t.Errorf("Findings(%d) + Dropped(%d) must account for all 300 extracted findings", reduce.Findings, reduce.Dropped)
	}
}

// TestTruncateFindingsToFitKeepsAtLeastOne guards the degenerate case: a budget
// the SOURCES header block alone exhausts must still leave the answer LLM one
// finding rather than an empty evidence block (the empty-findings degrade path
// runs earlier, on the map stage's own output).
func TestTruncateFindingsToFitKeepsAtLeastOne(t *testing.T) {
	pool := lcChunks(3)
	findings := []ai.LongContextFinding{
		{SourceIdx: 1, Claim: "first"},
		{SourceIdx: 2, Claim: "second"},
	}
	kept, dropped := truncateFindingsToFit(findings, pool, "de", 1)
	if len(kept) != 1 || kept[0].Claim != "first" {
		t.Fatalf("want the first finding kept, got %#v", kept)
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}

	// A budget that fits everything drops nothing.
	kept, dropped = truncateFindingsToFit(findings, pool, "de", 100_000)
	if len(kept) != 2 || dropped != 0 {
		t.Errorf("ample budget must keep all findings, got %d kept / %d dropped", len(kept), dropped)
	}
}
