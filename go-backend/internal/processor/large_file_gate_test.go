package processor

import (
	"context"
	"testing"
	"time"
)

// TestNewLargeFileGateClampsSlotsBelowOne verifies that 0 and negative slot
// counts are clamped to 1 rather than producing a semaphore with zero
// capacity (which would block every Acquire forever — a misconfigured slot
// count must degrade to "fully serialised", not "nothing ever ingests").
//
// Mutation check: change `if slots < 1 { slots = 1 }` to `if slots < 0 {
// slots = 1 }` so NewLargeFileGate(0) constructs a zero-capacity semaphore.
// That mutation makes this test red — Acquire on the returned gate would
// block forever and the test would time out — confirmed by hand below in
// the task report.
func TestNewLargeFileGateClampsSlotsBelowOne(t *testing.T) {
	for _, slots := range []int{0, -1, -5} {
		gate := NewLargeFileGate(slots)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := gate.Acquire(ctx); err != nil {
			t.Fatalf("slots=%d: expected a clamped 1-slot gate to acquire immediately, got %v", slots, err)
		}
		gate.Release()
	}
}

// TestLargeFileGateAcquireReleaseSerialisesAtCapacity verifies the core
// semaphore contract directly on LargeFileGate (independent of Processor):
// a 1-slot gate lets a second Acquire through only after the first Release.
func TestLargeFileGateAcquireReleaseSerialisesAtCapacity(t *testing.T) {
	gate := NewLargeFileGate(1)
	if err := gate.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		if err := gate.Acquire(context.Background()); err == nil {
			close(acquired)
		}
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded while the only slot was held")
	case <-time.After(150 * time.Millisecond):
	}

	gate.Release()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second acquire never completed after the slot was released")
	}
}

// TestLargeFileGateAcquireRespectsContextCancellation verifies Acquire is
// ctx-aware: a canceled context unblocks a waiting Acquire with an error
// instead of hanging forever (this is what lets ProcessFile's cancellation
// path — markTerminalError with stage "canceled" — actually fire while
// queued for a slot).
func TestLargeFileGateAcquireRespectsContextCancellation(t *testing.T) {
	gate := NewLargeFileGate(1)
	if err := gate.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer gate.Release()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- gate.Acquire(ctx) }()

	// Give the goroutine a moment to actually reach the blocked Acquire
	// before cancelling, so a passing test can't be an artifact of
	// cancelling before Acquire even started waiting.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected Acquire to return an error on context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire did not return after its context was canceled")
	}
}
