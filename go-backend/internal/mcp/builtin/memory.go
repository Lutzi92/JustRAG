package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/sessionmem"
)

// MemoryReadArgs is documented for completeness; the orchestrator only
// needs to inject `chat_id` (the LLM never sees this arg).
type MemoryReadArgs struct {
	ChatID string `json:"chat_id"`
	Limit  int    `json:"limit,omitempty"`
}

const memoryReadInputSchema = `{
  "type": "object",
  "required": ["chat_id"],
  "properties": {
    "chat_id": { "type": "string", "description": "Chat session identifier (orchestrator-injected)." },
    "limit":   { "type": "integer", "description": "Maximum number of notes to return (default 10)." }
  }
}`

// NewMemoryRead registers the memory_read built-in. It returns the
// session's recent notes as both Text (formatted for direct prompt
// inclusion) and Structured (raw JSON for tool-aware orchestrators).
func NewMemoryRead(store sessionmem.Store) mcp.Tool {
	return mcp.Tool{
		Name:        "memory_read",
		Description: "Read durable facts from the current chat session's memory.",
		InputSchema: json.RawMessage(memoryReadInputSchema),
		Handler:     mcp.ToolHandlerFunc(memoryReadHandler(store)),
	}
}

func memoryReadHandler(store sessionmem.Store) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args MemoryReadArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("memory_read: parse args: %w", err)
		}
		if args.ChatID == "" {
			return mcp.ToolResult{}, fmt.Errorf("memory_read: chat_id was not injected by the orchestrator")
		}
		mem, err := store.Get(ctx, args.ChatID)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("memory_read: %w", err)
		}
		structured, _ := json.Marshal(mem)
		return mcp.ToolResult{
			Text:       sessionmem.FormatPrompt(mem, "", args.Limit),
			Structured: structured,
			Meta: map[string]any{
				"note_count":    len(mem.Notes),
				"finding_count": len(mem.Findings),
			},
		}, nil
	}
}

// MemoryWriteArgs is the LLM-facing argument set for memory_write. The
// orchestrator injects `chat_id`; the LLM only supplies `text`.
type MemoryWriteArgs struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
	Source string `json:"source,omitempty"` // "agent" | "user_explicit"
}

const memoryWriteInputSchema = `{
  "type": "object",
  "required": ["chat_id", "text"],
  "properties": {
    "chat_id": { "type": "string", "description": "Chat session identifier (orchestrator-injected)." },
    "text":    { "type": "string", "description": "Durable fact to remember (≤280 chars)." },
    "source":  { "type": "string", "description": "agent | user_explicit (default: agent)." }
  }
}`

// NewMemoryWrite registers the memory_write built-in. The per-turn
// rate limit is enforced via a sessionmem.WriteCounter the chat
// handler attaches to the context at the start of each turn — see
// sessionmem.WithWriteCounter / sessionmem.FromContext. When no
// counter is on the context (tests, eval harness) the tool runs
// without a rate limit; production always attaches one.
//
// The counterOverride parameter exists only for tests that want to
// pin a deterministic counter onto every call regardless of context;
// production callers pass nil here.
func NewMemoryWrite(store sessionmem.Store, counterOverride *sessionmem.WriteCounter) mcp.Tool {
	return mcp.Tool{
		Name:        "memory_write",
		Description: "Persist a durable fact to the current chat session's memory. Use sparingly — for facts that should survive across turns (user preferences, scope decisions). Capped at 5 writes per turn.",
		InputSchema: json.RawMessage(memoryWriteInputSchema),
		Handler:     mcp.ToolHandlerFunc(memoryWriteHandler(store, counterOverride)),
	}
}

func memoryWriteHandler(store sessionmem.Store, counterOverride *sessionmem.WriteCounter) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args MemoryWriteArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("memory_write: parse args: %w", err)
		}
		if args.ChatID == "" {
			return mcp.ToolResult{}, fmt.Errorf("memory_write: chat_id was not injected by the orchestrator")
		}
		if args.Text == "" {
			return mcp.ToolResult{}, fmt.Errorf("memory_write: text is required")
		}
		counter := counterOverride
		if counter == nil {
			counter = sessionmem.FromContext(ctx)
		}
		if counter != nil {
			if !counter.Inc() {
				return mcp.ToolResult{}, sessionmem.ErrWriteRateLimited
			}
		}
		source := args.Source
		if source == "" {
			source = "agent"
		}
		err := store.AppendNote(ctx, args.ChatID, sessionmem.SessionNote{
			Text:   args.Text,
			Source: source,
		})
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("memory_write: %w", err)
		}
		return mcp.ToolResult{
			Text: "ok",
			Meta: map[string]any{"chat_id": args.ChatID},
		}, nil
	}
}
