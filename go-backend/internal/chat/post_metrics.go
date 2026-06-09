package chat

import (
	"context"
	"time"

	"github.com/justrag/go-backend/internal/safego"
)

// recordAgentDecision logs one agent_decisions row for the admin metrics
// panel (Phase 1 §1.4). Fire-and-forget in a fresh background context so
// the per-chat insert can outlive the request without touching the user-
// visible response path. Caller passes the latency they actually observed
// for the user-facing wait.
//
// AP-B4: also pulls the per-turn ToolCallRecorder snapshot (set up at
// turn start by the chat handler) and forwards it to the recorder so
// agent_decisions.tool_calls captures which tools the orchestrator
// actually dispatched.
func (h *Handler) recordAgentDecision(ctx context.Context, kbID, mode, outcome string, hops, rounds int, latencyMs int64) {
	if h.decisionRecorder == nil {
		return
	}
	// Snapshot now (in the request goroutine) so the detached insert
	// doesn't race the recorder. By this point the orchestrator is
	// done — no concurrent Record calls remain.
	var toolCalls []ToolCallRecord
	if rec := ToolCallRecorderFromContext(ctx); rec != nil {
		toolCalls = rec.Snapshot()
	}
	safego.GoCtx(ctx, func() {
		// Detached context: the chat handler's request context is
		// canceled the moment SSE finishes, but the analytics insert
		// should keep going for its own duration budget. Trace context
		// is propagated so the insert remains attached to the request's
		// span in the distributed trace.
		bgCtx, cancel := detachedContext(ctx, 5*time.Second)
		defer cancel()
		h.decisionRecorder.Record(bgCtx, kbID, mode, outcome, hops, rounds, int(latencyMs), toolCalls)
	})
}

// agentOutcomeFromEvents inspects buffered orchestrator events to pull out
// the final outcome plus hop / round counts. Returns ("", 0, 0) when the
// trajectory doesn't carry an `answer`-stage event yet (e.g. the path
// failed before emit). Operators should treat that as "the chat ran but
// no decision is recorded for it" rather than coercing it to a default
// bucket; the helper keeps the caller in control.
func agentOutcomeFromEvents(events []map[string]any) (outcome string, hops int, rounds int) {
	for _, e := range events {
		if t, ok := e["agentTrajectory"].(TrajectoryEvent); ok && t.Stage == "answer" {
			outcome = t.Decision
			// Plan-execute uses Step as the round count; agentic uses it
			// as the hop count. The orchestrators only set one of the
			// two histograms (RecordPlanExecuteDecision vs
			// RecordAgenticChatDecision), so the caller chooses which
			// field to populate via the `mode` argument when they call
			// recordAgentDecision.
			hops = t.Step
			rounds = t.Step
		}
	}
	return outcome, hops, rounds
}
