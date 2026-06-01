// Package agents implements the Phase 3 §3.2 supervisor + specialist
// pattern. The chat layer's monolithic plan-execute loop is split into
// role-scoped agents (Retriever, Enumerator) coordinated by a
// Supervisor that decides which specialist handles each sub-question.
//
// Each agent exposes a thin interface focused on the retrieval surface
// — synthesis (the final LLM completion that produces the user-facing
// answer) is left to the existing chat orchestrator's LLM call. v1
// keeps the Synthesizer role implicit; the chat layer's
// `ai.StreamCompletion` already plays that part. A future iteration
// can split it out if per-role model overrides become valuable.
package agents

import (
	"context"

	"github.com/justrag/go-backend/internal/vector"
)

// Input is the per-call payload the Supervisor hands to each
// specialist. It is intentionally small — anything orchestrator-wide
// (token budget, planning model, KB system prompt) lives outside the
// agent surface so specialists stay easy to test in isolation.
type Input struct {
	KbID     string
	Query    string
	Language string
	FileIDs  []string
	// GraphChunkIDs forwards the AP-C4 graph router's resolved
	// subgraph chunk IDs into the agent's SearchOptions. Empty
	// (default) preserves legacy behaviour. The Supervisor sets this
	// from chat.SupervisorChatParams.GraphChunkIDs when the
	// chat_graph_routing_inject_chunks gate is on.
	GraphChunkIDs []string
	// HyPESearch enables the HyPE query-time arm on this agent's
	// initial search (resolved from hype_search_enabled at dispatch).
	HyPESearch bool
}

// Output captures everything one specialist produced for one Input.
// `Chunks` flow into the supervisor's chunk accumulator; `Notes` is
// surfaced verbatim to the LLM context for specialists that produce
// structured findings (e.g. EnumeratorAgent's verified-matches list).
type Output struct {
	Chunks []vector.SearchChunk
	Notes  string
}

// Agent is the minimal contract every specialist implements. Returning
// an empty slice + nil error is allowed (a no-op result); returning an
// error short-circuits the supervisor's accumulation for that
// dispatch but does NOT abort the chat — the supervisor logs and
// proceeds with whatever other specialists produced.
type Agent interface {
	// Name returns a stable identifier used for routing logs and the
	// `rag_agent_dispatch_total{specialist}` metric label. Lower-case,
	// snake-case ("retriever", "enumerator").
	Name() string
	// Execute runs the agent against the given input. Implementations
	// MUST honor ctx cancellation — the supervisor enforces an upper
	// bound via context.WithTimeout.
	Execute(ctx context.Context, in Input) (Output, error)
}
