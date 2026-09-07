package chat

import (
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chatpolicy"
)

// restrictToolsForRoute filters catalog down to allow and wraps disp so a
// call outside allow is refused at Dispatch too — the catalog is a hint to
// the model, not a control; a prompt-injected model can still emit a call
// for a tool it never saw listed, which is why the same allowlist has to be
// enforced again at the dispatch boundary (W6-R8, mirroring RestrictedDispatcher's
// existing catalog+dispatch pairing for agent-level tool allowlists).
//
// A false restrict (the caller's Allowlist lookup returned ok=false, i.e.
// "the document says nothing about this route") returns disp and catalog
// unchanged — no wrapping, no filtering.
//
// allow may alias a policy document's own slice (chatpolicy.AnswerToolsByRoute.
// Allowlist returns the map's stored slice); it is only ever read here, copied
// into the returned dispatcher's allowed set, never mutated or reused as an
// output slice.
func restrictToolsForRoute(disp ToolDispatcher, catalog []ai.ChatTool, allow []string, restrict bool) (ToolDispatcher, []ai.ChatTool) {
	if !restrict {
		return disp, catalog
	}
	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[name] = true
	}
	filtered := make([]ai.ChatTool, 0, len(catalog))
	for _, t := range catalog {
		if allowed[t.Function.Name] {
			filtered = append(filtered, t)
		}
	}
	return &routeRestrictedDispatcher{inner: disp, allowed: allowed}, filtered
}

// shouldRunAnswerToolsLoop decides whether the answer-time tool loop should
// actually run. A route restriction (or the absence of an MCP dispatcher)
// can filter the catalog down to empty; calling RunAnswerWithTools with zero
// tools would be pointless scaffolding on the request path, so the caller
// falls through to the plain streaming answer instead.
func shouldRunAnswerToolsLoop(useAnswerTools bool, catalog []ai.ChatTool) bool {
	return useAnswerTools && len(catalog) > 0
}

// answerToolsRouteDecision names which route key AnswerToolsByRoute.Allowlist
// actually resolved for this turn, mirroring its own precedence exactly:
// "global_synthesis" wins only when the document carries that key AND the
// turn is a global-synthesis one. Used for the answer_tools_route trajectory
// event's Decision field so the reasoning panel shows which rule fired
// rather than just that some restriction applied.
func answerToolsRouteDecision(byRoute chatpolicy.AnswerToolsByRoute, queryType string, globalSynthesis bool) string {
	if globalSynthesis {
		if _, ok := byRoute["global_synthesis"]; ok {
			return "global_synthesis"
		}
	}
	return queryType
}

// resolveAnswerToolsRoute resolves chat_answer_tools_by_route for one turn:
// the allowlist, whether a restriction applies (mirrors
// AnswerToolsByRoute.Allowlist's ok — branch on it, not on len(allow)), and
// the answer_tools_route trajectory event's Decision/Reason.
//
// Folds in the controller ruling from Task 7 fix round 2: a turn whose
// query type is unknown (queryType == "" — e.g. a transform follow-up,
// which skips retrieval/classification entirely and so was never given a
// route) is treated as FULLY RESTRICTED whenever the document configures
// at least one route (len(byRoute) > 0). Rationale: an operator who
// restricts any route intends "no free-form tool use except where I said
// so", and an unclassified turn must not be a classification-based escape
// hatch around that intent — without this, a message the transform
// classifier accepts got the FULL, unrestricted catalog on a KB whose
// every other route was locked down. An EMPTY document (no routes
// configured at all) is unaffected: this ruling only ever narrows, and
// only when there is something configured to narrow against.
func resolveAnswerToolsRoute(byRoute chatpolicy.AnswerToolsByRoute, queryType string, globalSynthesis bool) (allow []string, ok bool, decision, reason string) {
	if queryType == "" {
		if len(byRoute) == 0 {
			return nil, false, "", ""
		}
		return []string{}, true, "unknown",
			"unknown query type (e.g. a transform follow-up, which skips retrieval/classification); treated as fully restricted because chat_answer_tools_by_route configures at least one route"
	}
	allow, ok = byRoute.Allowlist(queryType, globalSynthesis)
	if !ok {
		return allow, ok, "", ""
	}
	return allow, ok, answerToolsRouteDecision(byRoute, queryType, globalSynthesis), ""
}
