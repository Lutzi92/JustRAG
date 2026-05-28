package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestToolCallRecorder_NilSafe: every method must accept a nil
// receiver because ToolCallRecorderFromContext returns nil when no
// recorder is bound. The dispatcher's recording branch already
// nil-checks, but the type's own methods are nil-safe as a defense
// in depth — the most common bug in ctx-bound state is forgetting
// the nil-check on the way IN.
func TestToolCallRecorder_NilSafe(t *testing.T) {
	t.Parallel()
	var r *ToolCallRecorder
	r.Record(ToolCallRecord{Tool: "x", Status: "ok"})
	if got := r.Snapshot(); got != nil {
		t.Errorf("nil snapshot must be nil, got %v", got)
	}
}

// TestToolCallRecorder_Roundtrip pins the basic accumulate +
// snapshot contract. The snapshot must be a COPY — mutating it
// mustn't affect future Records.
func TestToolCallRecorder_Roundtrip(t *testing.T) {
	t.Parallel()
	r := &ToolCallRecorder{}
	r.Record(ToolCallRecord{Tool: "kb_search", DurationMs: 50, Status: "ok"})
	r.Record(ToolCallRecord{Tool: "calculator", DurationMs: 5, Status: "ok"})
	snap1 := r.Snapshot()
	if len(snap1) != 2 {
		t.Fatalf("expected 2 calls in snapshot, got %d", len(snap1))
	}
	snap1[0].Tool = "MUTATED"
	r.Record(ToolCallRecord{Tool: "sql_query", DurationMs: 100, Status: "error"})
	snap2 := r.Snapshot()
	if snap2[0].Tool == "MUTATED" {
		t.Error("snapshot was not a copy — caller mutation bled into recorder state")
	}
	if len(snap2) != 3 {
		t.Errorf("expected 3 calls after second record, got %d", len(snap2))
	}
}

// TestToolCallRecorder_ConcurrentRecord: the plan-execute DAG runs
// sibling nodes in parallel; each may dispatch a tool. The recorder
// MUST be safe under concurrent Record. With race detector on, this
// test surfaces any missing synchronisation.
func TestToolCallRecorder_ConcurrentRecord(t *testing.T) {
	t.Parallel()
	r := &ToolCallRecorder{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Record(ToolCallRecord{Tool: "kb_search", DurationMs: 1, Status: "ok"})
		}()
	}
	wg.Wait()
	if got := len(r.Snapshot()); got != 50 {
		t.Errorf("expected 50 concurrent records to land, got %d", got)
	}
}

// TestClassifyDispatchStatus pins the closed-enum mapping. Drift
// here would silently change which calls show up as "timeout" vs
// "error" in the admin panel.
func TestClassifyDispatchStatus(t *testing.T) {
	t.Parallel()
	if got := classifyDispatchStatus(nil, nil); got != "ok" {
		t.Errorf("nil err → ok, got %q", got)
	}
	if got := classifyDispatchStatus(errors.New("boom"), nil); got != "error" {
		t.Errorf("plain err → error, got %q", got)
	}
	if got := classifyDispatchStatus(errors.New("ctx canceled"), context.DeadlineExceeded); got != "timeout" {
		t.Errorf("err with deadline-exceeded ctx → timeout, got %q", got)
	}
}

// TestToolCallRecorder_CtxRoundtrip: the helper must round-trip
// through the canonical ctx key.
func TestToolCallRecorder_CtxRoundtrip(t *testing.T) {
	t.Parallel()
	r := &ToolCallRecorder{}
	ctx := WithToolCallRecorder(context.Background(), r)
	if got := ToolCallRecorderFromContext(ctx); got != r {
		t.Errorf("retrieved recorder != injected: %p vs %p", got, r)
	}
	if got := ToolCallRecorderFromContext(context.Background()); got != nil {
		t.Error("empty ctx must yield nil recorder")
	}
	if WithToolCallRecorder(ctx, nil) != ctx {
		t.Error("WithToolCallRecorder(ctx, nil) must be a no-op")
	}
}

// TestRecordToolCallToCtx_ComputesDuration: the helper measures
// duration from the start time the caller passed in. ~50 ms sleep
// must show up in the recorded duration.
func TestRecordToolCallToCtx_ComputesDuration(t *testing.T) {
	t.Parallel()
	r := &ToolCallRecorder{}
	ctx := WithToolCallRecorder(context.Background(), r)
	start := time.Now()
	time.Sleep(20 * time.Millisecond)
	recordToolCallToCtx(ctx, "calculator", start, nil)
	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 record, got %d", len(snap))
	}
	if snap[0].DurationMs < 15 {
		t.Errorf("duration_ms too small for 20ms sleep: %d", snap[0].DurationMs)
	}
}
