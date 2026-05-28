package chat

import (
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/vector"
)

// trajectoryChunkPreview caps the per-step chunk preview at 5 entries so the
// SSE payload stays bounded — the full chunk text is still streamed via the
// existing `sources` event.
const trajectoryChunkPreview = 5

// TrajectoryEvent is the unified envelope for streaming agentic-chat progress
// to the frontend. The orchestrators (agentic_chat, plan_execute_chat) and
// the standard CRAG path emit one event per decision point; the frontend
// renders them as a "Reasoning steps" panel above the answer.
//
// Stage values used today (extensible via §1.3 of the agentic plan):
//
//	plan             — Plan-and-Execute planner emitted a sub-query list
//	iterate          — Plan-and-Execute iterate stage ran one round
//	hop              — Agentic-chat ran one hop (1-based step)
//	decision         — A non-search decision was taken (CRAG rewrite/abstain/proceed,
//	                   plateau early-stop, etc.)
//	answer           — Orchestrator finished and is about to hand off to LLM
//	refine_start     — AP-A2: factuality gate decided to refine; emitted BEFORE
//	                   the refine LLM call so the frontend can paint a
//	                   "Korrigiert nach Faktencheck" indicator while the call runs
//	refine_complete  — AP-A2: refine LLM finished; carries the word-level diff
//	                   and the v2 verifier verdict
type TrajectoryEvent struct {
	Stage        string         `json:"stage"`
	Step         int            `json:"step,omitempty"`
	Query        string         `json:"query,omitempty"`
	Queries      []string       `json:"queries,omitempty"`
	Chunks       []TrajChunkRef `json:"chunks,omitempty"`
	Decision     string         `json:"decision,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Findings     int            `json:"findings,omitempty"`
	Mode         string         `json:"mode,omitempty"`          // refine_start: surgical|holistic
	Diff         []DiffChunk    `json:"diff,omitempty"`          // refine_complete: word-level diff
	ClaimsBefore int            `json:"claims_before,omitempty"` // refine_*: trigger count
	ClaimsAfter  int            `json:"claims_after,omitempty"`  // refine_complete: residual count
	// RefinedText carries the full refined answer text for refine_complete
	// events that actually changed the answer. The word-level diff in Diff is
	// newline-lossy by construction (see refine_diff.go), so the frontend
	// rebuilds message content from this field rather than from Diff — the
	// diff is purely visual. Empty when the refine was a no-op.
	RefinedText string `json:"refined_text,omitempty"`
}

// TrajChunkRef is the per-step chunk preview surfaced to the trajectory
// panel. The full chunk content lives on the existing `sources` SSE event;
// this is just enough metadata to render "found 3 chunks from these files"
// without doubling the SSE payload.
type TrajChunkRef struct {
	Idx      int     `json:"idx"`
	FileName string  `json:"file_name"`
	Score    float64 `json:"score"`
}

// emitTrajectory is the single point of trajectory-event emission. emit may
// be nil (non-streaming callers, eval harness) — in that case the function
// is a no-op so callers never have to nil-check at the call site.
//
// Emits two payload shapes for one release: the new unified
// `agentTrajectory` envelope plus the legacy keys (`agenticHop`,
// `planExecuteStage`, etc.) so a cached frontend hitting a new backend
// keeps working until the shim is dropped.
func emitTrajectory(emit func(map[string]any), evt TrajectoryEvent, legacy map[string]any) {
	if emit == nil {
		return
	}
	observability.RecordTrajectoryEmit(evt.Stage)
	emit(map[string]any{"agentTrajectory": evt})
	if legacy != nil {
		emit(legacy)
	}
}

// topChunkScore returns the maximum Score across chunks, or 0 when the
// slice is empty. Used by the Phase 1 §1.3 plateau-stop check to track
// per-round answer-quality gradients without an extra retrieval pass.
func topChunkScore(chunks []vector.SearchChunk) float64 {
	top := 0.0
	for _, c := range chunks {
		if c.Score > top {
			top = c.Score
		}
	}
	return top
}

// chunkRefs builds a bounded preview of the chunks the orchestrator just
// retrieved. Returns nil for an empty input so the JSON-marshalled event
// omits the `chunks` field entirely.
func chunkRefs(chunks []vector.SearchChunk) []TrajChunkRef {
	if len(chunks) == 0 {
		return nil
	}
	n := min(len(chunks), trajectoryChunkPreview)
	out := make([]TrajChunkRef, n)
	for i := range n {
		out[i] = TrajChunkRef{
			Idx:      i + 1,
			FileName: chunks[i].FileName,
			Score:    chunks[i].Score,
		}
	}
	return out
}
