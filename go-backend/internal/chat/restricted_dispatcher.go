package chat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/mcp"
)

// RestrictedDispatcher enforces a user-agent's tool allowlist at BOTH the
// catalog projection and the dispatch boundary. Catalog-only filtering is
// insufficient: a prompt-injected model can emit a call for a tool that was
// hidden from its catalog but is still registered — Dispatch is the
// load-bearing control.
type RestrictedDispatcher struct {
	inner   *MCPDispatcher
	allowed map[string]bool
}

// NewRestrictedDispatcher wraps inner with an allowlist. Privileged tools
// (mcp.PrivilegedTools) are stripped from the allowlist unless
// allowPrivileged — belt-and-suspenders on top of save-time validation,
// covering records saved before an admin turned the flag off.
func NewRestrictedDispatcher(inner *MCPDispatcher, toolNames []string, allowPrivileged bool) *RestrictedDispatcher {
	allowed := make(map[string]bool, len(toolNames))
	for _, n := range toolNames {
		if mcp.PrivilegedTools[n] && !allowPrivileged {
			continue
		}
		allowed[n] = true
	}
	return &RestrictedDispatcher{inner: inner, allowed: allowed}
}

// Dispatch satisfies ToolDispatcher.
func (d *RestrictedDispatcher) Dispatch(ctx context.Context, kbID, name string, args json.RawMessage) (DispatchedToolResult, error) {
	if !d.allowed[name] {
		return DispatchedToolResult{}, fmt.Errorf("tool %q is not allowed for this agent", name)
	}
	return d.inner.Dispatch(ctx, kbID, name, args)
}

// AnswerToolCatalog projects the inner catalog filtered to the allowlist.
func (d *RestrictedDispatcher) AnswerToolCatalog(kbID string) []ai.ChatTool {
	full := d.inner.AnswerToolCatalog(kbID)
	out := make([]ai.ChatTool, 0, len(full))
	for _, t := range full {
		if d.allowed[t.Function.Name] {
			out = append(out, t)
		}
	}
	return out
}

// routeRestrictedDispatcher enforces a per-route answer-tool allowlist
// (W6-R8, chat_answer_tools_by_route) at Dispatch — the same rationale as
// RestrictedDispatcher above: hiding a tool from the catalog is not a
// control, refusing its call is. Unlike RestrictedDispatcher, which wraps
// *MCPDispatcher concretely, this wraps the ToolDispatcher interface so it
// composes over either a plain MCPDispatcher or an already
// agent-restricted one; wrapping a RestrictedDispatcher yields the
// intersection of the two allowlists ("most restrictive wins" — each
// layer only ever narrows what a call can reach).
type routeRestrictedDispatcher struct {
	inner   ToolDispatcher
	allowed map[string]bool
}

// Dispatch satisfies ToolDispatcher.
func (d *routeRestrictedDispatcher) Dispatch(ctx context.Context, kbID, name string, args json.RawMessage) (DispatchedToolResult, error) {
	if !d.allowed[name] {
		return DispatchedToolResult{}, fmt.Errorf("tool %q is not allowed for this route", name)
	}
	return d.inner.Dispatch(ctx, kbID, name, args)
}
