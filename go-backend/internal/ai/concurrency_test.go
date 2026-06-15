package ai

import (
	"context"
	"testing"

	"golang.org/x/sync/semaphore"
)

// TestAcquireProviderSlotDisabledIsNoop pins the critical contract: with the
// cap unset (the default — AI_MAX_CONCURRENT_REQUESTS is not set in the test
// env), acquireProviderSlot must never block and must return a callable
// release, so the unary path behaves exactly as it did before the cap existed.
func TestAcquireProviderSlotDisabledIsNoop(t *testing.T) {
	if got := maxConcurrentRequests(); got != 0 {
		t.Skipf("AI_MAX_CONCURRENT_REQUESTS set in env (limit=%d); skipping no-op assertion", got)
	}
	// Far more acquisitions than any real semaphore would allow without a
	// matching release — if the disabled path blocked, this would deadlock.
	for range 1000 {
		release, err := acquireProviderSlot(context.Background(), "http://example/")
		if err != nil {
			t.Fatalf("disabled cap returned error: %v", err)
		}
		if release == nil {
			t.Fatal("disabled cap returned nil release")
		}
		release()
	}
}

// TestProviderSemBounds verifies the underlying weighted-semaphore behaviour
// the cap relies on: at the limit, a further non-blocking acquire fails until a
// slot is released. This exercises the semaphore contract directly (the global
// limit is read once via OnceValue and can't be reset per-test).
func TestProviderSemBounds(t *testing.T) {
	sem := semaphore.NewWeighted(2)
	if !sem.TryAcquire(1) {
		t.Fatal("expected first acquire to succeed")
	}
	if !sem.TryAcquire(1) {
		t.Fatal("expected second acquire to succeed")
	}
	if sem.TryAcquire(1) {
		t.Fatal("expected acquire past the limit to fail")
	}
	sem.Release(1)
	if !sem.TryAcquire(1) {
		t.Fatal("expected acquire to succeed after release")
	}
}
