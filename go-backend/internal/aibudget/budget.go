// Package aibudget tracks LLM-token spend across a single agentic run.
// The counter is attached to context.Context so deeply-nested LLM call
// sites can report tokens without threading explicit accumulator
// arguments. Used by RunPlanExecuteChat to short-circuit the iterate
// loop once the configured budget is exhausted; reusable by future
// phases (Reflection, GraphRAG) without taking a chat-package
// dependency.
package aibudget

import (
	"context"
	"sync/atomic"
)

// NoCounterRemaining is the value Remaining returns when no token
// counter is attached to ctx. Large enough that arithmetic like
// `min(Remaining(ctx), n)` or `Remaining(ctx) - n` stays positive on
// uncapped paths, ensuring callers that gate on Remaining > 0 don't
// accidentally block the no-counter case.
const NoCounterRemaining = 1 << 30

type counter struct {
	used   atomic.Int64
	budget int64
}

type ctxKey struct{}

// New returns a child context carrying a fresh token counter with the
// given budget. budget <= 0 means "always exceeded" (caller wants to
// disable the path entirely without a separate flag).
func New(parent context.Context, budget int) context.Context {
	c := &counter{budget: int64(budget)}
	return context.WithValue(parent, ctxKey{}, c)
}

// Add records `tokens` against the counter on ctx, if any. No-op when
// no counter is attached.
func Add(ctx context.Context, tokens int) {
	c, ok := ctx.Value(ctxKey{}).(*counter)
	if !ok || c == nil {
		return
	}
	c.used.Add(int64(tokens))
}

// Used returns the cumulative tokens recorded so far. 0 when no counter
// is attached.
func Used(ctx context.Context) int {
	c, ok := ctx.Value(ctxKey{}).(*counter)
	if !ok || c == nil {
		return 0
	}
	return int(c.used.Load())
}

// Remaining returns budget - used, clamped at 0. Returns a large
// sentinel (1<<30) when no counter is attached so callers treating
// "remaining > 0" as a permission gate don't accidentally gate the
// no-counter path.
func Remaining(ctx context.Context) int {
	c, ok := ctx.Value(ctxKey{}).(*counter)
	if !ok || c == nil {
		return NoCounterRemaining
	}
	used := c.used.Load()
	if used >= c.budget {
		return 0
	}
	return int(c.budget - used)
}

// Exceeded reports whether used >= budget. Returns false (uncapped)
// when no counter is attached.
func Exceeded(ctx context.Context) bool {
	c, ok := ctx.Value(ctxKey{}).(*counter)
	if !ok || c == nil {
		return false
	}
	return c.used.Load() >= c.budget
}
