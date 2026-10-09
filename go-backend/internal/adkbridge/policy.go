package adkbridge

import (
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/mcp"
)

// SideEffect classifies what a tool does outside the conversation.
type SideEffect string

const (
	SideEffectNone          SideEffect = "none"
	SideEffectExternalRead  SideEffect = "external_read"  // query leaves the house (web_search)
	SideEffectKBWrite       SideEffect = "kb_write"       // changes a KB's corpus
	SideEffectExternalWrite SideEffect = "external_write" // changes a system outside JustRAG
)

// Approval says whether a call pauses for the user before it runs.
type Approval string

const (
	ApprovalNever  Approval = "never"
	ApprovalAlways Approval = "always"
)

// ToolPolicy is enforced at dispatch time by every bridge tool.
type ToolPolicy struct {
	SideEffect   SideEffect
	RequiresRole string // minimum KB role of the RUNNING user
	Approval     Approval
}

var readOnly = ToolPolicy{SideEffect: SideEffectNone, RequiresRole: kbaccess.RoleView, Approval: ApprovalNever}

// builtinPolicies covers every built-in tool (pinned by TestBuiltinPoliciesCoverKnownTools)
// plus the planned KB-write tools that are not yet registered. Decisions (plan §0, §6b):
// web_search is approval-always (the query leaves the house) and, unlike in
// the legacy paths, needs no AllowPrivileged in the bridge; every write
// is approval-always and needs edit.
var builtinPolicies = map[string]ToolPolicy{
	"kb_search":        readOnly,
	"keyword_search":   readOnly,
	"graph_search":     readOnly,
	"chunk_read":       readOnly,
	"document_outline": readOnly,
	"table_query":      readOnly,
	"calculator":       readOnly,
	"count_mentions":   readOnly,
	"recent_documents": readOnly,
	"memory_read":      readOnly,
	// memory_write only touches the session memory (sessionmem, keyed by the
	// scope-injected chat_id) of the running chat, not long-term user memory.
	"memory_write":      readOnly,
	"sql_query":         readOnly, // privileged; gated separately
	"code_exec":         readOnly, // privileged; gated separately
	"web_search":        {SideEffect: SideEffectExternalRead, RequiresRole: kbaccess.RoleView, Approval: ApprovalAlways},
	"confluence_import": {SideEffect: SideEffectKBWrite, RequiresRole: kbaccess.RoleEdit, Approval: ApprovalAlways},
	"library_add_to_kb": {SideEffect: SideEffectKBWrite, RequiresRole: kbaccess.RoleEdit, Approval: ApprovalAlways},
}

// PolicyFor returns the policy for a tool name. Unknown tools (remote MCP
// servers) get the most restrictive policy: we cannot know what they do.
func PolicyFor(name string) ToolPolicy {
	if p, ok := builtinPolicies[name]; ok {
		return p
	}
	return ToolPolicy{SideEffect: SideEffectExternalWrite, RequiresRole: kbaccess.RoleEdit, Approval: ApprovalAlways}
}

var roleRank = map[string]int{kbaccess.RoleView: 1, kbaccess.RoleEdit: 2, kbaccess.RoleAdmin: 3, kbaccess.RoleOwner: 4}

// RoleAtLeast reports whether have meets need. Fails closed: an unknown have
// meets nothing, and an unknown or empty need is never met.
func RoleAtLeast(have, need string) bool {
	h, ok := roleRank[have]
	n, nok := roleRank[need]
	return ok && nok && h >= n
}

// privilegedInBridge reports whether a tool needs Scope.AllowPrivileged in
// the ADK bridge. web_search is exempt (user decision 2026-10-08): it is
// approval-always instead. mcp.PrivilegedTools — and the legacy paths that
// consult it — are unchanged.
func privilegedInBridge(name string) bool {
	return mcp.PrivilegedTools[name] && name != "web_search"
}
