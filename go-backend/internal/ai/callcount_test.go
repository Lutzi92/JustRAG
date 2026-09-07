package ai

import (
	"context"
	"sync"
	"testing"
)

// TestCallCounterFromAbsentIsNilSafe pins the default-path contract: a
// context that never went through WithCallCounter (i.e. every production
// request today) yields a nil *CallCounter from CallCounterFrom, and Inc /
// Count on that nil pointer must not panic and must report zero. This is
// the behaviour every doJSON/StreamChatCompletion call site relies on to
// call Inc unconditionally.
func TestCallCounterFromAbsentIsNilSafe(t *testing.T) {
	c := CallCounterFrom(context.Background())
	if c != nil {
		t.Fatalf("expected nil counter on a plain context, got %#v", c)
	}
	// Must not panic.
	c.Inc()
	c.Inc()
	if got := c.Count(); got != 0 {
		t.Fatalf("nil counter Count() = %d, want 0", got)
	}
}

// TestCallCounterFromPresentCounts pins the measurement contract: once a
// context is attached via WithCallCounter, CallCounterFrom on that same
// context returns a counter that Inc bumps and Count reflects accurately.
func TestCallCounterFromPresentCounts(t *testing.T) {
	ctx, counter := WithCallCounter(context.Background())
	if counter == nil {
		t.Fatal("WithCallCounter returned a nil counter")
	}
	got := CallCounterFrom(ctx)
	if got != counter {
		t.Fatalf("CallCounterFrom returned a different counter instance: %p vs %p", got, counter)
	}
	for range 5 {
		got.Inc()
	}
	if n := counter.Count(); n != 5 {
		t.Fatalf("Count() = %d, want 5", n)
	}
}

// TestCallCounterSharedAcrossChildContexts pins the fan-out contract this
// measurement depends on: a context derived from the counter-bearing one
// (e.g. one leg of a concurrent retrieval fan-out, or a per-round answer-tool
// call) must report into the SAME counter, not a fresh zero one, and
// concurrent increments from multiple goroutines must all be counted (no
// lost updates under a race).
func TestCallCounterSharedAcrossChildContexts(t *testing.T) {
	ctx, counter := WithCallCounter(context.Background())
	child, cancel := context.WithCancel(ctx)
	defer cancel()

	if CallCounterFrom(child) != counter {
		t.Fatal("child context did not inherit the parent's call counter")
	}

	const goroutines = 20
	const perGoroutine = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			c := CallCounterFrom(child)
			for range perGoroutine {
				c.Inc()
			}
		}()
	}
	wg.Wait()

	want := goroutines * perGoroutine
	if got := counter.Count(); got != want {
		t.Fatalf("Count() = %d, want %d", got, want)
	}
}

// TestCallCounterMutationCatchesRemovedIncrement is a guard-that-cannot-fail
// check: it asserts the SAME behaviour a doJSON/StreamChatCompletion call
// site relies on (increment happens on Inc, not on construction), by
// verifying a fresh counter starts at zero and only advances on explicit
// Inc calls. If Inc were ever accidentally turned into a no-op (the mutation
// this measurement is most exposed to, since it is a fire-and-forget call at
// two call sites with no other test coverage), this fails.
func TestCallCounterMutationCatchesRemovedIncrement(t *testing.T) {
	_, counter := WithCallCounter(context.Background())
	if counter.Count() != 0 {
		t.Fatalf("fresh counter Count() = %d, want 0", counter.Count())
	}
	counter.Inc()
	if counter.Count() != 1 {
		t.Fatalf("after one Inc, Count() = %d, want 1", counter.Count())
	}
}
