package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/websearch"
)

// webSearchToolName is the built-in MCP tool the per-turn web-search
// opt-in exposes to the answer LLM (internal/mcp/builtin/web_search.go).
const webSearchToolName = "web_search"

// The request field webSearch is a *bool with three states:
//
//   - absent (nil): upstream behaviour, unchanged — the answer LLM gets
//     whatever chat_answer_tools_enabled gives it (API clients, public API).
//   - true: the user asked for web search on this turn.
//   - false: the user switched web search off for this turn — web_search is
//     dropped from the catalog AND refused at dispatch, even when
//     chat_answer_tools_enabled would otherwise hand it out.
func webSearchRequested(ws *bool) bool   { return ws != nil && *ws }
func webSearchSwitchedOff(ws *bool) bool { return ws != nil && !*ws }

// webSearchLogValue renders the tri-state for log lines.
func webSearchLogValue(ws *bool) string {
	switch {
	case ws == nil:
		return "absent"
	case *ws:
		return "true"
	default:
		return "false"
	}
}

// User-facing refusals. Deliberately generic: they name no admin config
// key — which switch is off goes to the server log (refuseWebSearchTurn).
const (
	// webSearchSkippedReason is the client-visible Reason of the
	// "web_search skipped" trajectory event; the specific cause is logged.
	webSearchSkippedReason   = "web search is not available for this turn"
	msgWebSearchUnavailable  = "Web search is not available."
	msgWebSearchNeedsStream  = "Web search is only available on streaming requests (stream=true)."
	msgWebSearchWithTeamTurn = "Web search cannot be combined with an agent or team selection."
)

// errWebSearchUnavailable marks the configuration reasons webSearchAvailable
// reports, as opposed to a failed site_config read.
var errWebSearchUnavailable = errors.New("web search unavailable")

// webSearchAvailable is the server-side availability check for a turn that
// asks for web search: the admin gate chat_web_search_enabled, a wired
// web_search tool, and the shared web_search_enabled + credentials check
// (websearch.Resolve — the same one the tool itself runs). Every flag it
// reads is global-only, so the check does not depend on the KB.
func (h *Handler) webSearchAvailable(ctx context.Context) error {
	if !ChatWebSearchEnabled(ctx, h.siteConfigReader) {
		return fmt.Errorf("%w: chat_web_search_enabled is off", errWebSearchUnavailable)
	}
	mcpDisp, _ := h.toolDispatcher.(*MCPDispatcher)
	if mcpDisp == nil || mcpDisp.Registry == nil {
		return fmt.Errorf("%w: no MCP tool dispatcher is wired", errWebSearchUnavailable)
	}
	if _, ok := mcpDisp.Registry.Get("", webSearchToolName); !ok {
		return fmt.Errorf("%w: the web_search tool is not registered", errWebSearchUnavailable)
	}
	if _, err := websearch.Resolve(ctx, h.siteConfigReader); err != nil {
		if websearch.IsUnavailable(err) {
			return fmt.Errorf("%w: %w", errWebSearchUnavailable, err)
		}
		return fmt.Errorf("web search availability: %w", err)
	}
	return nil
}

// refuseWebSearchTurn decides, before any side effect, whether a turn that
// asks for web search can honour it. It runs right after the body is
// validated — before the KB router, before resolveOrCreateChat and before
// the usage record — so a refusal leaves no chat row and no usage row.
//
// It rejects every case that is knowable from the request and the global
// config alone:
//
//   - web search is unavailable on this server (admin gate off,
//     web_search_enabled off, Google credentials missing, tool not wired);
//   - a non-streaming request: writeJSONResponse runs a single completion
//     with no tool loop, so the request would be answered without it;
//   - an agent/team selection on a non-Enhance turn: answer tools are off on
//     team-authored turns (persona-influenced findings in the prompt), so
//     web search could not run there either.
//
// The one skip that depends on work done later in the turn — a
// chat_answer_tools_by_route allowlist that drops web_search for the
// classified route — is reported as a "web_search" trajectory event instead
// (answerToolsForTurn).
//
// Returns status 0 when the turn may proceed.
func (h *Handler) refuseWebSearchTurn(ctx context.Context, body sendMessageRequest, streamMode bool) (status int, message string) {
	if !webSearchRequested(body.WebSearch) {
		return 0, ""
	}
	if err := h.webSearchAvailable(ctx); err != nil {
		if errors.Is(err, errWebSearchUnavailable) {
			logctx.From(ctx).Warn("chat.web_search.refused", "reason", err.Error())
			return http.StatusUnprocessableEntity, msgWebSearchUnavailable
		}
		logctx.From(ctx).Error("chat.web_search.config_read_failed", "error", err)
		return http.StatusInternalServerError, "failed to read site config"
	}
	if !streamMode {
		logctx.From(ctx).Warn("chat.web_search.refused", "reason", "non-streaming request")
		return http.StatusUnprocessableEntity, msgWebSearchNeedsStream
	}
	if teamTurnRequested(body) && h.teamLoader != nil {
		logctx.From(ctx).Warn("chat.web_search.refused", "reason", "agent/team selection")
		return http.StatusUnprocessableEntity, msgWebSearchWithTeamTurn
	}
	return 0, ""
}

// teamTurnRequested reports whether the request will be answered by its
// agent/team selection. SendMessage only uses the selection on a turn
// without an Enhance pass — judged AFTER resolveRegenerate, which clears
// body.Enhance (a regenerate re-answers the stored question unenhanced). The
// up-front check runs before that rewrite, so it has to apply it itself:
// otherwise a regenerate carrying a team and an Enhance value would pass
// the check and then run as a team turn.
func teamTurnRequested(body sendMessageRequest) bool {
	enhance := body.Enhance
	if body.RegenerateOfMessageID != "" {
		enhance = ""
	}
	return enhance == "" && (body.TeamID != "" || body.AgentID != "")
}

// answerToolsInput is what answerToolsForTurn needs to know about the turn.
type answerToolsInput struct {
	kbID string
	lang string
	// queryType / isGlobalSynthesis feed the per-route allowlist
	// (chat_answer_tools_by_route); queryType is "" on a transform follow-up.
	queryType         string
	isGlobalSynthesis bool
	webSearch         *bool
	// teamAuthored: a team/agent wrote this turn's prompt content (see
	// teamAuthoredTurn) — answer tools stay off.
	teamAuthored bool
	// library: a library-chat turn (no KB). It never gets answer-time tools
	// (P3-R5) — every catalog tool is KB-scoped and kbID is "".
	library bool
}

// answerToolSet is the answer LLM's tool configuration for one turn. The
// catalog and the dispatcher are always narrowed together, so what the model
// is shown and what a call can reach never drift apart.
type answerToolSet struct {
	dispatcher ToolDispatcher
	catalog    []ai.ChatTool
	// webSearchHint is prompts.WebSearchTurnHint when the user asked for web
	// search and web_search survived every restriction; "" otherwise.
	webSearchHint string
}

// systemPrompt returns base with the web-search hint PREPENDED — ahead of
// the retrieved CONTEXT block every answer prompt ends with, never appended
// after it. Prepending rather than splicing at a "CONTEXT:" marker because
// not every orchestrator's prompt has that marker, and the first occurrence
// can sit inside user-derived text (an attachment, long-term memory, a
// previous answer).
func (s answerToolSet) systemPrompt(base string) string {
	if s.webSearchHint == "" {
		return base
	}
	return s.webSearchHint + "\n\n" + base
}

// answerToolsForTurn resolves the answer-time tools for a turn and is shared
// by both answer tails (tryDeepChat and writeStreamingResponse). ok=false
// means the legacy tool-less stream runs.
//
// emit receives two kinds of trajectory event: answer_tools_route (W6-R8,
// unchanged) and "web_search" with Decision "skipped" whenever the user
// asked for web search and this turn cannot provide it — so a requested
// web search is never dropped silently.
func (h *Handler) answerToolsForTurn(ctx context.Context, in answerToolsInput, emit func(map[string]any)) (set answerToolSet, ok bool) {
	set, ok, skipReason := h.baseAnswerTools(ctx, in)
	if ok {
		byRoute := ChatAnswerToolsByRoute(ctx, h.siteConfigReader)
		if allow, restrict, decision, reason := resolveAnswerToolsRoute(byRoute, in.queryType, in.isGlobalSynthesis); restrict {
			set.dispatcher, set.catalog = restrictToolsForRoute(set.dispatcher, set.catalog, allow, true)
			routeEvt := TrajectoryEvent{
				Stage:    "answer_tools_route",
				Decision: decision,
				Reason:   reason,
				Findings: len(set.catalog),
			}
			if routeEvt.Reason == "" && len(set.catalog) == 0 {
				// Findings is omitempty, so a bare {stage, decision} frame
				// cannot be told apart from "no findings key" — this is the
				// one case an operator debugging a route restriction most
				// wants to see (the loop is about to be skipped entirely).
				routeEvt.Reason = "catalog empty; tool loop skipped"
			}
			emitTrajectory(emit, routeEvt, nil)
			if set.webSearchHint != "" && !hasTool(set.catalog, webSearchToolName) {
				// Don't tell the answer LLM to use a tool it no longer has.
				set.webSearchHint = ""
				skipReason = "the answer-tool allowlist for route " + decision + " does not include web_search"
			}
		}
	}
	if skipReason != "" {
		// The event goes to the client over SSE, so its reason is generic;
		// which gate, dispatcher or route allowlist dropped web_search is
		// server-side detail and goes to the log only.
		logctx.From(ctx).Warn("chat.web_search.skipped", "kb_id", in.kbID, "reason", skipReason)
		emitTrajectory(emit, TrajectoryEvent{Stage: "web_search", Decision: "skipped", Reason: webSearchSkippedReason}, nil)
	}
	return set, ok
}

// baseAnswerTools is the catalog before the per-route allowlist. Two ways
// in:
//
//   - chat_answer_tools_enabled (admin, global): the full per-KB answer
//     catalog with the unrestricted dispatcher, exactly as upstream —
//     except that webSearch=false drops web_search from both.
//   - webSearch=true with chat_answer_tools_enabled off: a dispatcher
//     restricted to web_search alone, whose allowPrivileged is
//     chat_web_search_enabled — the user's request opens that one tool and
//     nothing else (kb_search, sql_query, … are refused at dispatch).
//
// skipReason is non-empty when the user asked for web search and it is not
// in the returned catalog.
func (h *Handler) baseAnswerTools(ctx context.Context, in answerToolsInput) (set answerToolSet, ok bool, skipReason string) {
	requested := webSearchRequested(in.webSearch)
	if h.toolDispatcher == nil {
		if requested {
			return answerToolSet{}, false, "no tool dispatcher is wired on this server"
		}
		return answerToolSet{}, false, ""
	}
	if in.library {
		if requested {
			return answerToolSet{}, false, "answer tools are off on a library chat turn"
		}
		return answerToolSet{}, false, ""
	}
	if in.teamAuthored {
		if requested {
			return answerToolSet{}, false, "answer tools are off on a turn a team or agent authored"
		}
		return answerToolSet{}, false, ""
	}
	mcpDisp, _ := h.toolDispatcher.(*MCPDispatcher)

	if ChatAnswerToolsEnabled(ctx, h.siteConfigReader) {
		set.dispatcher = h.toolDispatcher
		if mcpDisp != nil {
			set.catalog = mcpDisp.AnswerToolCatalog(in.kbID)
		}
		if webSearchSwitchedOff(in.webSearch) {
			set.dispatcher, set.catalog = withoutTool(set.dispatcher, set.catalog, webSearchToolName)
		}
		if requested {
			if hasTool(set.catalog, webSearchToolName) {
				set.webSearchHint = prompts.WebSearchTurnHint(in.lang)
			} else {
				skipReason = "web_search is not in the answer-tool catalog"
			}
		}
		return set, true, skipReason
	}

	if !requested {
		return answerToolSet{}, false, ""
	}
	if mcpDisp == nil {
		return answerToolSet{}, false, "the tool dispatcher cannot be restricted to web_search"
	}
	restricted := NewRestrictedDispatcher(mcpDisp, []string{webSearchToolName}, ChatWebSearchEnabled(ctx, h.siteConfigReader))
	catalog := restricted.AnswerToolCatalog(in.kbID)
	if len(catalog) == 0 {
		// The up-front check (refuseWebSearchTurn) makes this unlikely; it
		// is reachable when an admin switches the gate off mid-turn.
		return answerToolSet{}, false, "web_search is not available (admin gate off or tool not registered)"
	}
	return answerToolSet{dispatcher: restricted, catalog: catalog, webSearchHint: prompts.WebSearchTurnHint(in.lang)}, true, ""
}

// withoutTool drops name from catalog and wraps disp so a call for it is
// refused at Dispatch too — the catalog is a hint to the model, not a
// control (same pairing as restrictToolsForRoute). Unlike an allowlist it
// leaves every other tool exactly as reachable as before.
func withoutTool(disp ToolDispatcher, catalog []ai.ChatTool, name string) (ToolDispatcher, []ai.ChatTool) {
	filtered := make([]ai.ChatTool, 0, len(catalog))
	for _, t := range catalog {
		if t.Function.Name != name {
			filtered = append(filtered, t)
		}
	}
	return &toolDeniedDispatcher{inner: disp, denied: name}, filtered
}

// toolDeniedDispatcher refuses exactly one tool and forwards every other
// call unchanged.
type toolDeniedDispatcher struct {
	inner  ToolDispatcher
	denied string
}

// Dispatch satisfies ToolDispatcher.
func (d *toolDeniedDispatcher) Dispatch(ctx context.Context, kbID, name string, args json.RawMessage) (DispatchedToolResult, error) {
	if name == d.denied {
		return DispatchedToolResult{}, fmt.Errorf("tool %q is switched off for this turn", name)
	}
	return d.inner.Dispatch(ctx, kbID, name, args)
}

func hasTool(catalog []ai.ChatTool, name string) bool {
	for _, t := range catalog {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}
