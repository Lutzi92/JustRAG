package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chatpolicy"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/usage"
)

// ---------------------------------------------------------------------------
// Language defaults
// ---------------------------------------------------------------------------

// defaultLanguage is the fallback conversation language when the client
// omits the `language` field or sends an unsupported value. German is the
// default because this project's first deployment serves a German-speaking
// university; the prompts in internal/prompts use German wording for the
// "de" branch and English for "en". Adding a new supported language means
// adding the BCP-47 code here and providing the prompt translations.
const defaultLanguage = "de"

// supportedLanguages enumerates the language codes the chat pipeline
// recognises. Anything not present falls back to defaultLanguage.
var supportedLanguages = map[string]bool{
	"de": true,
	"en": true,
}

// ---------------------------------------------------------------------------
// Request type
// ---------------------------------------------------------------------------

// sendMessageRequest is the parsed JSON body for the SendMessage endpoint.
type sendMessageRequest struct {
	Message          string   `json:"message"`
	ChatID           string   `json:"chatId"`
	ParentMessageID  string   `json:"parentMessageId"`
	Language         string   `json:"language"`
	SelectedFileIDs  []string `json:"selectedFileIds"`
	Enhance          string   `json:"enhance"` // "rewrite", "expand", "spell", or ""
	ReasoningEnabled bool     `json:"reasoningEnabled"`
	ReasoningLevel   string   `json:"reasoningLevel"` // "low", "medium", "high"
	AttachmentID     string   `json:"attachmentId"`
	ComparisonModes  []string `json:"comparisonModes"`
	// RegenerateOfMessageID names an AI message to answer again. The turn
	// then carries no question of its own: the stored question is re-answered
	// and the new answer becomes a sibling of the named one. Overrides
	// Message, Enhance and ParentMessageID — see internal/chat/regenerate.go.
	RegenerateOfMessageID string `json:"regenerateOfMessageId"`
	// TeamID / AgentID select a user-created agent team (or single agent)
	// for this turn — sticky per chat session (persisted on the chat row).
	// Mutually exclusive; TeamID wins if both are set.
	TeamID  string `json:"teamId"`
	AgentID string `json:"agentId"`
}

// SanitizeParentMessageID returns a pointer to id when it is a valid UUID, and
// nil otherwise (including the empty string). Client placeholder ids such as
// "temp-error-…" must never reach the uuid `messages` columns; treating an
// invalid id as absent avoids the 22P02 error and falls back to full-chat
// history. Shared with the public API handler, which parses the same field.
func SanitizeParentMessageID(id string) *string {
	if id == "" {
		return nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return nil
	}
	return &id
}

// ---------------------------------------------------------------------------
// SSE helpers
// ---------------------------------------------------------------------------

// sseBufPool reuses encode buffers across writeSSE calls to amortize the
// per-frame allocation a fresh json.Marshal would incur on a long stream.
var sseBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// writeSSE encodes data as JSON and writes a Server-Sent Events data frame,
// flushing the response writer if it supports http.Flusher. Marshal can only
// fail for structurally invalid Go values (channels, functions, cyclic data),
// so an error here means a programming bug — log it instead of silently
// emitting a malformed "data: \n\n" frame that the client can't parse.
//
// Fprintf write errors are surfaced via slog at debug level — the typical
// cause is a client disconnect mid-stream, which is expected, not anomalous.
// Long-running orchestrators should check ctx.Done() between iterations to
// stop work when the client has gone away; this log is observability, not
// flow control.
func writeSSE(ctx context.Context, w http.ResponseWriter, data any) {
	// json.Marshal reuses an internal encodeState pool, so it avoids the
	// per-frame json.Encoder struct allocation a fresh NewEncoder incurs on
	// long streams (chat responses emit hundreds of frames). It HTML-escapes
	// identically and returns no trailing newline, so nothing to trim.
	payload, err := json.Marshal(data)
	if err != nil {
		logctx.From(ctx).Error("chat: writeSSE marshal failed", "error", err, "type", fmt.Sprintf("%T", data))
		return
	}
	// Assemble the whole frame in a pooled buffer and emit it in one Write
	// (one Flush syscall) instead of fmt.Fprintf parsing "data: %s\n\n" per
	// token. The buffer reuse keeps GC pressure flat across the stream.
	buf := sseBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer sseBufPool.Put(buf)
	buf.WriteString("data: ")
	buf.Write(payload)
	buf.WriteString("\n\n")
	// Sliding per-frame deadline: bounds how long a half-open client can
	// block this goroutine (see httputil.SSEWriteTimeout).
	httputil.RearmSSEWriteDeadline(w)
	if _, werr := w.Write(buf.Bytes()); werr != nil {
		logctx.From(ctx).Debug("chat: writeSSE write failed (likely client disconnect)", "error", werr)
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeOpeningFrames emits a turn's opening metadata frame and, when the
// turn has conflicts, the W5-R7 `{"conflicts": […]}` frame directly after
// it. One function so the two streaming paths (the standard path and the
// orchestrator tail) cannot disagree about the ORDER: the conflict entries
// reference sources by their [N] index, so a client must already hold the
// source list by the time the badge arrives.
//
// The conflicts frame is omitted entirely when there is nothing to report,
// so a turn without conflicts streams exactly the frames it streamed before
// this existed and an old client cannot be confused by an empty array it
// does not know.
func writeOpeningFrames(ctx context.Context, w http.ResponseWriter, sources []ChatSource, enhancedQuery, chatID, userMsgID string, report *ConflictReport) {
	writeSSE(ctx, w, map[string]any{
		"sources":       sources,
		"enhancedQuery": enhancedQuery,
		"chatId":        chatID,
		"userMessageId": userMsgID,
	})
	if cs := ConflictsForWire(report); cs != nil {
		writeSSE(ctx, w, map[string]any{"conflicts": cs})
	}
}

// writeSSEDone writes the SSE stream terminator and flushes. Write errors
// are observed at debug level — see writeSSE for the rationale.
func writeSSEDone(ctx context.Context, w http.ResponseWriter) {
	httputil.RearmSSEWriteDeadline(w)
	if _, werr := fmt.Fprint(w, "data: [DONE]\n\n"); werr != nil {
		logctx.From(ctx).Debug("chat: writeSSEDone write failed (likely client disconnect)", "error", werr)
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// ---------------------------------------------------------------------------
// SendMessage handler
// ---------------------------------------------------------------------------

// SendMessage handles POST /api/kb/{id}/chat.
// It validates the request, resolves or creates a chat, runs the RAG pipeline,
// and streams the AI response as Server-Sent Events when ?stream=true, or
// returns a single JSON response otherwise.
// Auth + KB view permission are enforced by middleware in main.go.
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbID := r.PathValue("id")
	streamMode := r.URL.Query().Get("stream") == "true"

	ctx = logctx.WithKB(ctx, kbID)
	ctx = logctx.WithUser(ctx, user.ID)
	// Cache the enriched logger once so the dozens of logctx.From(ctx)
	// calls downstream in the chat pipeline reuse it instead of rebuilding
	// the 3-`.With(...)` chain on every line.
	ctx = logctx.Attach(ctx)

	ctx, span := observability.Tracer().Start(ctx, "chat.send_message")
	defer span.End()
	span.SetAttributes(
		attribute.String("chat.kb_id", kbID),
		attribute.Bool("chat.stream", streamMode),
	)

	// AP-A3: bind per-turn budgets onto ctx (defaults are unlimited so
	// existing eval / dev paths see no behaviour change).
	ctx = h.attachTurnBudget(ctx)

	// AP-B4: per-turn tool-call recorder. Attached unconditionally —
	// orchestrators that don't dispatch any MCP tools snapshot an
	// empty slice, which the agent_decisions row stores as the JSONB
	// '[]' default. recordAgentDecision snapshots before its own
	// detached insert goroutine.
	ctx = WithToolCallRecorder(ctx, &ToolCallRecorder{})

	body, ok := parseAndValidateMessage(w, r, user.ID)
	if !ok {
		return
	}

	// Never on a regenerate: the KB was decided when the question was first
	// answered, and re-routing would either move the answer to a different
	// corpus than the one above it in the thread, or — since the chat is bound
	// to its KB — 403 in resolveOrCreateChat. The router would also be reading
	// the CLIENT's message, which a regenerate is about to discard.
	if body.RegenerateOfMessageID == "" {
		ctx, kbID = h.maybeRouteKB(ctx, r, user.ID, kbID, body.Message)
	}

	// Per-KB config: once kb_id is final, swap to a request-local handler whose
	// reader + SearchService overlay this KB's kb_site_configs overrides. No-op
	// (returns h) when the KB has no overrides.
	h = h.forKB(ctx, kbID)

	chatID, ok := h.resolveOrCreateChat(ctx, w, body.ChatID, kbID, user.ID, body.Message)
	if !ok {
		return
	}

	// Usage ledger: one row per accepted turn. Placed after maybeRouteKB so the
	// event names the KB actually answered from, after parseAndValidateMessage
	// so a malformed body records nothing, and after resolveOrCreateChat so a
	// chatId the caller does not own (403) records nothing either — aligning
	// web with publicapi, which runs the identical ownership check and already
	// records after it.
	if h.usageRecorder != nil {
		h.usageRecorder.Record(ctx, usage.Event{
			KbID:     kbID,
			UserID:   user.ID,
			APIKeyID: auth.APIKeyIDFromContext(ctx),
			Surface:  usage.SurfaceWeb,
		})
	}

	lang := body.Language
	if !supportedLanguages[lang] {
		lang = defaultLanguage
	}

	// Current-date line for date-aware answers (empty when disabled).
	dateLine := SystemPromptDateLine(ctx, h.siteConfigReader, lang)

	// Guard the uuid `messages` columns against client placeholder ids: a
	// non-uuid parent (e.g. "temp-error-…" left by a failed send) triggers
	// SQLSTATE 22P02 on both the ancestor lookup and the message insert, which
	// silently drops the whole conversation history. Treating it as absent falls
	// back to full-chat history instead.
	parentMsgID := SanitizeParentMessageID(body.ParentMessageID)

	// "Antwort neu generieren": no new question is written, the answer becomes
	// a sibling of the one being replaced, and the turn re-answers the STORED
	// question — resolveRegenerate rewrites body.Message/body.Enhance for it.
	anchor := turnAnchor{ParentMessageID: parentMsgID}
	if body.RegenerateOfMessageID != "" {
		regen, ok := h.resolveRegenerate(ctx, w, chatID, &body)
		if !ok {
			return
		}
		anchor.Regenerate = regen
		parentMsgID = regen.HistoryParentID
	}

	// Recent turns, loaded once: the answer-history block (every answer
	// path) and the transform-follow-up route both need them. CondenseFollowUp
	// keeps its own load because its signature is shared with publicapi.
	//
	// A regenerate carries its own history rather than re-loading it: both
	// loaders read a nil parent as "no anchor given" and fall back to the
	// WHOLE chat, which for a regenerate at the start of a chat would hand
	// the answer being replaced straight back to the answer LLM.
	var convRows []MessageRow
	if anchor.Regenerate != nil {
		convRows = anchor.Regenerate.HistoryRows
	} else {
		convRows = h.loadConversationRows(ctx, chatID, parentMsgID)
	}
	answerHistory := h.answerHistory(ctx, convRows)

	// Transform follow-up: "kannst du das als Tabelle erstellen?" asks to
	// reformat the PREVIOUS ANSWER. Re-retrieving here is wrong twice over —
	// a fresh corpus search redefines the scope (all events instead of the
	// ones just shown), and the condensed query can even hijack the
	// corpus-table map-reduce. Route the turn to a retrieval-free completion
	// over the previous answer instead. Checked before CondenseFollowUp so a
	// positive saves the condense LLM call.
	if body.Enhance == "" && ChatTransformFollowupEnabled(ctx, h.siteConfigReader) && IsTransformFollowUpQuery(body.Message) {
		if prev := lastAIMessage(convRows); prev != nil {
			h.handleTransformFollowUp(ctx, w, span, body, chatID, kbID, lang, user.ID, anchor, prev, convRows, streamMode)
			return
		}
	}

	// Condensing needs a preceding turn. A regenerate at the start of a chat
	// has none, and its nil anchor would send CondenseFollowUp back to the
	// whole-chat fallback — see the convRows note above.
	searchQuery := body.Message
	if anchor.Regenerate == nil || anchor.Regenerate.HistoryParentID != nil {
		searchQuery, _ = CondenseFollowUp(ctx, h.aiResolver, h.store, chatID, parentMsgID, body.Message, kbID, lang)
	}
	rawQuery := RawQueryForRetrieval(ChatCondenseKeepRawEnabled(ctx, h.siteConfigReader), body.Message, searchQuery)

	cls := h.classifyQuery(ctx, searchQuery, body.Enhance, kbID, lang)

	var kbSystemPrompt string
	ctx, kbSystemPrompt = h.assembleSystemPrompt(ctx, chatID, kbID, user.ID, lang, searchQuery)

	reasoningLevel := resolveReasoningLevel(body)

	// AP-C4 graph-routing decision shared across all dispatch paths —
	// the resolved subgraph chunks thread through whichever orchestrator
	// handles the turn (Supervisor / Plan-Execute / Agentic / DeepChat /
	// standard PrepareChatContext).
	graphDec, graphChunkIDs, bridgeChunks := h.resolveGraphRouting(ctx, kbID, searchQuery, cls.QueryType)

	// In-chat document comparison: an attachment + at least one mode + the
	// feature gate routes this turn to the comparison orchestrator (handled
	// inside tryDeepChat's dispatch switch as the highest-priority case).
	// Computed here so the deep-chat gate fires even when the query itself is
	// not classified complex_reasoning.
	compareEnabled := CompareEnabled(ctx, h.siteConfigReader)
	runCompare := willRunComparison(compareEnabled, body.AttachmentID, body.ComparisonModes)

	// Explicit user-created team/agent selection: beats every flag-driven
	// orchestrator (but not the comparison attachment), runs regardless of
	// query classification — the team router is the triage. Enhance passes
	// stay retrieval-transform turns and bypass it. Resolution itself runs
	// unconditionally (even on Enhance turns) so persistChatSelection always
	// reflects the pick's *current* validity — a load failure or a
	// since-deleted team no longer gets persisted as if it still worked.
	resolvedSel, teamSelReason := h.resolveTeamSelection(ctx, body, kbID)
	h.persistChatSelection(ctx, chatID, resolvedSel)
	var teamSel *teamSelection
	if body.Enhance == "" {
		teamSel = resolvedSel
	}

	// Follow-up over a prior upload: when an attachment rides along but this is
	// NOT a fresh comparison run (no modes), inject a capped block of the
	// uploaded document + prior findings into the KB system prompt so the user
	// can ask about the upload without re-uploading. Best-effort: load failures
	// and foreign-owned attachments are silently skipped. Threads through both
	// the deep-chat path and the standard PrepareChatContext path because both
	// receive kbSystemPrompt below.
	if shouldInjectFollowUpContext(body.AttachmentID, runCompare, h.attachmentStore != nil) {
		if att, aerr := h.attachmentStore.Get(ctx, body.AttachmentID); aerr == nil && att.UserID == user.ID {
			kbSystemPrompt = prependBlock(buildFollowUpContext(att), kbSystemPrompt)
		}
	}

	// W6-R6 / Wave-6 fix round 1: the orchestrator policy is resolved HERE,
	// above the complexity gate, not inside tryDeepChat. `isComplex` is
	// definitionally "the classifier said complex_reasoning" (classifyQuery
	// sets UseHyDE && UseMultiQuery only on that branch), so a policy read
	// below this line could never steer a lookup or an enumeration turn — and
	// `when.query_type` exists precisely to name those.
	turnPol := h.resolveTurnPolicy(ctx, kbID, cls.QueryType, searchQuery, lang, body, answerHistory)

	// Deep chat: complex streaming queries get a 2-step research agent.
	// Comparison turns also route through tryDeepChat (its switch dispatches
	// the comparison orchestrator) regardless of complexity classification.
	//
	// The policy arm only ever WIDENS this gate — with no policy, or with no
	// matching rule, opensDeepChatDispatch() is false and the condition is
	// byte-identical to the pre-W6-R6 one, so a lookup turn still never
	// enters tryDeepChat. A rule naming "standard" does not widen it either
	// (see turnPolicy.opensDeepChatDispatch); its rule index is recorded on
	// the standard path below.
	isComplex := cls.UseHyDE && cls.UseMultiQuery
	deepChatAttempted := false
	if shouldTryDeepChat(isComplex, runCompare, teamSel != nil, streamMode, turnPol) {
		deepChatAttempted = true
		if handled := h.tryDeepChat(ctx, w, r, chatID, kbID, lang, dateLine, searchQuery, rawQuery, cls.QueryType, kbSystemPrompt, reasoningLevel, body, anchor, graphDec, graphChunkIDs, bridgeChunks, answerHistory, teamSel, teamSelReason, turnPol); handled {
			return
		}
		// Deep chat failed — fall through to standard path.
	}

	// W6-R16: "a forced orchestrator whose dependencies are missing falls back
	// through the existing orchestrator-error → PrepareChatContext path" — and
	// that fall-back has to be VISIBLE, or an operator who forces a route with
	// missing dependencies sees a perfectly ordinary answer and no reason why.
	// tryDeepChat's own event buffer is discarded when it returns false, so
	// the event is re-emitted into the standard path's buffer below; the log
	// line covers the non-streaming and the buffer-less cases.
	policyFellThrough := deepChatAttempted && turnPol.opensDeepChatDispatch()
	if policyFellThrough {
		logctx.From(ctx).Warn("chat.orchestrator_policy.fallthrough",
			"policy_rule", turnPol.gate.RuleIndex,
			"orchestrator", turnPol.gate.Orchestrator,
			"mode", turnPol.gate.Mode,
			"kb_id", kbID,
		)
	}

	// Buffer trajectory/CRAG events emitted during PrepareChatContext —
	// SSE headers are not yet written, so they have to be replayed
	// later (mirror the tryDeepChat buffer-and-replay pattern). When
	// streamMode is false collectEmit stays nil so the helper short-
	// circuits and the slice never allocates.
	var bufferedTrajectory []map[string]any
	var collectEmit func(data map[string]any)
	if streamMode {
		collectEmit = func(data map[string]any) {
			bufferedTrajectory = append(bufferedTrajectory, data)
		}
	}
	if policyFellThrough {
		idx := turnPol.gate.RuleIndex
		emitTrajectory(collectEmit, TrajectoryEvent{
			Stage:      "orchestrator_policy",
			Decision:   "fallthrough",
			Reason:     "forced orchestrator " + turnPol.gate.Orchestrator + " could not run; answering on the standard path",
			Mode:       turnPol.gate.Mode,
			PolicyRule: &idx,
		}, nil)
	}
	params := ChatContextParams{
		KbID:                  kbID,
		SearchQuery:           searchQuery,
		Language:              lang,
		CurrentDateLine:       dateLine,
		Enhance:               body.Enhance,
		FileIDs:               body.SelectedFileIDs,
		HyDE:                  cls.UseHyDE,
		MultiQuery:            cls.UseMultiQuery,
		KbSystemPrompt:        kbSystemPrompt,
		QueryType:             cls.QueryType,
		Emit:                  collectEmit,
		GraphSubgraphChunkIDs: graphChunkIDs,
		BridgeChunks:          bridgeChunks,
		RecencyLister:         h.recencyLister,
		TabularRouter:         h.tabularRouter,
		RawQuery:              rawQuery,
		FileDates:             h.fileDates,
	}

	// AP-C4 trajectory event (standard path): the decision was computed
	// in resolveGraphRouting above; emit the event here so the
	// streaming client's reasoning panel sees it interleaved with the
	// other PrepareChatContext events.
	if graphDec.Fired && collectEmit != nil {
		queries := make([]string, 0, len(graphDec.MatchedEntities))
		for _, e := range graphDec.MatchedEntities {
			queries = append(queries, e.CanonicalName)
		}
		emitTrajectory(collectEmit,
			TrajectoryEvent{
				Stage:    "decision",
				Decision: "graph_traversal",
				Reason:   graphDec.Outcome,
				Queries:  queries,
				Findings: len(graphDec.MatchedEntities),
			},
			nil)
	}

	chatCtx, err := PrepareChatContext(ctx, h.aiResolver, h.searchService, h.siteConfigReader, params)
	if err != nil {
		logctx.From(ctx).Error("chat.send: prepare context", "error", err, "chat_id", chatID, "user_id", user.ID, "kb_id", kbID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to prepare context")
		return
	}

	enhancedQuery := chatCtx.EnhancedQuery
	hasEnhanced := enhancedQuery != ""
	var enhancedQueryPtr *string
	if hasEnhanced {
		enhancedQueryPtr = &enhancedQuery
	}
	userMsg, err := h.resolveTurnUserMessage(ctx, AddMessageParams{
		ChatID:        chatID,
		Role:          "user",
		Content:       body.Message,
		IsEnhanced:    hasEnhanced,
		EnhancedQuery: enhancedQueryPtr,
	}, anchor)
	if err != nil {
		logctx.From(ctx).Error("chat.send: save user message", "error", err, "chat_id", chatID, "user_id", user.ID, "kb_id", kbID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save user message")
		return
	}

	rp := chatResponseParams{
		span:               span,
		chatID:             chatID,
		kbID:               kbID,
		lang:               lang,
		userMessage:        body.Message,
		reasoningLevel:     reasoningLevel,
		userMsgID:          userMsg.ID,
		chatCtx:            chatCtx,
		bufferedTrajectory: bufferedTrajectory,
		chatStartTime:      time.Now(),
		history:            answerHistory,
		// W6-R6: a rule that FORCED the standard route pinned this turn, so
		// the agent_decisions row records it. standardPathRule() returns nil
		// for every other case — including a fall-through from a failed
		// orchestrator, where the forced route did not answer and claiming it
		// did would corrupt the measurement (that row stays NULL, and the
		// trajectory event above is where the fall-through is visible).
		policyRule: standardPathPolicyRule(turnPol, deepChatAttempted),
		// W6-R8: feeds the per-route answer-tool allowlist in
		// writeStreamingResponse.
		queryType:         cls.QueryType,
		isGlobalSynthesis: IsGlobalSynthesisQuery(searchQuery),
	}
	if streamMode {
		h.writeStreamingResponse(ctx, w, rp)
		return
	}
	h.writeJSONResponse(ctx, w, rp)
}

// ---------------------------------------------------------------------------
// Orchestrator policy (W6-R6) — resolved once per turn in SendMessage
// ---------------------------------------------------------------------------

// turnPolicy is the once-per-turn resolution of chat_orchestrator_policy,
// handed from SendMessage into tryDeepChat so the document is read and the
// signal bag built exactly once.
//
// `gate` is a PRE-decision: it answers "may this turn reach the deep-chat
// dispatch at all", using the orchestrator flags read straight from
// site_config. It deliberately knows nothing about the comparison / team /
// corpus-table arms — those are re-evaluated (and win) inside
// SelectOrchestratorWithPolicy, which is the authoritative decision for the
// turn. The two agree by construction: same document, same signals, and
// policyEnabledFromConfig builds the same map OrchestratorInputs.policyEnabled
// does (pinned by TestPolicyEnabledMapsAgree).
type turnPolicy struct {
	policy  chatpolicy.OrchestratorPolicy
	signals chatpolicy.Signals
	gate    chatpolicy.Decision
}

// opensDeepChatDispatch reports whether the policy alone justifies entering
// tryDeepChat for a turn the complexity classifier would not have sent there.
//
// A rule naming "standard" is deliberately excluded: the standard path is
// where such a turn already goes, so widening the gate for it would only swap
// PrepareChatContext for RunDeepChat — a routing change the operator did not
// ask for. Its rule index is recorded on the standard path instead.
func (tp turnPolicy) opensDeepChatDispatch() bool {
	return tp.gate.Applied && tp.gate.Orchestrator != chatpolicy.OrchestratorStandard
}

// standardPathRule is the rule index the standard path should record: set only
// when a rule FORCED the standard route for this turn. nil in every other
// case, including a fall-through from a failed orchestrator (the forced route
// did not answer, so the row must not claim it did).
func (tp turnPolicy) standardPathRule() *int {
	if !tp.gate.Applied || tp.gate.Orchestrator != chatpolicy.OrchestratorStandard {
		return nil
	}
	idx := tp.gate.RuleIndex
	return &idx
}

// shouldTryDeepChat is SendMessage's deep-chat entry gate, extracted so the
// W6-R6 widening is directly testable (a condition written inline in a 300-line
// handler is only reachable through an end-to-end turn).
//
// The first three arms are the pre-W6-R6 gate verbatim. The fourth is the
// policy, and it can only ever ADD turns: with no policy, or with no matching
// rule, or with a rule naming "standard", opensDeepChatDispatch() is false and
// this function returns exactly what the original condition returned — which
// is what keeps a lookup or enumeration turn out of tryDeepChat by default.
//
// streamMode still gates everything: tryDeepChat writes SSE, so a
// non-streaming turn cannot use it no matter what the policy says. A rule
// forcing a non-standard orchestrator on a non-streaming turn therefore does
// not apply, and correctly records no rule index.
func shouldTryDeepChat(isComplex, runCompare, teamSelected, streamMode bool, tp turnPolicy) bool {
	return (isComplex || runCompare || teamSelected || tp.opensDeepChatDispatch()) && streamMode
}

// standardPathPolicyRule is what the standard path records in
// agent_decisions.policy_rule. A rule that forced "standard" pinned the turn,
// so it is recorded — but only when the deep-chat dispatch was never
// attempted. When it WAS attempted and fell through, the route the rule named
// did not answer; the row stays NULL and the fall-through is reported as a
// trajectory event and a log line instead.
func standardPathPolicyRule(tp turnPolicy, deepChatAttempted bool) *int {
	if deepChatAttempted {
		return nil
	}
	return tp.standardPathRule()
}

// policyEnabledFromConfig reads the six policy-nameable orchestrator flags
// from site_config into the enabled map chatpolicy.Decide expects. It is the
// reader-sourced twin of OrchestratorInputs.policyEnabled, which builds the
// same map from already-resolved inputs inside tryDeepChat.
func policyEnabledFromConfig(ctx context.Context, reader SiteConfigReader) map[string]bool {
	planExecute := ChatPlanExecuteEnabled(ctx, reader)
	return map[string]bool{
		"drift":            ChatDriftEnabled(ctx, reader),
		"longcontext":      ChatLongContextEnabled(ctx, reader),
		"supervisor":       ChatSupervisorEnabled(ctx, reader),
		"plan_execute":     planExecute,
		"plan_execute_dag": planExecute,
		"agentic":          ChatAgenticEnabled(ctx, reader),
	}
}

// resolveTurnPolicy reads chat_orchestrator_policy and, when a policy exists,
// builds the signal bag and the entry-gate decision.
//
// Everything past the read is skipped for an empty policy — the default, and
// the fail-soft result of an unparseable stored document — so a deployment
// without a policy pays one site_config read and neither of the two regex
// classifiers, and every gate below behaves exactly as it did before W6-R6.
func (h *Handler) resolveTurnPolicy(
	ctx context.Context,
	kbID, queryType, searchQuery, lang string,
	body sendMessageRequest,
	answerHistory []ai.ChatHistoryEntry,
) turnPolicy {
	tp := turnPolicy{
		policy: ChatOrchestratorPolicy(ctx, h.siteConfigReader),
		gate:   chatpolicy.Decision{RuleIndex: -1},
	}
	if len(tp.policy) == 0 {
		return tp
	}
	tp.signals = chatpolicy.Signals{
		QueryType:        queryType,
		GlobalSynthesis:  IsGlobalSynthesisQuery(searchQuery),
		Enumeration:      IsEnumerationQuery(searchQuery, lang),
		RecencyListing:   IsRecencyListingQuery(searchQuery),
		HasFileSelection: len(body.SelectedFileIDs) > 0,
		HistoryTurns:     len(answerHistory),
		KBID:             kbID,
	}
	tp.gate = chatpolicy.Decide(tp.policy, tp.signals, policyEnabledFromConfig(ctx, h.siteConfigReader))
	return tp
}

// ---------------------------------------------------------------------------
// tryDeepChat — 2-step research agent for complex streaming queries
// ---------------------------------------------------------------------------

// tryDeepChat attempts the deep chat path for complex queries. Returns true if
// it handled the response (success or error written to SSE), false if the caller
// should fall through to the standard chat path.
func (h *Handler) tryDeepChat(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	chatID, kbID, lang, dateLine, searchQuery, rawQuery, queryType, kbSystemPrompt, reasoningLevel string,
	body sendMessageRequest,
	anchor turnAnchor,
	graphDec GraphTraversalDecision,
	graphChunkIDs []string,
	bridgeChunks map[string]int,
	answerHistory []ai.ChatHistoryEntry,
	teamSel *teamSelection,
	teamSelReason string,
	turnPol turnPolicy,
) bool {
	ctx, span := observability.Tracer().Start(ctx, "chat.deep_chat")
	defer span.End()

	// Run deep chat research. Planning-phase LLM calls (RewriteQuery /
	// ExpandQuery / HyDE / MultiQuery) go to the configured enrichment
	// model when one is set, so deep chat stays responsive while the
	// streamed answer still uses the main ChatModel.
	deepParams := DeepChatParams{
		KbID:            kbID,
		ChatID:          chatID,
		Message:         body.Message,
		Query:           searchQuery,
		Language:        lang,
		FileIDs:         body.SelectedFileIDs,
		KbSystemPrompt:  kbSystemPrompt,
		ReasoningLevel:  reasoningLevel,
		PlanningModel:   EnrichmentModel(ctx, h.siteConfigReader),
		QueryType:       queryType,
		GraphChunkIDs:   graphChunkIDs,
		BridgeChunks:    bridgeChunks,
		HyPESearch:      HyPESearchEnabled(ctx, h.siteConfigReader),
		CurrentDateLine: dateLine,
	}

	// We need to save the user message before starting SSE, because SSE
	// headers must be sent before any data. However, if deep chat fails,
	// we'll have already committed the user message — that's acceptable
	// because the standard path would save it anyway.

	// Build an emit function that buffers events until SSE headers are set.
	// We'll collect progress events and replay them once we commit to SSE.
	var progressEvents []map[string]any
	collectEmit := func(data map[string]any) {
		progressEvents = append(progressEvents, data)
	}

	// Surface a silent team-selection degradation: the user explicitly picked
	// a team/agent but resolveTeamSelection couldn't load it (deleted,
	// disabled, empty team) and fell back to nil. Emitted here — rather than
	// in resolveTeamSelection itself, which runs in SendMessage before the
	// emit buffer exists — so the UI's reasoning panel shows the downgrade
	// instead of silently answering off the standard/other-orchestrator path.
	// Only fires on turns that reach tryDeepChat at all (gate: isComplex ||
	// runCompare || teamSel != nil); a failed pick on an otherwise-simple,
	// non-streaming-orchestrator query never enters this function, so no
	// event is emitted for it — acceptable, since there is no orchestrator
	// run to attach the event to on that path.
	if teamSel == nil && teamSelReason != "" {
		emitTrajectory(collectEmit, TrajectoryEvent{Stage: "decision", Decision: "team_unavailable", Reason: teamSelReason}, nil)
	}

	// AP-C4 trajectory event for the orchestrator path (mirrors the
	// emission in the standard path's PrepareChatContext flow). Done
	// here so the streaming client's reasoning panel sees the
	// graph_traversal decision regardless of which orchestrator runs.
	if graphDec.Fired {
		queries := make([]string, 0, len(graphDec.MatchedEntities))
		for _, e := range graphDec.MatchedEntities {
			queries = append(queries, e.CanonicalName)
		}
		emitTrajectory(collectEmit,
			TrajectoryEvent{
				Stage:    "decision",
				Decision: "graph_traversal",
				Reason:   graphDec.Outcome,
				Queries:  queries,
				Findings: len(graphDec.MatchedEntities),
			},
			nil)
	}

	// Phase 3 §F: when the master switch is on AND the query is the
	// complex_reasoning class AND the caller didn't ask for an explicit
	// Enhance pass, route to the agentic loop. Otherwise fall through
	// to the existing 2-step RunDeepChat. (The streaming-mode predicate
	// is implicit here — tryDeepChat is only invoked from the streaming
	// branch of SendMessage.)
	var chatCtx *ChatContext
	var err error
	// comparisonTeamAnswered is true only when a selected team/agent
	// actually WROTE the comparison summary (RunTeamChat succeeded). It
	// feeds teamAuthoredTurn below, which decides message attribution —
	// a team merely selected but whose summary run failed must not be
	// attributed.
	var comparisonTeamAnswered bool

	agenticEnabled := ChatAgenticEnabled(ctx, h.siteConfigReader)
	planExecuteEnabled := ChatPlanExecuteEnabled(ctx, h.siteConfigReader)
	supervisorEnabled := ChatSupervisorEnabled(ctx, h.siteConfigReader)
	driftEnabled := ChatDriftEnabled(ctx, h.siteConfigReader)
	longContextEnabled := ChatLongContextEnabled(ctx, h.siteConfigReader)
	corpusTableEnabled := ChatCorpusTableEnabled(ctx, h.siteConfigReader)

	// In-chat document comparison takes top priority over every orchestrator
	// (including corpus-table) — an explicit attachment + modes is an
	// unambiguous user intent, so it should never be hijacked by a query
	// classifier. attachmentStore being non-nil is the infrastructure guard.
	comparisonReady := h.attachmentStore != nil &&
		willRunComparison(CompareEnabled(ctx, h.siteConfigReader), body.AttachmentID, body.ComparisonModes)

	// The full precedence ladder now lives in SelectOrchestrator
	// (orchestrator_select.go) — this call site only resolves inputs.
	// Rationale for each gate, preserved from the original inline ladder:
	//   - team: explicit user-created team/agent selection takes priority
	//     over every flag-driven orchestrator (but not the comparison
	//     attachment) — the team router is the triage, so it also
	//     supersedes the corpus-table heuristic classifier.
	//   - corpus-table: two-stage gate (keyword classifier + optional LLM
	//     confirm). Both higher-priority explicit-intent gates short-circuit
	//     it so the classifier — and especially the optional confirm LLM
	//     call — never runs on comparison or team turns.
	//   - drift: full iterative DRIFT takes priority over the other
	//     orchestrators, but only for global-synthesis queries (its primer
	//     is wasted on narrow lookups). Narrow gate → it rarely intercepts;
	//     everything else falls through to
	//     supervisor/plan-execute/agentic/standard unchanged.
	//   - longcontext (W3-R5): the System-2 wide-retrieval route, promoted
	//     from a PrepareChatContext-internal branch — which the streaming
	//     path never reaches — to a real orchestrator. Same narrow
	//     global-synthesis gate as drift, and below it: DRIFT is the more
	//     specific answer for those queries when its KG communities exist.
	//   - supervisor: Phase 3 §3.2 supervisor takes priority over both
	//     plan-execute and agentic when the gate is on. Plan §3.2 ship
	//     gate: "non-regression on lookup/enumeration; ≥ 2 pp gain on
	//     complex_reasoning MRR or nDCG" — only the eval harness can decide
	//     whether the gate flips, so this gate stays per-deployment opt-in.
	orchIn := OrchestratorInputs{
		QueryType:             queryType,
		EnhanceRequested:      body.Enhance != "",
		ComparisonReady:       comparisonReady,
		TeamSelected:          teamSel != nil,
		CorpusTableEnabled:    corpusTableEnabled,
		CorpusChunksAvailable: h.corpusChunks != nil,
		IsCorpusQuery:         IsCorpusComparisonQuery(searchQuery, lang),
		CorpusRouterLLMOn:     ChatCorpusTableRouterLLMEnabled(ctx, h.siteConfigReader),
		DriftEnabled:          driftEnabled,
		IsGlobalSynthesis:     IsGlobalSynthesisQuery(searchQuery),
		LongContextEnabled:    longContextEnabled,
		SupervisorEnabled:     supervisorEnabled,
		PlanExecuteEnabled:    planExecuteEnabled,
		AgenticEnabled:        agenticEnabled,
	}

	// W6-R6: the operator's per-query orchestrator policy, resolved once per
	// turn by SendMessage (resolveTurnPolicy) — it also gates whether this
	// function is reached at all for a non-complex turn. Re-deciding it HERE
	// rather than reusing turnPol.gate is deliberate: only this call site
	// knows the comparison / team / corpus-table arms, and W6-R16 requires
	// those to win over any rule.
	//
	// The corpus-table confirmation is an LLM call; SelectOrchestrator
	// invokes this at most once, and only after every higher-priority gate
	// has already failed and the cheap keyword classifier has already
	// matched — preserving the original short-circuit that kept this call
	// off the hot path.
	orch, policyDec := SelectOrchestratorWithPolicy(orchIn, turnPol.policy, turnPol.signals, func() bool {
		return ai.ConfirmCorpusComparison(ctx, h.aiResolver, searchQuery, kbID, lang, ChatCorpusTableModel(ctx, h.siteConfigReader))
	})

	// policyRule is what agent_decisions.policy_rule records: the rule that
	// actually PINNED the route. A matched-but-not-applied prefer rule left
	// the ladder in charge, so it must stay NULL on the row — the trajectory
	// event below is where that near-miss is visible.
	var policyRule *int
	if policyDec.Applied {
		idx := policyDec.RuleIndex
		policyRule = &idx
	}
	if policyDec.Matched {
		idx := policyDec.RuleIndex
		decision, reason := policyDec.Orchestrator, "rule matched (mode="+policyDec.Mode+")"
		if !policyDec.Applied {
			decision = "fallthrough"
			reason = "prefer rule matched but " + policyDec.Orchestrator + " is disabled"
		}
		emitTrajectory(collectEmit, TrajectoryEvent{
			Stage:      "orchestrator_policy",
			Decision:   decision,
			Reason:     reason,
			Mode:       policyDec.Mode,
			PolicyRule: &idx,
		}, nil)
	}

	// The `considered` denominator for rag_longcontext_route_total: the
	// operator gate is on and the turn was eligible, but the keyword
	// classifier did not fire. `fired` is recorded inside RunLongContextChat.
	if longContextEnabled && orchIn.complexAndUnenhanced() && !orchIn.IsGlobalSynthesis {
		observability.RecordLongContextRoute("considered", "")
	}

	logctx.From(ctx).Info("rag.deep_chat.dispatch",
		"supervisor_enabled", supervisorEnabled,
		"plan_execute_enabled", planExecuteEnabled,
		"agentic_enabled", agenticEnabled,
		"drift_enabled", driftEnabled,
		"longcontext_enabled", longContextEnabled,
		"corpus_table_enabled", corpusTableEnabled,
		"query_type", queryType,
		"enhance", body.Enhance,
		"will_run_comparison", orch == OrchComparison,
		"will_run_corpus_table", orch == OrchCorpusTable,
		"will_run_team", orch == OrchTeam,
		"will_run_drift", orch == OrchDrift,
		"will_run_longcontext", orch == OrchLongContext,
		"will_run_supervisor", orch == OrchSupervisor,
		"will_run_plan_execute", orch == OrchPlanExecute,
		"will_run_agentic", orch == OrchAgentic,
		"orchestrator", string(orch),
	)

	plateau := ResolvePlateauConfig(ctx, h.siteConfigReader)
	// Phase 2 §2.1: wire the MCP tool dispatcher into the plan-execute
	// orchestrator only when (a) the handler was constructed with a
	// dispatcher AND (b) the site_config gate is on. Either condition
	// being false leaves the orchestrator on the legacy direct-search
	// path so the rollout stays per-deployment opt-in.
	var planExecuteTools ToolDispatcher
	if h.toolDispatcher != nil && ChatUseMCPTools(ctx, h.siteConfigReader) {
		planExecuteTools = h.toolDispatcher
	}

	switch orch {
	case OrchComparison:
		userID := ""
		if u := auth.UserFromContext(ctx); u != nil {
			userID = u.ID
		}
		cmpParams := ComparisonChatParams{
			KbID:            kbID,
			ChatID:          chatID,
			Language:        lang,
			Model:           CompareModel(ctx, h.siteConfigReader),
			Modes:           body.ComparisonModes,
			MaxSections:     CompareMaxSections(ctx, h.siteConfigReader),
			Concurrency:     CompareConcurrency(ctx, h.siteConfigReader),
			PeersPerSection: ComparePeersPerSection(ctx, h.siteConfigReader),
		}
		structured := func(c context.Context, prompt, system, kb, model string, spec *ai.StructuredSpec) (string, error) {
			res, gerr := ai.GenerateCompletionStructured(c, h.aiResolver, prompt, system, kb, model, spec)
			if gerr != nil {
				return "", gerr
			}
			return res.Content, nil
		}
		cmpDeps := ComparisonDeps{Store: h.attachmentStore, Search: h.searchService.Search, Structured: structured}
		var cmpFindings []Finding
		chatCtx, cmpFindings, err = RunComparisonChat(ctx, cmpDeps, body.AttachmentID, userID, cmpParams, collectEmit)
		if err == nil && chatCtx != nil {
			// Drive the post-dispatch streamer to emit a prose SUMMARY over the
			// findings. The streamer (below) feeds chatCtx.SystemPrompt +
			// body.Message to ai.StreamCompletionWithHistory and ignores
			// chatCtx.Context, so the findings text is embedded into the system
			// prompt here (ComparisonSummaryPrompt's wording refers to "the
			// following findings"). The structured findings were already streamed
			// as a comparisonFindings event; this prose is the gist.
			// Plain path: byte-identical to the pre-2026-08 behaviour (see
			// 669ae5e) — deliberately built with an EMPTY kbSystemPrompt.
			// This task is about agent routing, not about the comparison
			// prompt's composition; changing what this shipping opt-in
			// feature emits, as a side effect of sharing a builder with the
			// new team path, would need its own decision and its own
			// baseline, not a ride-along here. Also used as the fail-soft
			// fallback when a selected team's RunTeamChat call fails: a
			// resolved-but-unused pick degrades to exactly what a comparison
			// turn without any team selection would have produced.
			plainSummaryPrompt := comparisonSummaryPromptFor("", lang, cmpFindings)
			if teamSel != nil {
				// The findings extraction stays on chat_compare_model — a
				// persona prompt does not help a structured-outputs call, and
				// a team router per section would be expensive and pointless.
				// Only the prose summary runs through the selected team/agent.
				//
				// Unlike the plain path above, this path is NEW — it has no
				// prior behaviour to preserve — so it carries kbSystemPrompt,
				// matching the OrchTeam case's own convention of passing
				// kbSystemPrompt through as KbSystemPrompt.
				teamSummaryPrompt := comparisonSummaryPromptFor(kbSystemPrompt, lang, cmpFindings)
				var dispatcher *MCPDispatcher
				if h.toolDispatcher != nil {
					if md, ok := h.toolDispatcher.(*MCPDispatcher); ok {
						dispatcher = md
					}
				}
				tp := BuildTeamParams(ctx, TeamParamsInput{
					KbID: kbID, ChatID: chatID, Query: searchQuery, Language: lang,
					CurrentDateLine: dateLine, KbSystemPrompt: teamSummaryPrompt,
					FileIDs: body.SelectedFileIDs,
					Team:    teamSel.team, Agent: teamSel.agent,
					DeriveToolPolicy: true,
					SiteCfg:          h.siteConfigReader, SearchService: h.searchService, ToolDispatcher: dispatcher,
				})
				teamCtx, terr := RunTeamChat(ctx, h.aiResolver, h.searchService, tp, collectEmit)
				if terr != nil {
					// Fail-soft like everywhere else on the team path: the
					// findings were already streamed as a comparisonFindings
					// event, and a failure here must not discard them. Falls
					// back to the PLAIN prompt (not teamSummaryPrompt) — a
					// team that never wrote anything must not leave its
					// kbSystemPrompt-carrying prompt behind either.
					logctx.From(ctx).Warn("comparison.team_summary_failed", "error", terr)
					emitTrajectory(collectEmit, TrajectoryEvent{
						Stage: "decision", Decision: "team_unavailable",
						Reason: terr.Error(),
					}, nil)
					chatCtx.SystemPrompt = plainSummaryPrompt
					comparisonTeamAnswered = false
				} else {
					// Merge the comparison stage's peer chunks with the
					// team's: citation validation and the source list must
					// see both stages, or every finding source from stage 1
					// drops out.
					teamCtx.FinalChunks = mergeComparisonChunks(teamCtx.FinalChunks, chatCtx.FinalChunks)
					teamCtx.Sources, teamCtx.Context = buildChatSourcesAndContext(teamCtx.FinalChunks)
					chatCtx = teamCtx
					comparisonTeamAnswered = true
				}
			} else {
				chatCtx.SystemPrompt = plainSummaryPrompt
			}
		}

	case OrchTeam:
		var dispatcher *MCPDispatcher
		if h.toolDispatcher != nil {
			if md, ok := h.toolDispatcher.(*MCPDispatcher); ok {
				dispatcher = md
			}
		}
		params := BuildTeamParams(ctx, TeamParamsInput{
			KbID: kbID, ChatID: chatID, Query: searchQuery, Language: lang,
			CurrentDateLine: dateLine, KbSystemPrompt: kbSystemPrompt,
			FileIDs: body.SelectedFileIDs, GraphChunkIDs: graphChunkIDs, BridgeChunks: bridgeChunks,
			Team: teamSel.team, Agent: teamSel.agent,
			DeriveToolPolicy: true,
			SiteCfg:          h.siteConfigReader, SearchService: h.searchService, ToolDispatcher: dispatcher,
		})
		chatCtx, err = RunTeamChat(ctx, h.aiResolver, h.searchService, params, collectEmit)

	case OrchCorpusTable:
		chatCtx, err = RunCorpusTableChat(ctx, h.aiResolver, h.searchService, h.corpusChunks, CorpusTableParams{
			KbID:           kbID,
			Query:          searchQuery,
			Language:       lang,
			FileIDs:        body.SelectedFileIDs,
			KbSystemPrompt: kbSystemPrompt,
			Model:          ChatCorpusTableModel(ctx, h.siteConfigReader),
			MaxFiles:       ChatCorpusTableMaxFiles(ctx, h.siteConfigReader),
			Concurrency:    ChatCorpusTableConcurrency(ctx, h.siteConfigReader),
		}, collectEmit)

	case OrchDrift:
		driftParams := DriftChatParams{
			KbID:            kbID,
			Query:           searchQuery,
			Language:        lang,
			CurrentDateLine: dateLine,
			FileIDs:         body.SelectedFileIDs,
			KbSystemPrompt:  kbSystemPrompt,
			PlanningModel:   ResolveFastTierModel(ctx, h.siteConfigReader, "chat_drift_model"),
			MaxFollowups:    ChatDriftMaxFollowups(ctx, h.siteConfigReader),
			PrimerTopK:      ChatDriftPrimerTopK(ctx, h.siteConfigReader),
			SearchTopK:      ChatDriftSearchTopK(ctx, h.siteConfigReader),
			GraphChunkIDs:   graphChunkIDs,
			BridgeChunks:    bridgeChunks,
			HyPESearch:      HyPESearchEnabled(ctx, h.siteConfigReader),
		}
		chatCtx, err = RunDriftChat(ctx, h.aiResolver, h.searchService, driftParams, collectEmit)

	case OrchLongContext:
		// Knobs are resolved inside RunLongContextChat from h.siteConfigReader
		// (the per-KB-overlaid reader SendMessage installed via forKB), so the
		// per-KB `chat_longcontext_mode` override is honoured. Only the fields
		// that come from THIS request are set here.
		chatCtx, err = RunLongContextChat(ctx, h.aiResolver, h.searchService, h.siteConfigReader, LongContextParams{
			KbID:            kbID,
			Query:           searchQuery,
			Language:        lang,
			CurrentDateLine: dateLine,
			KbSystemPrompt:  kbSystemPrompt,
			FileIDs:         body.SelectedFileIDs,
			RawQuery:        rawQuery,
			GraphChunkIDs:   graphChunkIDs,
			BridgeChunks:    bridgeChunks,
			HyPESearch:      HyPESearchEnabled(ctx, h.siteConfigReader),
			Emit:            collectEmit,
		})

	case OrchSupervisor:
		// Resolved HERE, not at wiring time: h is the per-KB handler
		// (SendMessage swapped it via forKB before calling tryDeepChat),
		// so this reader carries the KB's kb_site_configs overrides. The
		// router's own cfgFn only ever sees the global reader.
		var tabularCfg *TabularRouterConfig
		if h.siteConfigReader != nil {
			cfg := ResolveTabularRouterConfig(ctx, h.siteConfigReader)
			tabularCfg = &cfg
		}
		// Resolve the conflict knobs only behind the master flag: the OFF
		// path (the default everywhere) then costs one bool read instead of
		// four config lookups per Supervisor turn. The zero value skips the
		// pass, which is exactly what an OFF flag means.
		var conflictCfg ConflictConfig
		if ChatConflictSurfacingEnabled(ctx, h.siteConfigReader) {
			conflictCfg = ResolveConflictConfig(ctx, h.siteConfigReader)
		}
		supervisorParams := SupervisorChatParams{
			KbID:            kbID,
			Query:           searchQuery,
			Language:        lang,
			CurrentDateLine: dateLine,
			FileIDs:         body.SelectedFileIDs,
			KbSystemPrompt:  kbSystemPrompt,
			PlanningModel:   EnrichmentModel(ctx, h.siteConfigReader),
			GraphChunkIDs:   graphChunkIDs,
			BridgeChunks:    bridgeChunks,
			HyPESearch:      HyPESearchEnabled(ctx, h.siteConfigReader),
			MultiSpecialist: ChatSupervisorMultiSpecialist(ctx, h.siteConfigReader),
			TabularRouter:   h.tabularRouter,
			RawQuery:        rawQuery,

			TabularRouterConfig:      tabularCfg,
			SufficientContextEnabled: ChatSufficientContextEnabled(ctx, h.siteConfigReader),
			SufficientContextModel:   ResolveFastTierModel(ctx, h.siteConfigReader, "chat_sufficient_context_model"),
			ConflictConfig:           conflictCfg,
			FileDates:                h.fileDates,
		}
		chatCtx, err = RunSupervisorChat(ctx, h.aiResolver, h.searchService, supervisorParams, collectEmit)

	case OrchPlanExecute:
		planningModel := ChatPlanExecuteModel(ctx, h.siteConfigReader)
		if planningModel == "" {
			planningModel = EnrichmentModel(ctx, h.siteConfigReader)
		}
		planExecuteParams := PlanExecuteParams{
			KbID:            kbID,
			Query:           searchQuery,
			Language:        lang,
			CurrentDateLine: dateLine,
			FileIDs:         body.SelectedFileIDs,
			KbSystemPrompt:  kbSystemPrompt,
			PlanningModel:   planningModel,
			MaxSubQueries:   ChatPlanExecuteMaxSubQueries(ctx, h.siteConfigReader),
			MaxIterations:   ChatPlanExecuteMaxIterations(ctx, h.siteConfigReader),
			TokenBudget:     ChatPlanExecuteTokenBudget(ctx, h.siteConfigReader),
			Plateau:         plateau,
			Tools:           planExecuteTools,
			// W6-R16: a "plan_execute_dag" policy rule is plan-execute with
			// the DAG pinned on for this turn — the name has no
			// chat.Orchestrator of its own. OR, never override: a
			// deployment with chat_plan_execute_dag already on keeps it.
			DAG:           ChatPlanExecuteDAG(ctx, h.siteConfigReader) || policyDec.ForceDAG,
			MaxDAGDepth:   ChatPlanExecuteMaxDAGDepth(ctx, h.siteConfigReader),
			MaxDAGNodes:   ChatPlanExecuteMaxDAGNodes(ctx, h.siteConfigReader),
			GraphChunkIDs: graphChunkIDs,
			BridgeChunks:  bridgeChunks,
			HyPESearch:    HyPESearchEnabled(ctx, h.siteConfigReader),
		}
		// AP-B3: tool-aware planner. Only meaningful when DAG is on
		// AND a dispatcher is wired AND the gate is set. Catalog is
		// rebuilt per request so admin tool-config changes take effect
		// on the next chat without restart. ToolRunner adapts the
		// existing dispatcher into the executor's interface.
		if planExecuteParams.DAG && ChatPlanExecuteToolAware(ctx, h.siteConfigReader) {
			if mcpDisp, ok := planExecuteTools.(*MCPDispatcher); ok && mcpDisp != nil {
				planExecuteParams.ToolAware = true
				planExecuteParams.ToolCatalog = mcpDisp.PlanToolCatalog(kbID)
				planExecuteParams.ToolRunner = mcpDisp
			} else {
				logctx.From(ctx).Warn("plan_execute: tool-aware gate is on but dispatcher is not MCPDispatcher; falling back to legacy planner")
			}
		}

		// AP-D3: inter-level critic. Wired only when DAG mode is
		// also on (the critic operates on DAG levels). The model
		// override falls back to the planning model, which itself
		// falls back to the KB default — three-layer resolution.
		if planExecuteParams.DAG && ChatPlanExecuteDAGIterative(ctx, h.siteConfigReader) {
			criticModel := ChatPlanExecuteDAGIterativeModel(ctx, h.siteConfigReader)
			if criticModel == "" {
				criticModel = planningModel
			}
			planExecuteParams.DAGCritic = newAIDAGCritic(h.aiResolver, kbID, lang, criticModel)
		}
		chatCtx, err = RunPlanExecuteChat(ctx, h.aiResolver, h.searchService, planExecuteParams, collectEmit)

	case OrchAgentic:
		agenticParams := AgenticChatParams{
			KbID:            kbID,
			Query:           searchQuery,
			Language:        lang,
			CurrentDateLine: dateLine,
			FileIDs:         body.SelectedFileIDs,
			KbSystemPrompt:  kbSystemPrompt,
			PlanningModel:   EnrichmentModel(ctx, h.siteConfigReader),
			MaxHops:         ChatAgenticMaxHops(ctx, h.siteConfigReader),
			Plateau:         plateau,
			GraphChunkIDs:   graphChunkIDs,
			BridgeChunks:    bridgeChunks,
			HyPESearch:      HyPESearchEnabled(ctx, h.siteConfigReader),
		}
		chatCtx, err = RunAgenticChat(ctx, h.aiResolver, h.searchService, agenticParams, collectEmit)

	case OrchStandard:
		chatCtx, err = RunDeepChat(ctx, h.aiResolver, h.searchService, deepParams, collectEmit)
	default:
		// Defensive: an unrecognized Orchestrator value must never leave
		// chatCtx nil — fall back to the same 2-step research path standard
		// turns use.
		chatCtx, err = RunDeepChat(ctx, h.aiResolver, h.searchService, deepParams, collectEmit)
	}
	if err != nil {
		// Agentic or deep chat failed — fall through to standard path.
		return false
	}

	// Deep chat succeeded — commit to SSE response.

	// Freshness dates for the cited files (one batch query, fail-soft).
	// Runs before the `sources` SSE frame below AND before the AddMessage
	// that persists the same slice, so the stream and messages.sources
	// carry identical dates.
	enrichSourceDates(ctx, h.fileDates, chatCtx.Sources)

	// Save user message.
	enhancedQuery := chatCtx.EnhancedQuery
	hasEnhanced := enhancedQuery != ""
	var enhancedQueryPtr *string
	if hasEnhanced {
		enhancedQueryPtr = &enhancedQuery
	}

	userMsg, err := h.resolveTurnUserMessage(ctx, AddMessageParams{
		ChatID:        chatID,
		Role:          "user",
		Content:       body.Message,
		IsEnhanced:    hasEnhanced,
		EnhancedQuery: enhancedQueryPtr,
	}, anchor)
	if err != nil {
		// Can't save user message — fall through to standard path which
		// will also fail, but at least it will write a proper error.
		return false
	}

	// Set SSE headers + opt out of the server-wide WriteTimeout.
	httputil.EnableSSE(w)

	sseFinished := false
	defer func() {
		if !sseFinished {
			writeSSEDone(ctx, w)
		}
	}()

	// Replay buffered progress events.
	for _, evt := range progressEvents {
		writeSSE(ctx, w, evt)
	}

	// Send initial metadata.
	writeOpeningFrames(ctx, w, chatCtx.Sources, enhancedQuery, chatID, userMsg.ID, chatCtx.Conflicts)

	// Stream AI completion. When chat_answer_tools_enabled is on AND a
	// tool dispatcher is wired, route through RunAnswerWithTools so the
	// model can call MCP tools mid-turn (calculator, sql_query, kb_search,
	// etc.). Otherwise the legacy single-shot streaming path runs
	// byte-identically to today.
	deepChatStart := time.Now()
	var responseBuf, reasoningBuf strings.Builder
	// Degenerate-run guard (W5-R4). The completion — and ONLY the
	// completion — runs under a cancellable child of ctx: when the answer
	// collapses into a runaway repetition, cancelling genCtx is what stops
	// the provider generating (and billing) the rest of it. ctx itself
	// stays live for the SSE writes, the AddMessage and the post-response
	// tasks that follow, so a guard cancel completes the turn normally
	// instead of surfacing as a stream error.
	genCtx, cancelGen := context.WithCancel(ctx)
	defer cancelGen()
	guard := newAnswerGuard(ChatAnswerDegenerateRunLimit(ctx, h.siteConfigReader), lang, "web", cancelGen)
	streamEmit := newGuardedEmit(guard, &responseBuf, &reasoningBuf,
		func(s string) { writeSSE(ctx, w, map[string]string{"content": s}) },
		func(s string) { writeSSE(ctx, w, map[string]string{"reasoning": s}) },
	)
	// Team synthesis carries user-authored, persona-influenced findings in its
	// system prompt (a prompt-injection amplifier if handed the full,
	// unrestricted answer-tool catalog) — answer tools stay off on any turn a
	// team actually authored, until per-team catalog restriction lands
	// (follow-up). That now includes a comparison turn whose summary a team
	// wrote (OrchComparison + comparisonTeamAnswered): its system prompt
	// carries the same kind of team-synthesised content via KbSystemPrompt,
	// so it needs the same exclusion as a pure OrchTeam turn — not just
	// "orch != OrchTeam", which teamAuthoredTurn is what makes this drop.
	useAnswerTools := !teamAuthoredTurn(orch, comparisonTeamAnswered) && ChatAnswerToolsEnabled(ctx, h.siteConfigReader) && h.toolDispatcher != nil
	// answerToolsDispatcher/catalog default to the unrestricted pair; a
	// per-route allowlist (W6-R8) narrows both together below so the catalog
	// projection and the dispatch boundary can never drift apart.
	var answerToolsDispatcher ToolDispatcher = h.toolDispatcher
	var catalog []ai.ChatTool
	if useAnswerTools {
		mcpDisp, _ := h.toolDispatcher.(*MCPDispatcher)
		if mcpDisp != nil {
			catalog = mcpDisp.AnswerToolCatalog(kbID)
		}
		byRoute := ChatAnswerToolsByRoute(ctx, h.siteConfigReader)
		if allow, ok, decision, reason := resolveAnswerToolsRoute(byRoute, queryType, orchIn.IsGlobalSynthesis); ok {
			answerToolsDispatcher, catalog = restrictToolsForRoute(h.toolDispatcher, catalog, allow, true)
			routeEvt := TrajectoryEvent{
				Stage:    "answer_tools_route",
				Decision: decision,
				Reason:   reason,
				Findings: len(catalog),
			}
			if routeEvt.Reason == "" && len(catalog) == 0 {
				// Findings is omitempty, so a bare {stage, decision} frame
				// cannot be told apart from "no findings key" — this is the
				// one case an operator debugging a route restriction most
				// wants to see (the loop is about to be skipped entirely).
				routeEvt.Reason = "catalog empty; tool loop skipped"
			}
			emitTrajectory(func(pl map[string]any) { writeSSE(ctx, w, pl) }, routeEvt, nil)
		}
	}
	// A route restriction can filter the catalog down to empty; running the
	// tool loop with zero tools would be pointless scaffolding, so that case
	// falls through to the plain streaming answer below instead.
	runAnswerTools := shouldRunAnswerToolsLoop(useAnswerTools, catalog)
	if runAnswerTools {
		answerTrace := func(stage, decision, reason string, details map[string]any) {
			payload := map[string]any{
				"stage":    stage,
				"decision": decision,
				"reason":   reason,
			}
			for k, v := range details {
				payload[k] = v
			}
			writeSSE(ctx, w, payload)
		}
		err = RunAnswerWithTools(genCtx, AnswerToolsParams{
			AIResolver:      h.aiResolver,
			KbID:            kbID,
			ChatID:          chatID,
			SystemPrompt:    chatCtx.SystemPrompt,
			UserPrompt:      body.Message,
			History:         answerHistory,
			Tools:           catalog,
			Dispatcher:      answerToolsDispatcher,
			MaxRounds:       ChatAnswerToolsMaxRounds(ctx, h.siteConfigReader),
			ReasoningEffort: reasoningLevel,
			Temperature:     ChatAnswerTemperature(ctx, h.siteConfigReader),
		}, streamEmit, answerTrace)
		// A guard trip cancels genCtx, which the tool loop reports as a
		// context error — that is a deliberate abort with a usable answer
		// behind it, not a stream failure. Only a real error bails.
		if err != nil && !guard.tripped() {
			writeSSE(ctx, w, map[string]string{"error": "failed to run AI stream"})
			writeSSEDone(ctx, w)
			sseFinished = true
			return true
		}
	} else {
		events, sErr := ai.StreamCompletionWithHistory(genCtx, h.aiResolver, answerHistory, body.Message, chatCtx.SystemPrompt, kbID, reasoningLevel, ChatAnswerTemperature(ctx, h.siteConfigReader))
		if sErr != nil {
			writeSSE(ctx, w, map[string]string{"error": "failed to start AI stream"})
			writeSSEDone(ctx, w)
			sseFinished = true
			return true
		}
		var streamErr error
		for event := range events {
			if event.Done {
				streamErr = event.Err
				break
			}
			streamEmit(ai.StreamEvent{Content: event.Content, Reasoning: event.Reasoning})
			if guard.tripped() {
				// genCtx is already cancelled; stop consuming instead of
				// waiting for the provider's terminal event (which would
				// arrive carrying context.Canceled).
				break
			}
		}
		if streamErr != nil && !guard.tripped() {
			// Mid-stream abort (connection reset, oversized SSE frame): the
			// buffered content is truncated. Surface the error and bail
			// instead of persisting it as a complete AI message.
			logctx.From(ctx).Error("chat.send: AI stream aborted mid-answer", "error", streamErr, "chat_id", chatID, "kb_id", kbID)
			writeSSE(ctx, w, map[string]string{"error": "AI stream interrupted"})
			writeSSEDone(ctx, w)
			sseFinished = true
			return true
		}
	}

	fullResponse := responseBuf.String()
	if guard.tripped() {
		// Strip the run from the answer that gets persisted, append the
		// notice, and stream that notice as the final content frame so the
		// user sees why the answer stops mid-sentence. The turn then
		// completes normally — message saved, post-response tasks run.
		guarded, appended := guard.finish(fullResponse)
		fullResponse = guarded
		writeSSE(ctx, w, map[string]string{"content": appended})
		guard.recordTrajectory(func(pl map[string]any) { writeSSE(ctx, w, pl) })
		logctx.From(ctx).Warn("chat.send: degenerate answer run truncated",
			"chat_id", chatID, "kb_id", kbID,
			"limit", guard.tracker.Limit(), "run_length", guard.tracker.RunLength())
	}

	toolCallsThisTurn := 0
	if rec := ToolCallRecorderFromContext(ctx); rec != nil {
		toolCallsThisTurn = len(rec.Snapshot())
	}
	logctx.From(ctx).Info("rag.completion",
		"stage", "llm_completion",
		"answer_len", len(fullResponse),
		"reasoning_len", reasoningBuf.Len(),
		// reasoning_level is "" when the request did not enable reasoning;
		// "low"/"medium"/"high" when it did. A non-empty value here with
		// reasoning_len=0 means the provider returned no reasoning_content
		// despite enable_thinking being sent; "" means the toggle was off.
		"reasoning_level", reasoningLevel,
		"source_count", len(chatCtx.Sources),
		"low_confidence", len(chatCtx.Sources) < 3,
		"stream", true,
		"deep_chat", true,
		// answer_tools_path means "the tool loop actually ran" (W6-R8
		// fix round 1), not merely "tools were configured" — a route
		// restriction (or fix-round-2's unknown-query-type case) can
		// leave useAnswerTools true while this is false.
		"answer_tools_path", runAnswerTools,
		"tool_calls", toolCallsThisTurn,
	)
	observability.RecordCompletion(true, time.Since(deepChatStart).Seconds())
	if len(chatCtx.Sources) < 3 {
		observability.RecordLowConfidence()
	}

	result := h.finishDeepChatAnswer(ctx, w, chatID, kbID, lang, body, userMsg.ID, chatCtx, fullResponse, &reasoningBuf, orch, comparisonTeamAnswered, teamSel, progressEvents, deepChatStart, policyRule)
	// finishDeepChatAnswer calls writeSSEDone on every one of its own return
	// paths (funlen extraction of the former tail of this function, which
	// did the same inline) — sseFinished is a local of THIS function, so it
	// has to be set here rather than inside the extracted method.
	sseFinished = true
	return result
}

// finishDeepChatAnswer persists the assembled deep-chat answer, streams the
// terminal SSE frames (aiMessageId, structuredTable, follow-ups,
// verification), records the trace id, and logs the agent-decision outcome
// row. Extracted from the tail of tryDeepChat (funlen) — pure extraction, no
// behaviour change: every log line, trajectory/SSE frame and early return is
// identical to the inline version. Always returns true (tryDeepChat's own
// return value on every path through this block); the caller still owns
// sseFinished, since that variable belongs to tryDeepChat's own deferred
// writeSSEDone guard.
func (h *Handler) finishDeepChatAnswer(
	ctx context.Context,
	w http.ResponseWriter,
	chatID, kbID, lang string,
	body sendMessageRequest,
	userMsgID string,
	chatCtx *ChatContext,
	fullResponse string,
	reasoningBuf *strings.Builder,
	orch Orchestrator,
	comparisonTeamAnswered bool,
	teamSel *teamSelection,
	progressEvents []map[string]any,
	deepChatStart time.Time,
	policyRule *int,
) bool {
	// Save AI message.
	var reasoningPtr *string
	if reasoningBuf.Len() > 0 {
		fullReasoning := reasoningBuf.String()
		reasoningPtr = &fullReasoning
	}
	// Derive team/agent attribution for AI message (reused for recordAgentDecision below).
	decTeamID, decAgentID := attributionIDs(teamAuthoredTurn(orch, comparisonTeamAnswered), teamSel)
	aiMsg, err := h.store.AddMessage(ctx, AddMessageParams{
		ChatID:          chatID,
		Role:            "ai",
		Content:         fullResponse,
		Sources:         chatCtx.Sources,
		Reasoning:       reasoningPtr,
		ParentMessageID: &userMsgID,
		StructuredTable: chatCtx.StructuredTable,
		Conflicts:       ConflictsForWire(chatCtx.Conflicts),
		TeamID:          decTeamID,
		AgentID:         decAgentID,
	})
	if err != nil {
		writeSSE(ctx, w, map[string]string{"error": "failed to save AI message"})
		writeSSEDone(ctx, w)
		return true
	}

	// Send AI message ID.
	writeSSE(ctx, w, map[string]string{"aiMessageId": aiMsg.ID})
	if chatCtx.StructuredTable != nil {
		writeSSE(ctx, w, map[string]any{"structuredTable": chatCtx.StructuredTable})
	}

	// Follow-ups + factcheck in parallel (no data dependency).
	// AP-A2: emit callback so refine_start/refine_complete trajectory
	// events stream live; the painted streaming UI mutates in place.
	emit := func(p map[string]any) { writeSSE(ctx, w, p) }
	followUps, verification, _ := h.runPostResponseTasks(ctx, body.Message, fullResponse, chatCtx.Context, kbID, lang, aiMsg.ID, chatCtx.Sources, emit, chatCtx.TabularTrace)
	if len(followUps) > 0 {
		writeSSE(ctx, w, map[string]any{"followUpQuestions": followUps})
	}
	if verification != nil {
		writeSSE(ctx, w, map[string]any{"verification": verification})
	}

	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
		if err := h.store.UpdateMessageTraceID(ctx, aiMsg.ID, sc.TraceID().String()); err != nil {
			logctx.From(ctx).Warn("failed to persist message trace_id", "messageId", aiMsg.ID, "error", err)
		}
	}

	// Phase 1 §1.4: log the orchestrator's outcome row for the admin
	// metrics panel. agentOutcomeFromEvents maps the trajectory's
	// terminal `answer` stage to the closed-enum outcome label.
	mode := "standard"
	switch orch {
	case OrchComparison:
		mode = "comparison"
	case OrchTeam:
		mode = "team"
	case OrchCorpusTable:
		mode = "corpus_table"
	case OrchDrift:
		mode = "drift"
	case OrchLongContext:
		mode = "longcontext"
	case OrchSupervisor:
		mode = "supervisor"
	case OrchPlanExecute:
		mode = "plan_execute"
	case OrchAgentic:
		mode = "agentic"
	}
	outcome, hops, rounds := agentOutcomeFromEvents(progressEvents)
	if outcome == "" {
		outcome = "answered"
	}
	if mode == "agentic" {
		rounds = 0
	} else {
		hops = 0
	}
	h.recordAgentDecision(ctx, kbID, mode, outcome, hops, rounds, time.Since(deepChatStart).Milliseconds(), decTeamID, decAgentID, policyRule)

	writeSSEDone(ctx, w)
	return true
}

// ---------------------------------------------------------------------------
// Explicit team/agent selection
// ---------------------------------------------------------------------------

// teamSelection is the resolved explicit team/agent pick for one turn.
type teamSelection struct {
	team  *agentteams.TeamForChat
	agent *agentteams.AgentRecord
}

// attributionIDs derives the team/agent ids to attribute an AI message (and
// its agent_decisions row) to. The first argument must be true only when the
// team actually WROTE something on this turn — either as the orchestrator
// (OrchTeam) or as the author of a comparison turn's summary (since 2026-08;
// before that, comparison turns always won over the team selection and the
// pick was resolved-but-unused). A resolved-but-unused pick — Enhance forces
// the standard path, or a comparison whose team summary failed — must not be
// written to messages.team_id / agent_decisions.team_id, per the
// DecisionRecorder contract ("nil otherwise"). Callers should pass
// teamAuthoredTurn(orch, comparisonTeamAnswered) rather than reimplementing
// the condition.
func attributionIDs(willRunTeam bool, teamSel *teamSelection) (teamID, agentID *string) {
	if !willRunTeam || teamSel == nil {
		return nil, nil
	}
	switch {
	case teamSel.team != nil:
		id := teamSel.team.Team.ID
		teamID = &id
	case teamSel.agent != nil:
		id := teamSel.agent.ID
		agentID = &id
	}
	return teamID, agentID
}

// teamAuthoredTurn reports whether a team/agent actually wrote something on
// this turn — as the orchestrator, or as the author of a comparison turn's
// summary. Extracted as a named function (rather than an inline `||` at the
// attributionIDs call site) so the condition itself is directly testable: a
// unit test of attributionIDs alone stays green even when the call site's
// condition regresses, because it never exercises the call site.
func teamAuthoredTurn(orch Orchestrator, comparisonTeamAnswered bool) bool {
	return orch == OrchTeam || comparisonTeamAnswered
}

// resolveTeamSelection loads and authorizes the request's team/agent pick.
// Fail-soft: any load failure (not attached, disabled, deleted, DB error)
// logs and returns (nil, reason) so the turn degrades to the standard path
// instead of erroring the chat. reason is non-empty only when the request
// explicitly named a team/agent (body.TeamID/AgentID set) and resolution
// failed — callers use it to surface the degradation instead of silently
// dropping the pick. The empty (no selection requested) and success cases
// both return a "" reason.
func (h *Handler) resolveTeamSelection(ctx context.Context, body sendMessageRequest, kbID string) (*teamSelection, string) {
	if h.teamLoader == nil {
		return nil, ""
	}
	switch {
	case body.TeamID != "":
		tfc, err := h.teamLoader.LoadTeamForChat(ctx, body.TeamID, kbID)
		if err != nil {
			logctx.From(ctx).Warn("chat.team_selection.load_failed",
				"team_id", body.TeamID, "kb_id", kbID, "error", err)
			return nil, "load_failed"
		}
		if len(tfc.Members) == 0 {
			logctx.From(ctx).Warn("chat.team_selection.empty_team", "team_id", body.TeamID)
			return nil, "empty_team"
		}
		return &teamSelection{team: tfc}, ""
	case body.AgentID != "":
		a, err := h.teamLoader.LoadAgentForChat(ctx, body.AgentID, kbID)
		if err != nil {
			logctx.From(ctx).Warn("chat.agent_selection.load_failed",
				"agent_id", body.AgentID, "kb_id", kbID, "error", err)
			return nil, "load_failed"
		}
		return &teamSelection{agent: a}, ""
	}
	return nil, ""
}

// persistChatSelection stores the RESOLVED team/agent pick on the chat row so
// reopening the chat restores it. Takes the resolved teamSelection (not the
// raw request body) so a pick that failed to resolve — deleted team,
// disabled agent, empty team — is persisted as cleared rather than as a
// stale id the picker would just have to drop on next load. Always runs (a
// cleared picker, or a resolution failure, must clear the row). Best-effort.
func (h *Handler) persistChatSelection(ctx context.Context, chatID string, teamSel *teamSelection) {
	var teamID, agentID *string
	if teamSel != nil {
		switch {
		case teamSel.team != nil:
			id := teamSel.team.Team.ID
			teamID = &id
		case teamSel.agent != nil:
			id := teamSel.agent.ID
			agentID = &id
		}
	}
	if err := h.store.UpdateChatAgentSelection(ctx, chatID, teamID, agentID); err != nil {
		logctx.From(ctx).Warn("chat.selection.persist_failed", "chat_id", chatID, "error", err)
	}
}
