package chat

import (
	"context"
	"sync"
	"time"
)

// ToolCallRecord captures one MCP tool dispatch within a chat turn.
// Persisted into the agent_decisions.tool_calls JSONB column at the
// end of the turn so the admin "Tool-Mix" card can render the
// per-tool histogram without scraping logs.
//
// Status mirrors observability.RecordMCPToolCall's closed enum: ok
// for a successful dispatch, error for any tool-side or schema
// error, timeout when ctx was canceled by the per-call deadline.
type ToolCallRecord struct {
	Tool       string `json:"tool"`
	DurationMs int    `json:"duration_ms"`
	Status     string `json:"status"`
}

// ToolCallRecorder accumulates per-turn tool calls. The chat
// handler attaches one to ctx at turn start; MCPDispatcher.Dispatch
// appends to it on every call; recordAgentDecision pulls the
// snapshot at turn end and persists it.
//
// Thread-safe: the plan-execute DAG executor runs sibling nodes in
// parallel, each potentially dispatching a tool. The mutex is on
// the append path only — the snapshot copy at turn end runs after
// every dispatch goroutine has finished.
type ToolCallRecorder struct {
	mu    sync.Mutex
	calls []ToolCallRecord
}

// Record appends one entry. Safe to call from any goroutine; the
// chat handler's per-turn ctx isolates recorders so cross-turn
// races aren't a concern.
func (r *ToolCallRecorder) Record(c ToolCallRecord) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

// Snapshot returns a copy of the accumulated calls. Safe to call
// from the post-turn goroutine even while siblings are still
// dispatching — but the chat handler always waits for plan-execute
// to finish before recording, so in practice this is called when
// the recorder is already quiescent.
func (r *ToolCallRecorder) Snapshot() []ToolCallRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ToolCallRecord, len(r.calls))
	copy(out, r.calls)
	return out
}

type toolCallRecorderCtxKey struct{}

// WithToolCallRecorder binds r to ctx. Subsequent
// MCPDispatcher.Dispatch calls under this ctx append to r. Pass nil
// to express "do not record" — ToolCallRecorderFromContext returns
// nil and the dispatcher's recording branch becomes a no-op.
func WithToolCallRecorder(ctx context.Context, r *ToolCallRecorder) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, toolCallRecorderCtxKey{}, r)
}

// ToolCallRecorderFromContext returns the recorder bound to ctx, or
// nil if none was attached. The dispatcher's recording branch must
// nil-check; the recorder's methods are nil-safe but the lookup
// caller usually has its own conditional logic.
func ToolCallRecorderFromContext(ctx context.Context) *ToolCallRecorder {
	if v, ok := ctx.Value(toolCallRecorderCtxKey{}).(*ToolCallRecorder); ok {
		return v
	}
	return nil
}

// classifyDispatchStatus maps a Dispatch error to the closed-enum
// status label. Mirrors observability.RecordMCPToolCall's classifier
// so the admin panel and the Prometheus metric agree on what
// "timeout" means.
func classifyDispatchStatus(err error, ctxErr error) string {
	if err == nil {
		return "ok"
	}
	if ctxErr == context.DeadlineExceeded {
		return "timeout"
	}
	return "error"
}

// recordToolCallToCtx is the single-line helper the dispatcher calls
// after every Dispatch. start is the dispatch start time so duration
// covers the full call (including args marshal + registry lookup).
func recordToolCallToCtx(ctx context.Context, tool string, start time.Time, err error) {
	rec := ToolCallRecorderFromContext(ctx)
	if rec == nil {
		return
	}
	rec.Record(ToolCallRecord{
		Tool:       tool,
		DurationMs: int(time.Since(start).Milliseconds()),
		Status:     classifyDispatchStatus(err, ctx.Err()),
	})
}
