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
