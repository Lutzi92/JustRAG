package worker

import (
	"context"
	"testing"
	"time"
)

func TestWatchAbortKey_NilRedis(t *testing.T) {
	// Should return immediately without panic when rdb is nil.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		watchAbortKey(ctx, cancel, nil, "test-key")
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("watchAbortKey with nil redis did not return")
	}
}

func TestWatchAbortKey_EmptyKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		watchAbortKey(ctx, cancel, nil, "")
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("watchAbortKey with empty key did not return")
	}
}

func TestWatchAbortKey_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	done := make(chan struct{})
	go func() {
		watchAbortKey(ctx, func() {}, nil, "")
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("watchAbortKey did not return on cancelled context")
	}
}
