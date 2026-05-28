package chat

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/aibudget"
)

// TestTurnBudget_NilSafe: every method must accept a nil receiver
// because TurnBudgetFromContext returns nil when the request handler
// didn't construct one. Without the guards, the orchestrators would
// have to nil-check at every call site.
func TestTurnBudget_NilSafe(t *testing.T) {
	t.Parallel()
	var b *TurnBudget
	if !b.HasTime() {
		t.Error("nil HasTime must report true (= unlimited)")
	}
	if !b.HasToolCalls() {
		t.Error("nil HasToolCalls must report true")
	}
	if !b.DeductToolCall() {
		t.Error("nil DeductToolCall must report true")
	}
	if b.CheckExceeded(context.Background(), "agentic") != "" {
		t.Error("nil CheckExceeded must return empty (no exhaustion)")
	}
}

// TestTurnBudget_UnlimitedDefaults: zero values on the constructor map
// to "unlimited" — distinguishable from "exhausted" via the hadCalls
// flag. Without that distinction, every default deployment would
// immediately abort.
func TestTurnBudget_UnlimitedDefaults(t *testing.T) {
	t.Parallel()
	b := NewTurnBudget(0, 0)
	if !b.HasTime() {
		t.Error("zero deadline must mean unlimited time")
	}
	if !b.HasToolCalls() {
		t.Error("zero tool-call budget must mean unlimited")
	}
	for range 10 {
		b.DeductToolCall()
	}
	if !b.HasToolCalls() {
		t.Error("deduct on unlimited tool-call budget must remain unlimited")
	}
}

// TestTurnBudget_ToolCallExhaustion: once the counter hits zero,
// HasToolCalls flips false. A budget of N must allow exactly N
// successful deductions, then refuse.
func TestTurnBudget_ToolCallExhaustion(t *testing.T) {
	t.Parallel()
	b := NewTurnBudget(0, 3)
	for i := 1; i <= 3; i++ {
		if !b.HasToolCalls() {
			t.Errorf("call %d: HasToolCalls must be true before deduction", i)
		}
		b.DeductToolCall()
	}
	if b.HasToolCalls() {
		t.Error("after 3 deductions on a 3-call budget, HasToolCalls must be false")
	}
}

// TestTurnBudget_TimeExhaustion: a deadline already in the past
// triggers the "time" branch.
func TestTurnBudget_TimeExhaustion(t *testing.T) {
	t.Parallel()
	b := &TurnBudget{Deadline: time.Now().Add(-1 * time.Second)}
	if b.HasTime() {
		t.Error("past deadline must report HasTime=false")
	}
	if got := b.CheckExceeded(context.Background(), "plan_execute"); got != "time" {
		t.Errorf("CheckExceeded with past deadline: got %q, want time", got)
	}
}

// TestTurnBudget_TokensViaAibudget: the tokens dimension delegates to
// aibudget.Exceeded(ctx). When the handler seeds aibudget AND the AI
// layer has logged enough tokens, CheckExceeded returns "tokens"
// (after the time branch passes).
func TestTurnBudget_TokensViaAibudget(t *testing.T) {
	t.Parallel()
	ctx := aibudget.New(context.Background(), 100)
	b := NewTurnBudget(time.Hour, 10)
	if got := b.CheckExceeded(ctx, "agentic"); got != "" {
		t.Errorf("fresh aibudget: CheckExceeded must be empty, got %q", got)
	}
	aibudget.Add(ctx, 100)
	if got := b.CheckExceeded(ctx, "agentic"); got != "tokens" {
		t.Errorf("after 100 tokens on a 100-token budget: got %q, want tokens", got)
	}
}

// TestTurnBudget_CtxRoundtrip: Inject and retrieve. Same pointer.
func TestTurnBudget_CtxRoundtrip(t *testing.T) {
	t.Parallel()
	b := NewTurnBudget(time.Second, 5)
	ctx := WithTurnBudget(context.Background(), b)
	got := TurnBudgetFromContext(ctx)
	if got != b {
		t.Errorf("retrieved budget != injected: %p vs %p", got, b)
	}
	if TurnBudgetFromContext(context.Background()) != nil {
		t.Error("empty ctx must yield nil budget")
	}
	if WithTurnBudget(ctx, nil) != ctx {
		t.Error("WithTurnBudget(ctx, nil) must be a no-op (returns same ctx)")
	}
}

// TestTurnBudget_CheckOrder: the check order is documented as time →
// tokens → tool_calls. When all three are exhausted, CheckExceeded
// must return "time" first.
func TestTurnBudget_CheckOrder(t *testing.T) {
	t.Parallel()
	ctx := aibudget.New(context.Background(), 10)
	aibudget.Add(ctx, 10) // tokens-exhausted
	b := NewTurnBudget(time.Hour, 1)
	b.Deadline = time.Now().Add(-1 * time.Second) // time-exhausted
	b.DeductToolCall()                            // tool-calls-exhausted
	if got := b.CheckExceeded(ctx, "supervisor"); got != "time" {
		t.Errorf("with all three exhausted: got %q, want time first", got)
	}
}

// TestTurnBudget_NilBudgetButAibudgetExceeded: when only the token
// knob is configured, the handler skips TurnBudget construction (no
// time / tool-call limits). aibudget alone should still trigger the
// tokens branch — CheckExceeded handles a nil receiver and still
// consults aibudget.
func TestTurnBudget_NilBudgetButAibudgetExceeded(t *testing.T) {
	t.Parallel()
	ctx := aibudget.New(context.Background(), 10)
	aibudget.Add(ctx, 100)
	var b *TurnBudget
	if got := b.CheckExceeded(ctx, "standard"); got != "tokens" {
		t.Errorf("nil budget with exhausted aibudget: got %q, want tokens", got)
	}
}
