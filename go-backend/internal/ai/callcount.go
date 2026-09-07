package ai

import (
	"context"
	"sync/atomic"
)

// CallCounter counts model-provider requests made while a context derived
// from WithCallCounter is in flight. "LLM call" here means any request to
// the model-provider HTTP API — chat completions (streamed or unary),
// embeddings and reranks all count, because they all consume provider
// capacity and (usually) billing, which is exactly what a cost measurement
// needs. It is deliberately not "reasoning calls only".
//
// Zero value is unusable directly; construct via WithCallCounter. The
// pointer is safe for concurrent use by multiple goroutines sharing the same
// (possibly derived) context, e.g. parallel sub-searches within one eval
// question.
type CallCounter struct {
	n atomic.Int64
}

// Inc increments the counter. Nil-safe: Inc on a nil *CallCounter is a no-op,
// so call sites can do `CallCounterFrom(ctx).Inc()` unconditionally without a
// nil check — the common case (no counter attached, e.g. production chat
// traffic) costs one nil comparison.
func (c *CallCounter) Inc() {
	if c == nil {
		return
	}
	c.n.Add(1)
}

// Count returns the current count. Nil-safe: a nil *CallCounter reads as 0.
func (c *CallCounter) Count() int {
	if c == nil {
		return 0
	}
	return int(c.n.Load())
}

// callCounterKey is an unexported context key type so no other package can
// collide with it.
type callCounterKey struct{}

// WithCallCounter attaches a fresh *CallCounter to ctx and returns both the
// derived context and the counter. Every context derived from the returned
// one (including across goroutines that share it, e.g. concurrent retrieval
// fan-out) reports into the same counter — CallCounterFrom walks the normal
// context.Value chain, so there is nothing to re-attach at each call site.
func WithCallCounter(ctx context.Context) (context.Context, *CallCounter) {
	c := &CallCounter{}
	return context.WithValue(ctx, callCounterKey{}, c), c
}

// CallCounterFrom returns the *CallCounter attached to ctx via
// WithCallCounter, or nil when none is attached (the default — production
// request paths never call WithCallCounter, so this is nil there and Inc is
// a no-op).
func CallCounterFrom(ctx context.Context) *CallCounter {
	c, _ := ctx.Value(callCounterKey{}).(*CallCounter)
	return c
}
