// Package mcp implements the Phase 2 §2.1 MCP (Model Context Protocol)
// integration: a tool registry the agentic chat orchestrators dispatch
// through. It supports two tool sources:
//
//  1. Built-in tools (in-process Go funcs) — kb_search, web_search,
//     memory_read, memory_write live here.
//  2. Remote MCP servers (JSON-RPC 2.0 over HTTP) configured per-KB via
//     the `mcp_servers` site-config JSON array.
//
// Both register the same Tool shape; the orchestrator never branches on
// "is this a built-in or a remote tool" — Dispatch handles the routing.
//
// Transport: HTTP-only in v1 (per plan §3 #3). stdio support is out of
// scope until a real use case demands it.
package mcp

import (
	"context"
	"encoding/json"
)

// Tool is the registry entry the orchestrator works with. Every tool —
// built-in or remote — produces and consumes JSON, schemas inclusive.
type Tool struct {
	// Name is the registry key. MUST be unique within a KB; collisions
	// are resolved with built-in-wins precedence (a remote server can
	// not shadow `kb_search` etc.).
	Name string `json:"name"`
	// Description is the human-readable tool description. Surfaced in
	// the trajectory panel and folded into the LLM tool-selection prompt.
	Description string `json:"description"`
	// InputSchema is the JSON Schema describing the args object. Stored
	// as raw JSON so it can be embedded verbatim in MCP tool-list
	// responses; the registry's lightweight required-field validator
	// reads it via SimpleSchema.
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	// Origin marks where the tool came from. "builtin" tools are in
	// process; "remote:<server-name>" tools dispatch via an MCP client.
	// Used by the trajectory panel and the per-tool metric label.
	Origin string `json:"origin"`
	// Handler invokes the tool. Owned by the registry; orchestrators
	// must NOT call this directly — they go through Registry.Dispatch
	// so timeouts, validation, and telemetry are applied uniformly.
	Handler ToolHandler `json:"-"`
}

// ToolHandler is the in-process invocation point. Built-in tools
// implement it directly with a closure; remote tools wrap an MCP client
// call.
type ToolHandler interface {
	Invoke(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

// ToolHandlerFunc is the closure adapter for built-in tools.
type ToolHandlerFunc func(ctx context.Context, args json.RawMessage) (ToolResult, error)

// Invoke satisfies ToolHandler.
func (f ToolHandlerFunc) Invoke(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	return f(ctx, args)
}

// ToolResult carries everything the orchestrator might need from one
// invocation. Concrete tools populate the relevant fields:
//
//	kb_search    → Chunks (the retrieved evidence)
//	web_search   → Text (a concatenated summary the LLM can read inline)
//	memory_*     → Text + Structured (notes payload)
//
// `Structured` is JSON the orchestrator forwards back to the LLM (e.g.
// when the tool wants to return a list of {entity, role, quote} matches).
// `Meta` is a small bag for trajectory rendering — `chunk_count`,
// `latency_ms`, etc. — and stays out of the prompt.
type ToolResult struct {
	Text       string          `json:"text,omitempty"`
	Chunks     []ResultChunk   `json:"chunks,omitempty"`
	Structured json.RawMessage `json:"structured,omitempty"`
	Meta       map[string]any  `json:"meta,omitempty"`
}

// ResultChunk is the minimal kb_search-shaped chunk surfaced through
// the tool layer. It mirrors vector.SearchChunk's user-facing fields
// without dragging the full struct into the mcp package's import graph
// (the chat orchestrator does the conversion in one place).
type ResultChunk struct {
	ID       string  `json:"id"`
	FileID   string  `json:"file_id"`
	FileName string  `json:"file_name"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
}

// ServerSpec is one entry of the per-KB `mcp_servers` site-config JSON.
// HTTP-only in v1; if stdio support arrives the spec grows a `transport`
// field.
type ServerSpec struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Validate returns an error when the spec is missing required fields.
// Caller (registry refresh) skips invalid entries and logs.
func (s ServerSpec) Validate() error {
	if s.Name == "" {
		return errMissing("name")
	}
	if s.URL == "" {
		return errMissing("url")
	}
	return nil
}

type errMissing string

func (e errMissing) Error() string { return "mcp: server spec missing field: " + string(e) }

// PrivilegedTools are excluded from user-created agents unless the
// agents_allow_privileged_tools site_config is on: code_exec executes code,
// sql_query reaches the (read-only) DB, web_search is an exfiltration channel
// for prompt-injected content. Enforced at agent-save validation AND at
// dispatch time (chat.RestrictedDispatcher).
var PrivilegedTools = map[string]bool{
	"code_exec":  true,
	"sql_query":  true,
	"web_search": true,
}
