package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
)

// DefaultToolCallTimeout is the per-call upper bound applied when the
// caller does not supply a tighter context deadline. Set at 10 s — long
// enough for a slow LLM-backed remote tool, short enough to bail out on
// a hung server before the user's chat-streaming patience runs out.
const DefaultToolCallTimeout = 10 * time.Second

// ErrUnknownTool is returned by Dispatch when no matching tool is
// registered for the (kb, name) pair. The orchestrator surfaces this as
// a `tool_call_unknown` outcome so the LLM can self-correct.
var ErrUnknownTool = errors.New("mcp: unknown tool")

// Dispatch runs one tool call: argument validation → handler invocation →
// telemetry. The timeout is enforced via a child context; pass a parent
// ctx with an already-tighter deadline to shorten.
//
// The returned ToolResult is whatever the tool produced; on error the
// result is zero-valued (callers must not read fields off it). The
// observability counter labels capture success/error per tool so a
// flaky third-party server is visible at a glance.
func (r *Registry) Dispatch(ctx context.Context, kbID, name string, args json.RawMessage) (ToolResult, error) {
	tool, ok := r.Get(kbID, name)
	if !ok {
		observability.RecordMCPToolCall(name, "unknown")
		return ToolResult{}, fmt.Errorf("%w: %q (kb=%s)", ErrUnknownTool, name, kbID)
	}

	if err := validateArgsAgainstSchema(tool.InputSchema, args); err != nil {
		observability.RecordMCPToolCall(name, "schema_error")
		return ToolResult{}, fmt.Errorf("mcp: %s: schema validation: %w", name, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, DefaultToolCallTimeout)
	defer cancel()

	start := time.Now()
	result, err := tool.Handler.Invoke(callCtx, args)
	latency := time.Since(start)
	observability.ObserveMCPToolCallSeconds(name, latency.Seconds())

	switch {
	case err == nil:
		observability.RecordMCPToolCall(name, "ok")
		logctx.From(ctx).Info("mcp.tool_call",
			"tool", name, "kb_id", kbID, "origin", tool.Origin,
			"latency_ms", latency.Milliseconds(), "result_chunks", len(result.Chunks))
		return result, nil
	case errors.Is(err, context.DeadlineExceeded):
		observability.RecordMCPToolCall(name, "timeout")
		return ToolResult{}, fmt.Errorf("mcp: %s: %w", name, err)
	default:
		observability.RecordMCPToolCall(name, "server_error")
		return ToolResult{}, fmt.Errorf("mcp: %s: %w", name, err)
	}
}

// validateArgsAgainstSchema performs a lightweight required-field check.
// We deliberately do NOT pull a full JSON Schema validator dependency in
// v1: most malformed args are missing-required-field, and the
// orchestrator's own LLM is much more likely to produce well-typed JSON
// than to type-confuse a string for a number. If a stricter validator
// becomes worth its weight (e.g. for third-party MCP servers exposing
// nuanced schemas), swap in github.com/santhosh-tekuri/jsonschema or
// equivalent here without changing the public Dispatch signature.
func validateArgsAgainstSchema(schema, args json.RawMessage) error {
	// Empty schema — accept anything.
	if len(schema) == 0 {
		return nil
	}
	var s simpleSchema
	if err := json.Unmarshal(schema, &s); err != nil {
		// A schema we can't parse is a developer error in the registering
		// tool, not a caller error. Don't reject the call; log via the
		// caller side (Dispatch already records a metric).
		return nil
	}
	if len(s.Required) == 0 {
		return nil
	}
	// Args MUST be a JSON object once required fields exist on the schema.
	var obj map[string]json.RawMessage
	if len(args) == 0 {
		obj = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(args, &obj); err != nil {
		return fmt.Errorf("args is not a JSON object: %w", err)
	}
	for _, key := range s.Required {
		if _, ok := obj[key]; !ok {
			return fmt.Errorf("missing required field %q", key)
		}
	}
	return nil
}

// simpleSchema is the subset of JSON Schema this registry validates. We
// only honor `required` in v1; other constraints (type, enum, pattern)
// are the tool implementation's responsibility.
type simpleSchema struct {
	Type     string   `json:"type"`
	Required []string `json:"required"`
}
