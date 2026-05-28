package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// MCPDispatcher adapts *mcp.Registry to the chat package's
// ToolDispatcher interface. It lives in the chat package (rather than
// alongside mcp) because importing mcp/builtin from here would create
// a cycle — the builtin tools import internal/research, which imports
// chat. The conversion is small enough to inline.
type MCPDispatcher struct {
	Registry *mcp.Registry
}

// NewMCPDispatcher builds a dispatcher around the given registry. nil
// registries are allowed; the resulting dispatcher rejects every call
// with a clear error so a misconfigured deployment surfaces visibly
// instead of silently degrading to legacy behavior.
func NewMCPDispatcher(r *mcp.Registry) *MCPDispatcher {
	return &MCPDispatcher{Registry: r}
}

// Dispatch satisfies ToolDispatcher. Records the call duration +
// status into the AP-B4 per-turn ToolCallRecorder if one is bound
// to ctx; the recording call is a no-op when no recorder is set
// (eval harness, non-tool-aware paths) so backward-compat is full.
func (d *MCPDispatcher) Dispatch(ctx context.Context, kbID, name string, args json.RawMessage) (DispatchedToolResult, error) {
	if d == nil || d.Registry == nil {
		return DispatchedToolResult{}, mcp.ErrUnknownTool
	}
	start := time.Now()
	res, err := d.Registry.Dispatch(ctx, kbID, name, args)
	recordToolCallToCtx(ctx, name, start, err)
	if err != nil {
		return DispatchedToolResult{}, err
	}
	return DispatchedToolResult{
		Text:       res.Text,
		Chunks:     vectorChunksFromMCP(res.Chunks),
		Meta:       res.Meta,
		Structured: res.Structured,
	}, nil
}

// PlanToolCatalog builds the tool catalog the AP-B3 planner LLM
// sees. Reads the registry's per-KB tool list (built-in tools +
// any KB-bound MCP servers + global remote tools) and projects
// each one into the prompt-side PlanToolEntry shape.
//
// Why on the dispatcher (not on the registry directly): the
// dispatcher already owns the kb_id-aware view of the registry
// (Registry.List(kbID)). Adding the projection here keeps the
// chat package's handler wiring tiny.
func (d *MCPDispatcher) PlanToolCatalog(kbID string) []prompts.PlanToolEntry {
	if d == nil || d.Registry == nil {
		return nil
	}
	tools := d.Registry.List(kbID)
	out := make([]prompts.PlanToolEntry, 0, len(tools))
	for _, t := range tools {
		// memory_read / memory_write are session-state tools that
		// the planner shouldn't pick at plan time — they're meant
		// for the iterate loop to consult mid-conversation. Filter
		// them out here so the catalog stays focused on retrieval
		// + computation tools.
		if t.Name == "memory_read" || t.Name == "memory_write" {
			continue
		}
		out = append(out, prompts.PlanToolEntry{
			Name:            t.Name,
			Description:     t.Description,
			InputSchemaJSON: string(t.InputSchema),
		})
	}
	return out
}

// AnswerToolCatalog projects the dispatcher's per-KB tool list into the
// ai.ChatTool shape the streaming completion expects. Mirrors
// PlanToolCatalog but excludes code_exec — that tool is gated behind
// chat_code_exec_enabled and the answer-time path does not expose it
// in v1 (see docs/superpowers/specs/2026-05-13-llm-answer-tool-calling-design.md
// non-goals).
//
// Memory tools (memory_read, memory_write) ARE exposed here even though
// PlanToolCatalog excludes them. The reason for that asymmetry: the
// planner runs once at the start of a turn and shouldn't pick session-
// state tools at plan time; the answer-time LLM IS the iterate loop
// and that's exactly where memory recall/write belong.
//
// Called per-request from http_send.go so admin tool-config edits take
// effect on the next chat without a restart. Returns nil on a nil
// receiver OR a nil Registry — the answer-tools handler builds the
// catalog before checking chat_answer_tools_enabled, so a not-yet-
// wired dispatcher is a valid state.
func (d *MCPDispatcher) AnswerToolCatalog(kbID string) []ai.ChatTool {
	if d == nil || d.Registry == nil {
		return nil
	}
	tools := d.Registry.List(kbID)
	out := make([]ai.ChatTool, 0, len(tools))
	for _, t := range tools {
		if t.Name == "code_exec" {
			continue
		}
		out = append(out, ai.ChatTool{
			Type: "function",
			Function: ai.ChatToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}

// RunTool satisfies the AP-B3 DAGToolRunner interface. The dispatcher
// already knows about the kb_id injection (Registry.Dispatch
// validates it later), so this method just marshals the args map
// to JSON, optionally pre-fills `query` for tools that don't have
// it in args, and forwards. Returns SearchChunks for the chunk-
// shaped tools (kb_search, keyword_search, chunk_read); for non-
// chunk tools (calculator, sql_query, document_outline) it wraps
// the Text or Structured payload in a single synthetic chunk so
// the plan-execute accumulator can read it.
func (d *MCPDispatcher) RunTool(ctx context.Context, kbID, tool string, args map[string]any, query string) ([]vector.SearchChunk, error) {
	if args == nil {
		args = map[string]any{}
	}
	// kb_id is injected here so the LLM never has to know its own KB.
	args["kb_id"] = kbID
	// Convenience: if the args don't carry a "query" but the executor
	// has one (from buildNodeQuery), forward it. Tools that don't
	// declare a query input ignore it via the schema.
	if _, hasQuery := args["query"]; !hasQuery && query != "" {
		args["query"] = query
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("dispatch: marshal args: %w", err)
	}
	res, err := d.Dispatch(ctx, kbID, tool, raw)
	if err != nil {
		return nil, err
	}
	if len(res.Chunks) > 0 {
		return res.Chunks, nil
	}
	// Non-chunk tool output: synthesize a single chunk so downstream
	// dedup / scoring code paths that assume []SearchChunk don't need
	// a special case. ID is "tool:<name>" so deduplicateChunks doesn't
	// collapse the result with a real chunk that happens to share ID.
	body := res.Text
	if body == "" && len(res.Structured) > 0 {
		body = string(res.Structured)
	}
	if body == "" {
		return nil, nil
	}
	return []vector.SearchChunk{{
		ID:      "tool:" + tool,
		Content: body,
		Score:   1.0,
	}}, nil
}

// vectorChunksFromMCP rebuilds vector.SearchChunk values from the
// minimal protocol-shaped chunks the registry returns. Score and
// Content are sufficient for the downstream pipeline; fields the
// registry doesn't expose (RerankScore, Grade, ParentChunkID) are
// zero-valued and recovered by the LLM-context assembly layer if it
// ever needs them.
func vectorChunksFromMCP(chunks []mcp.ResultChunk) []vector.SearchChunk {
	if len(chunks) == 0 {
		return nil
	}
	out := make([]vector.SearchChunk, len(chunks))
	for i, c := range chunks {
		out[i] = vector.SearchChunk{
			ID:       c.ID,
			FileID:   c.FileID,
			FileName: c.FileName,
			Content:  c.Content,
			Score:    c.Score,
		}
	}
	return out
}
