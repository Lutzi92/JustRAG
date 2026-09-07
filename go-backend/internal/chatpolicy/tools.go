package chatpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Routes are the keys a `chat_answer_tools_by_route` document may carry. The
// first three are the query classifier's labels; "global_synthesis" is the
// cross-cutting route a complex_reasoning turn takes when the global-synthesis
// classifier fires, and it wins over the query type's own entry.
var Routes = []string{"lookup", "enumeration", "complex_reasoning", "global_synthesis"}

// KnownAnswerTools is the closed set of built-in MCP tool names a route
// allowlist may name (W6-R15). Remote (per-KB / global-remote) MCP tools
// cannot be route-scoped in v1: their names are deployment data, so validating
// against them at save time would either reject a legitimate name or accept a
// typo. A cross-check test in internal/mcp/builtin pins this list against the
// registry, so adding or renaming a built-in fails there.
var KnownAnswerTools = []string{
	"chunk_read", "calculator", "keyword_search", "kb_search", "web_search",
	"code_exec", "memory_read", "memory_write", "recent_documents",
	"count_mentions", "document_outline", "sql_query", "table_query", "graph_search",
}

// answerToolsExcludedFromCatalog names built-ins that are in KnownAnswerTools
// (kept there unchanged — the internal/mcp/builtin cross-check test pins
// KnownAnswerTools against the MCP registry's full tool list) but that
// MCPDispatcher.AnswerToolCatalog (internal/chat/tool_dispatcher.go)
// deliberately never includes in the answer-time catalog it builds. Naming
// one in a chat_answer_tools_by_route route would otherwise validate (it IS
// a known tool) and then silently produce an EMPTY catalog for that route —
// the operator's restriction "just this tool" reads as "no tools at all".
// Rejecting it at save time with an explicit reason (S13, final review)
// turns that silent trap into an immediate 400.
var answerToolsExcludedFromCatalog = map[string]string{
	"code_exec": `"code_exec" is excluded from the answer-time tool catalog by design (MCPDispatcher.AnswerToolCatalog never includes it) — naming it in a chat_answer_tools_by_route route would validate but silently leave that route with no tools at all`,
}

// AnswerToolsByRoute maps a route to the tool names the answer-time tool
// catalog is filtered down to on that route. An entry with an EMPTY list is a
// real restriction ("no tools on this route"), distinct from a missing entry
// ("no restriction") — Allowlist's ok return is what separates them.
type AnswerToolsByRoute map[string][]string

// ParseAnswerToolsByRoute decodes and validates a stored tool document. An
// empty or whitespace-only value is the documented default and yields
// (nil, nil).
func ParseAnswerToolsByRoute(raw string) (AnswerToolsByRoute, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("chat_answer_tools_by_route must be a JSON object keyed by route")
	}

	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	var m AnswerToolsByRoute
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid tool-map JSON: %w", err)
	}
	if err := ensureEOF(dec); err != nil {
		return nil, err
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// ValidateAnswerToolsByRouteJSON is the save-time entry point used by
// internal/siteconfig.
func ValidateAnswerToolsByRouteJSON(raw string) error {
	_, err := ParseAnswerToolsByRoute(raw)
	return err
}

func (m AnswerToolsByRoute) validate() error {
	for route, tools := range m {
		if !slices.Contains(Routes, route) {
			return fmt.Errorf("unknown route %q (known: %s)", route, strings.Join(Routes, ", "))
		}
		seen := make(map[string]bool, len(tools))
		for _, name := range tools {
			if !slices.Contains(KnownAnswerTools, name) {
				return fmt.Errorf("route %q: unknown tool %q (known: %s)", route, name, strings.Join(KnownAnswerTools, ", "))
			}
			if reason, excluded := answerToolsExcludedFromCatalog[name]; excluded {
				return fmt.Errorf("route %q: %s", route, reason)
			}
			if seen[name] {
				return fmt.Errorf("route %q: duplicate tool %q", route, name)
			}
			seen[name] = true
		}
	}
	return nil
}

// Allowlist resolves the route for one turn. "global_synthesis" wins when the
// document carries that key AND the turn is a global-synthesis one; otherwise
// the query type's own entry decides. ok=false means the document says nothing
// about this route, i.e. no restriction — NOT "no tools".
//
// The returned slice aliases the map's value; callers must not mutate it.
func (m AnswerToolsByRoute) Allowlist(queryType string, globalSynthesis bool) ([]string, bool) {
	if len(m) == 0 {
		return nil, false
	}
	if globalSynthesis {
		if allow, ok := m["global_synthesis"]; ok {
			return allow, true
		}
	}
	if allow, ok := m[queryType]; ok {
		return allow, true
	}
	return nil, false
}
