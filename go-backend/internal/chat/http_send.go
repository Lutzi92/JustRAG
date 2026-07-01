package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
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
func writeSSE(w http.ResponseWriter, data any) {
	// json.Marshal reuses an internal encodeState pool, so it avoids the
	// per-frame json.Encoder struct allocation a fresh NewEncoder incurs on
	// long streams (chat responses emit hundreds of frames). It HTML-escapes
	// identically and returns no trailing newline, so nothing to trim.
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("chat: writeSSE marshal failed", "error", err, "type", fmt.Sprintf("%T", data))
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
		slog.Debug("chat: writeSSE write failed (likely client disconnect)", "error", werr)
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeSSEDone writes the SSE stream terminator and flushes. Write errors
// are observed at debug level — see writeSSE for the rationale.
func writeSSEDone(w http.ResponseWriter) {
	httputil.RearmSSEWriteDeadline(w)
	if _, werr := fmt.Fprint(w, "data: [DONE]\n\n"); werr != nil {
		slog.Debug("chat: writeSSEDone write failed (likely client disconnect)", "error", werr)
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

	ctx, kbID = h.maybeRouteKB(ctx, r, user.ID, kbID, body.Message)

	// Per-KB config: once kb_id is final, swap to a request-local handler whose
	// reader + SearchService overlay this KB's kb_site_configs overrides. No-op
	// (returns h) when the KB has no overrides.
	h = h.forKB(ctx, kbID)

	chatID, ok := h.resolveOrCreateChat(ctx, w, body.ChatID, kbID, user.ID, body.Message)
	if !ok {
		return
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

	// Recent turns, loaded once: the answer-history block (every answer
	// path) and the transform-follow-up route both need them. CondenseFollowUp
	// keeps its own load because its signature is shared with publicapi.
	convRows := h.loadConversationRows(ctx, chatID, parentMsgID)
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
			h.handleTransformFollowUp(ctx, w, span, body, chatID, kbID, lang, user.ID, parentMsgID, prev, convRows, streamMode)
			return
		}
	}

	searchQuery, _ := CondenseFollowUp(ctx, h.aiResolver, h.store, chatID, parentMsgID, body.Message, kbID, lang)

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

	// Follow-up over a prior upload: when an attachment rides along but this is
	// NOT a fresh comparison run (no modes), inject a capped block of the
	// uploaded document + prior findings into the KB system prompt so the user
	// can ask about the upload without re-uploading. Best-effort: load failures
	// and foreign-owned attachments are silently skipped. Threads through both
	// the deep-chat path and the standard PrepareChatContext path because both
	// receive kbSystemPrompt below.
	if body.AttachmentID != "" && !runCompare && h.attachmentStore != nil {
		if att, aerr := h.attachmentStore.Get(ctx, body.AttachmentID); aerr == nil && att.UserID == user.ID {
			kbSystemPrompt = prependBlock(buildFollowUpContext(att), kbSystemPrompt)
		}
	}

	// Deep chat: complex streaming queries get a 2-step research agent.
	// Comparison turns also route through tryDeepChat (its switch dispatches
	// the comparison orchestrator) regardless of complexity classification.
	isComplex := cls.UseHyDE && cls.UseMultiQuery
	if (isComplex || runCompare) && streamMode {
		if handled := h.tryDeepChat(ctx, w, r, chatID, kbID, lang, dateLine, searchQuery, cls.QueryType, kbSystemPrompt, reasoningLevel, body, parentMsgID, graphDec, graphChunkIDs, bridgeChunks, answerHistory); handled {
			return
		}
		// Deep chat failed — fall through to standard path.
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
	userMsg, err := h.store.AddMessage(ctx, AddMessageParams{
		ChatID:          chatID,
		Role:            "user",
		Content:         body.Message,
		IsEnhanced:      hasEnhanced,
		EnhancedQuery:   enhancedQueryPtr,
		ParentMessageID: parentMsgID,
	})
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
	}
	if streamMode {
		h.writeStreamingResponse(ctx, w, rp)
		return
	}
	h.writeJSONResponse(ctx, w, rp)
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
	chatID, kbID, lang, dateLine, searchQuery, queryType, kbSystemPrompt, reasoningLevel string,
	body sendMessageRequest,
	parentMsgID *string,
	graphDec GraphTraversalDecision,
	graphChunkIDs []string,
	bridgeChunks map[string]int,
	answerHistory []ai.ChatHistoryEntry,
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
	agenticEnabled := ChatAgenticEnabled(ctx, h.siteConfigReader)
	planExecuteEnabled := ChatPlanExecuteEnabled(ctx, h.siteConfigReader)

	supervisorEnabled := ChatSupervisorEnabled(ctx, h.siteConfigReader)
	driftEnabled := ChatDriftEnabled(ctx, h.siteConfigReader)

	// Corpus-table comparison: checked first so it can take top priority over
	// all orchestrators. The two-stage gate (keyword classifier + optional LLM
	// confirm) keeps false-positive rate low; corpusChunks being non-nil is the
	// infrastructure-availability guard.
	corpusTableEnabled := ChatCorpusTableEnabled(ctx, h.siteConfigReader)
	willRunCorpusTable := corpusTableEnabled &&
		h.corpusChunks != nil &&
		body.Enhance == "" &&
		IsCorpusComparisonQuery(searchQuery, lang) &&
		(!ChatCorpusTableRouterLLMEnabled(ctx, h.siteConfigReader) ||
			ai.ConfirmCorpusComparison(ctx, h.aiResolver, searchQuery, kbID, lang, ChatCorpusTableModel(ctx, h.siteConfigReader)))

	// In-chat document comparison takes top priority over every orchestrator
	// (including corpus-table) — an explicit attachment + modes is an
	// unambiguous user intent, so it should never be hijacked by a query
	// classifier. attachmentStore being non-nil is the infrastructure guard.
	runCompare := h.attachmentStore != nil &&
		willRunComparison(CompareEnabled(ctx, h.siteConfigReader), body.AttachmentID, body.ComparisonModes)
	if runCompare {
		willRunCorpusTable = false
	}

	// Full iterative DRIFT takes priority over the other orchestrators, but
	// only for global-synthesis queries (its primer is wasted on narrow
	// lookups). Narrow gate → it rarely intercepts; everything else falls
	// through to supervisor/plan-execute/agentic/standard unchanged.
	willRunDrift := !runCompare && !willRunCorpusTable && driftEnabled &&
		queryType == vector.QueryTypeComplexReasoning &&
		body.Enhance == "" &&
		IsGlobalSynthesisQuery(searchQuery)

	// Phase 3 §3.2 supervisor takes priority over both plan-execute and
	// agentic when the gate is on. Plan §3.2 ship gate: "non-regression
	// on lookup/enumeration; ≥ 2 pp gain on complex_reasoning MRR or
	// nDCG" — only the eval harness can decide whether the gate flips,
	// so this gate stays per-deployment opt-in.
	willRunSupervisor := !runCompare && !willRunCorpusTable && !willRunDrift && supervisorEnabled &&
		queryType == vector.QueryTypeComplexReasoning &&
		body.Enhance == ""
	willRunPlanExecute := !runCompare && !willRunCorpusTable && !willRunDrift && !willRunSupervisor && planExecuteEnabled &&
		queryType == vector.QueryTypeComplexReasoning &&
		body.Enhance == ""
	willRunAgentic := !runCompare && !willRunCorpusTable && !willRunDrift && !willRunSupervisor && !willRunPlanExecute &&
		agenticEnabled &&
		queryType == vector.QueryTypeComplexReasoning &&
		body.Enhance == ""

	logctx.From(ctx).Info("rag.deep_chat.dispatch",
		"supervisor_enabled", supervisorEnabled,
		"plan_execute_enabled", planExecuteEnabled,
		"agentic_enabled", agenticEnabled,
		"drift_enabled", driftEnabled,
		"corpus_table_enabled", corpusTableEnabled,
		"query_type", queryType,
		"enhance", body.Enhance,
		"will_run_comparison", runCompare,
		"will_run_corpus_table", willRunCorpusTable,
		"will_run_drift", willRunDrift,
		"will_run_supervisor", willRunSupervisor,
		"will_run_plan_execute", willRunPlanExecute,
		"will_run_agentic", willRunAgentic,
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

	switch {
	case runCompare:
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
			chatCtx.SystemPrompt = prompts.ComparisonSummaryPrompt(lang) +
				"\n\n" + renderFindingsForSummary(cmpFindings)
		}

	case willRunCorpusTable:
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

	case willRunDrift:
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

	case willRunSupervisor:
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

			SufficientContextEnabled: ChatSufficientContextEnabled(ctx, h.siteConfigReader),
			SufficientContextModel:   ResolveFastTierModel(ctx, h.siteConfigReader, "chat_sufficient_context_model"),
		}
		chatCtx, err = RunSupervisorChat(ctx, h.aiResolver, h.searchService, supervisorParams, collectEmit)

	case willRunPlanExecute:
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
			DAG:             ChatPlanExecuteDAG(ctx, h.siteConfigReader),
			MaxDAGDepth:     ChatPlanExecuteMaxDAGDepth(ctx, h.siteConfigReader),
			MaxDAGNodes:     ChatPlanExecuteMaxDAGNodes(ctx, h.siteConfigReader),
			GraphChunkIDs:   graphChunkIDs,
			BridgeChunks:    bridgeChunks,
			HyPESearch:      HyPESearchEnabled(ctx, h.siteConfigReader),
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

	case willRunAgentic:
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

	default:
		chatCtx, err = RunDeepChat(ctx, h.aiResolver, h.searchService, deepParams, collectEmit)
	}
	if err != nil {
		// Agentic or deep chat failed — fall through to standard path.
		return false
	}

	// Deep chat succeeded — commit to SSE response.

	// Save user message.
	enhancedQuery := chatCtx.EnhancedQuery
	hasEnhanced := enhancedQuery != ""
	var enhancedQueryPtr *string
	if hasEnhanced {
		enhancedQueryPtr = &enhancedQuery
	}

	userMsg, err := h.store.AddMessage(ctx, AddMessageParams{
		ChatID:          chatID,
		Role:            "user",
		Content:         body.Message,
		IsEnhanced:      hasEnhanced,
		EnhancedQuery:   enhancedQueryPtr,
		ParentMessageID: parentMsgID,
	})
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
			writeSSEDone(w)
		}
	}()

	// Replay buffered progress events.
	for _, evt := range progressEvents {
		writeSSE(w, evt)
	}

	// Send initial metadata.
	writeSSE(w, map[string]any{
		"sources":       chatCtx.Sources,
		"enhancedQuery": enhancedQuery,
		"chatId":        chatID,
		"userMessageId": userMsg.ID,
	})

	// Stream AI completion. When chat_answer_tools_enabled is on AND a
	// tool dispatcher is wired, route through RunAnswerWithTools so the
	// model can call MCP tools mid-turn (calculator, sql_query, kb_search,
	// etc.). Otherwise the legacy single-shot streaming path runs
	// byte-identically to today.
	deepChatStart := time.Now()
	var responseBuf, reasoningBuf strings.Builder
	streamEmit := func(e ai.StreamEvent) {
		if e.Content != "" {
			responseBuf.WriteString(e.Content)
			writeSSE(w, map[string]string{"content": e.Content})
		}
		if e.Reasoning != "" {
			reasoningBuf.WriteString(e.Reasoning)
			writeSSE(w, map[string]string{"reasoning": e.Reasoning})
		}
	}
	useAnswerTools := ChatAnswerToolsEnabled(ctx, h.siteConfigReader) && h.toolDispatcher != nil
	if useAnswerTools {
		answerTrace := func(stage, decision, reason string, details map[string]any) {
			payload := map[string]any{
				"stage":    stage,
				"decision": decision,
				"reason":   reason,
			}
			for k, v := range details {
				payload[k] = v
			}
			writeSSE(w, payload)
		}
		mcpDisp, _ := h.toolDispatcher.(*MCPDispatcher)
		var catalog []ai.ChatTool
		if mcpDisp != nil {
			catalog = mcpDisp.AnswerToolCatalog(kbID)
		}
		err = RunAnswerWithTools(ctx, AnswerToolsParams{
			AIResolver:      h.aiResolver,
			KbID:            kbID,
			ChatID:          chatID,
			SystemPrompt:    chatCtx.SystemPrompt,
			UserPrompt:      body.Message,
			History:         answerHistory,
			Tools:           catalog,
			Dispatcher:      h.toolDispatcher,
			MaxRounds:       ChatAnswerToolsMaxRounds(ctx, h.siteConfigReader),
			ReasoningEffort: reasoningLevel,
			Temperature:     ChatAnswerTemperature(ctx, h.siteConfigReader),
		}, streamEmit, answerTrace)
		if err != nil {
			writeSSE(w, map[string]string{"error": "failed to run AI stream"})
			writeSSEDone(w)
			sseFinished = true
			return true
		}
	} else {
		events, sErr := ai.StreamCompletionWithHistory(ctx, h.aiResolver, answerHistory, body.Message, chatCtx.SystemPrompt, kbID, reasoningLevel, ChatAnswerTemperature(ctx, h.siteConfigReader))
		if sErr != nil {
			writeSSE(w, map[string]string{"error": "failed to start AI stream"})
			writeSSEDone(w)
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
		}
		if streamErr != nil {
			// Mid-stream abort (connection reset, oversized SSE frame): the
			// buffered content is truncated. Surface the error and bail
			// instead of persisting it as a complete AI message.
			logctx.From(ctx).Error("chat.send: AI stream aborted mid-answer", "error", streamErr, "chat_id", chatID, "kb_id", kbID)
			writeSSE(w, map[string]string{"error": "AI stream interrupted"})
			writeSSEDone(w)
			sseFinished = true
			return true
		}
	}

	fullResponse := responseBuf.String()

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
		"answer_tools_path", useAnswerTools,
		"tool_calls", toolCallsThisTurn,
	)
	observability.RecordCompletion(true, time.Since(deepChatStart).Seconds())
	if len(chatCtx.Sources) < 3 {
		observability.RecordLowConfidence()
	}

	// Save AI message.
	var reasoningPtr *string
	if reasoningBuf.Len() > 0 {
		fullReasoning := reasoningBuf.String()
		reasoningPtr = &fullReasoning
	}
	aiMsg, err := h.store.AddMessage(ctx, AddMessageParams{
		ChatID:          chatID,
		Role:            "ai",
		Content:         fullResponse,
		Sources:         chatCtx.Sources,
		Reasoning:       reasoningPtr,
		ParentMessageID: &userMsg.ID,
		StructuredTable: chatCtx.StructuredTable,
	})
	if err != nil {
		writeSSE(w, map[string]string{"error": "failed to save AI message"})
		writeSSEDone(w)
		sseFinished = true
		return true
	}

	// Send AI message ID.
	writeSSE(w, map[string]string{"aiMessageId": aiMsg.ID})
	if chatCtx.StructuredTable != nil {
		writeSSE(w, map[string]any{"structuredTable": chatCtx.StructuredTable})
	}

	// Follow-ups + factcheck in parallel (no data dependency).
	// AP-A2: emit callback so refine_start/refine_complete trajectory
	// events stream live; the painted streaming UI mutates in place.
	emit := func(p map[string]any) { writeSSE(w, p) }
	followUps, verification, _ := h.runPostResponseTasks(ctx, body.Message, fullResponse, chatCtx.Context, kbID, lang, aiMsg.ID, chatCtx.Sources, emit)
	if len(followUps) > 0 {
		writeSSE(w, map[string]any{"followUpQuestions": followUps})
	}
	if verification != nil {
		writeSSE(w, map[string]any{"verification": verification})
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
	switch {
	case runCompare:
		mode = "comparison"
	case willRunCorpusTable:
		mode = "corpus_table"
	case willRunDrift:
		mode = "drift"
	case willRunSupervisor:
		mode = "supervisor"
	case willRunPlanExecute:
		mode = "plan_execute"
	case willRunAgentic:
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
	h.recordAgentDecision(ctx, kbID, mode, outcome, hops, rounds, time.Since(deepChatStart).Milliseconds())

	writeSSEDone(w)
	sseFinished = true
	return true
}
