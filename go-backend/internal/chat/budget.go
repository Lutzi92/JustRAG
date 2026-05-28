package chat

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/justrag/go-backend/internal/aibudget"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
)

// TurnBudget caps how much "expensive work" one chat turn may consume
// before the orchestrator must wrap up gracefully. Three dimensions,
// only two of them tracked here:
//
//   - Deadline:      wall-clock cutoff (zero = unlimited)
//   - ToolCallsLeft: hard cap on Registry.Dispatch invocations
//     (-1 = unlimited; 0 with hadCalls=true = exhausted)
//   - tokens:        delegated to the existing internal/aibudget package
//     so this struct does NOT own a parallel counter.
//     The handler seeds both — TurnBudget for time / tool
//     calls, aibudget for tokens — and CheckExceeded
//     consults aibudget.Exceeded(ctx) for the token branch.
//     Avoids the dual-counter trap that would have plan-
//     execute checking both `aibudget.Exceeded` AND
//     `TurnBudget.tokensLeft.Load()` separately.
//
// All four orchestrators (agentic, plan-execute, supervisor, standard)
// consult the budget through `TurnBudgetFromContext(ctx)` before any
// expensive action. On exhaustion they break out and synthesise an
// answer from whatever they've gathered — never half-answers, never
// silent stalls.
//
// AP-A3 design: ToolCallsLeft is atomic int64 because the plan-execute
// DAG executor runs nodes at the same topological level in parallel
// via errgroup. The Deadline is a value type — a time.Time is safe to
// read concurrently without a mutex.
type TurnBudget struct {
	Deadline      time.Time
	toolCallsLeft atomic.Int64
	// hadCalls is set once in NewTurnBudget and read concurrently from
	// every orchestrator goroutine. Safe without atomicity because
	// WithTurnBudget publishes the *TurnBudget to ctx AFTER construction
	// completes — that publication establishes a happens-before edge for
	// every later TurnBudgetFromContext reader. Not extracted into a
	// sentinel on toolCallsLeft (e.g. -1 = unlimited) because
	// DeductToolCall does Add(-1) unconditionally: a sentinel would have
	// to gate the Add behind a Load, racing the deduction across the
	// parallel DAG executor's level-mates.
	hadCalls bool
}

// NewTurnBudget builds a budget from raw config values. Zero means
// unlimited on the source side — the constructor maps it to -1
// internally so the deduction methods can distinguish "no limit"
// from "exhausted".
//
// timeBudget == 0 → no deadline.
// toolCallBudget == 0 → unlimited tool calls.
//
// Tokens are NOT a parameter here — seed via aibudget.New(ctx, n)
// in the handler before WithTurnBudget. CheckExceeded reads from
// that ctx-bound counter.
func NewTurnBudget(timeBudget time.Duration, toolCallBudget int) *TurnBudget {
	b := &TurnBudget{}
	if timeBudget > 0 {
		b.Deadline = time.Now().Add(timeBudget)
	}
	if toolCallBudget > 0 {
		b.toolCallsLeft.Store(int64(toolCallBudget))
		b.hadCalls = true
	} else {
		b.toolCallsLeft.Store(-1)
	}
	return b
}

type turnBudgetCtxKey struct{}

// WithTurnBudget injects b into ctx. Pass a nil budget to express
// "no budget configured for this request" — `TurnBudgetFromContext`
// returns nil and orchestrators treat that as unlimited.
func WithTurnBudget(ctx context.Context, b *TurnBudget) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, turnBudgetCtxKey{}, b)
}

// TurnBudgetFromContext returns the budget bound to ctx, or nil if
// none was set. Callers MUST nil-check; the methods on the type
// have nil-receiver guards but using nil through an interface call
// still segfaults.
func TurnBudgetFromContext(ctx context.Context) *TurnBudget {
	if v, ok := ctx.Value(turnBudgetCtxKey{}).(*TurnBudget); ok {
		return v
	}
	return nil
}

// HasTime reports whether the wall-clock budget allows another
// expensive step. Returns true when no deadline is set OR when the
// deadline is in the future. Never blocks.
func (b *TurnBudget) HasTime() bool {
	if b == nil {
		return true
	}
	if b.Deadline.IsZero() {
		return true
	}
	return time.Now().Before(b.Deadline)
}

// HasToolCalls reports whether the tool-call counter allows another
// Registry.Dispatch.
func (b *TurnBudget) HasToolCalls() bool {
	if b == nil || !b.hadCalls {
		return true
	}
	return b.toolCallsLeft.Load() > 0
}

// DeductToolCall subtracts one from the tool-call counter. No-op
// when no limit was set. Returns true when budget remains.
func (b *TurnBudget) DeductToolCall() bool {
	if b == nil || !b.hadCalls {
		return true
	}
	left := b.toolCallsLeft.Add(-1)
	return left > 0
}

// CheckExceeded returns the first dimension of the budget that's
// exhausted (or "" when all dimensions are still valid). The
// orchestrator passes its own name as `orchestrator` for the metric;
// the function fires `rag_turn_budget_exceeded_total{kind,orchestrator}`
// on the kind it returns and logs a structured event.
//
// Order: time → tokens (via aibudget) → tool_calls. Time is checked
// first because once the wall clock is up, the answer is already
// stalling for the user — the most user-visible failure mode.
//
// Tokens delegates to aibudget.Exceeded(ctx). When no aibudget is
// bound to ctx, that returns false (= not exhausted), so the only
// way to trigger the tokens branch here is for the handler to have
// called aibudget.New(ctx, chat_turn_budget_tokens) earlier in the
// pipeline.
func (b *TurnBudget) CheckExceeded(ctx context.Context, orchestrator string) string {
	// Even when b is nil we still consult aibudget — the chat handler
	// seeds aibudget independently of TurnBudget when only the token
	// knob is set.
	if b != nil && !b.HasTime() {
		observability.RecordTurnBudgetExceeded("time", orchestrator)
		logctx.From(ctx).Info("rag.turn_budget.exceeded",
			"orchestrator", orchestrator, "kind", "time")
		return "time"
	}
	if aibudget.Exceeded(ctx) {
		observability.RecordTurnBudgetExceeded("tokens", orchestrator)
		logctx.From(ctx).Info("rag.turn_budget.exceeded",
			"orchestrator", orchestrator, "kind", "tokens")
		return "tokens"
	}
	if b != nil && !b.HasToolCalls() {
		observability.RecordTurnBudgetExceeded("tool_calls", orchestrator)
		logctx.From(ctx).Info("rag.turn_budget.exceeded",
			"orchestrator", orchestrator, "kind", "tool_calls")
		return "tool_calls"
	}
	return ""
}
