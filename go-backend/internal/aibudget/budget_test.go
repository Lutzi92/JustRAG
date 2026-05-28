package aibudget_test

import (
	"context"
	"sync"
	"testing"

	"github.com/justrag/go-backend/internal/aibudget"
)

func TestNew_AttachesCounterToContext(t *testing.T) {
	ctx := aibudget.New(context.Background(), 1000)
	if got := aibudget.Used(ctx); got != 0 {
		t.Errorf("Used() on fresh counter: want 0, got %d", got)
	}
	if got := aibudget.Remaining(ctx); got != 1000 {
		t.Errorf("Remaining() on fresh 1000-budget counter: want 1000, got %d", got)
	}
}

func TestAdd_AccumulatesAndReportsExceeded(t *testing.T) {
	ctx := aibudget.New(context.Background(), 100)
	aibudget.Add(ctx, 30)
	aibudget.Add(ctx, 30)
	if aibudget.Exceeded(ctx) {
		t.Fatalf("Exceeded() at 60/100: want false, got true")
	}
	aibudget.Add(ctx, 50)
	if !aibudget.Exceeded(ctx) {
		t.Fatalf("Exceeded() at 110/100: want true, got false")
	}
	if got := aibudget.Used(ctx); got != 110 {
		t.Errorf("Used(): want 110, got %d", got)
	}
}

func TestNoCounter_OperationsAreNoOp(t *testing.T) {
	ctx := context.Background()
	aibudget.Add(ctx, 999)
	if aibudget.Exceeded(ctx) {
		t.Errorf("Exceeded() with no counter: want false (uncapped), got true")
	}
	if got := aibudget.Used(ctx); got != 0 {
		t.Errorf("Used() with no counter: want 0, got %d", got)
	}
	if got := aibudget.Remaining(ctx); got != aibudget.NoCounterRemaining {
		t.Errorf("Remaining() with no counter: want NoCounterRemaining, got %d", got)
	}
}

func TestZeroBudget_AlwaysExceeded(t *testing.T) {
	ctx := aibudget.New(context.Background(), 0)
	if !aibudget.Exceeded(ctx) {
		t.Errorf("Exceeded() with zero budget: want true, got false")
	}
}

func TestAdd_ConcurrentSafe(t *testing.T) {
	ctx := aibudget.New(context.Background(), 1_000_000)
	var wg sync.WaitGroup
	const goroutines = 100
	const tokensPerGoroutine = 10
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			aibudget.Add(ctx, tokensPerGoroutine)
		}()
	}
	wg.Wait()
	want := goroutines * tokensPerGoroutine
	if got := aibudget.Used(ctx); got != want {
		t.Errorf("concurrent Used(): want %d, got %d", want, got)
	}
}

func TestNegativeBudget_AlwaysExceeded(t *testing.T) {
	// budget < 0 follows the same "always exceeded" contract as zero.
	ctx := aibudget.New(context.Background(), -1)
	if !aibudget.Exceeded(ctx) {
		t.Errorf("Exceeded() with negative budget: want true, got false")
	}
	if got := aibudget.Remaining(ctx); got != 0 {
		t.Errorf("Remaining() with negative budget: want 0, got %d", got)
	}
}
