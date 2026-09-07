package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/aibudget"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/longmem"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/sessionmem"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// SendMessage helper functions
//
// These are the sub-functions SendMessage delegates to. Each is a discrete
// pipeline stage (validate, resolve chat, classify, etc.) that was extracted
// from the original 600+ line handler so the orchestration in SendMessage
// stays readable and each stage stays independently testable.
// ---------------------------------------------------------------------------

// attachTurnBudget binds the AP-A3 per-turn budgets (time, tokens, tool
// calls) onto ctx. All three defaults are 0 = unlimited so a deployment
// that hasn't set any chat_turn_budget_* keys sees no behaviour change.
// Token budget reuses internal/aibudget; time + tool-call dimensions
// live on chat.TurnBudget.
func (h *Handler) attachTurnBudget(ctx context.Context) context.Context {
	budgetSeconds := ChatTurnBudgetSeconds(ctx, h.siteConfigReader)
	budgetTokens := ChatTurnBudgetTokens(ctx, h.siteConfigReader)
	budgetToolCalls := ChatTurnBudgetToolCalls(ctx, h.siteConfigReader)
	if budgetTokens > 0 {
		ctx = aibudget.New(ctx, budgetTokens)
	}
	if budgetSeconds > 0 || budgetToolCalls > 0 {
		ctx = WithTurnBudget(ctx, NewTurnBudget(
			time.Duration(budgetSeconds)*time.Second,
			budgetToolCalls,
		))
	}
	return ctx
}

// parseAndValidateMessage decodes the JSON body and applies length +
// prompt-security validation. On failure, writes the HTTP error and
// returns ok=false so the caller can return immediately.
//
// Length and prompt-security checks run BEFORE downstream LLM calls
// (KB router, classifiers) so an oversized or disallowed message
// doesn't pay for expensive work. The explicit length check produces a
// descriptive error ("exceeds maximum length"); ValidatePromptInput's
// own length cap is the secondary gate that produces a generic
// "disallowed content" response when callers bypass the fast path.
func parseAndValidateMessage(w http.ResponseWriter, r *http.Request, userID string) (sendMessageRequest, bool) {
	var body sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return body, false
	}
	if body.Message == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "message is required")
		return body, false
	}
	if len(body.Message) > MaxMessageLength {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "message exceeds maximum length of 32,000 characters")
		return body, false
	}
	validation := ValidatePromptInput(body.Message, "message")
	if !validation.IsValid {
		LogSecurityWarning(userID, body.Message, validation.Warnings)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "message contains disallowed content")
		return body, false
	}
	// Regenerate is shape-checked here rather than where it is resolved,
	// because resolving it needs a chat — and by then the caller has already
	// created one and recorded a usage event. A regenerate that cannot
	// possibly be answered must not leave either behind. It names an existing
	// answer, so an absent chatId or a client placeholder id ("temp-ai-…") is
	// malformed, not a normal turn.
	if body.RegenerateOfMessageID != "" {
		if body.ChatID == "" {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "regenerateOfMessageId requires chatId")
			return body, false
		}
		if SanitizeParentMessageID(body.RegenerateOfMessageID) == nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "regenerateOfMessageId must be a message id")
			return body, false
		}
	}
	return body, true
}

// maybeRouteKB applies the AP-A4 sub-KB router when the URL carries
// `?route=auto`. ResolveKBViaRouter is fail-open at every step — on
// disabled gate, missing lister, LLM error, or below-threshold
// confidence, it returns the original kbID unchanged so the rest of
// the pipeline behaves identically. The override happens before the
// chat-level permission check so the resolved KB is the one whose
// access is validated downstream.
func (h *Handler) maybeRouteKB(ctx context.Context, r *http.Request, userID, kbID, message string) (context.Context, string) {
	if r.URL.Query().Get("route") != "auto" {
		return ctx, kbID
	}
	resolvedKBID, _, rerr := ResolveKBViaRouter(
		ctx, h.aiResolver, h.siteConfigReader,
		h.kbRouterCandidates,
		userID, kbID, message,
		true,
	)
	if rerr != nil {
		logctx.From(ctx).Warn("kb_router: resolver error; keeping URL kb_id", "error", rerr)
	}
	if resolvedKBID == "" || resolvedKBID == kbID {
		return ctx, kbID
	}
	logctx.From(ctx).Info("rag.kb_router.override",
		"requested_kb_id", kbID, "resolved_kb_id", resolvedKBID)
	return logctx.WithKB(ctx, resolvedKBID), resolvedKBID
}

// resolveOrCreateChat returns the chat ID to use for this turn. When
// chatID is non-empty, verifies the chat belongs to userID + kbID and
// returns a 403 otherwise. When chatID is empty, creates a new chat
// titled from the first 50 characters of message. On error, writes the
// HTTP response and returns ok=false.
func (h *Handler) resolveOrCreateChat(ctx context.Context, w http.ResponseWriter, chatID, kbID, userID, message string) (string, bool) {
	if chatID != "" {
		chat, err := h.store.GetChatByID(ctx, chatID)
		if err != nil {
			logctx.From(ctx).Error("chat.send: get chat", "error", err, "chat_id", chatID, "user_id", userID, "kb_id", kbID)
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch chat")
			return "", false
		}
		if chat == nil || chat.UserID != userID || chat.KbID != kbID {
			httputil.WriteErrorCtx(ctx, w, http.StatusForbidden, "access denied")
			return "", false
		}
		return chatID, true
	}
	title := message
	runes := []rune(title)
	if len(runes) > 50 {
		title = string(runes[:50])
	}
	newChat, err := h.store.CreateChat(ctx, kbID, userID, title)
	if err != nil {
		logctx.From(ctx).Error("chat.send: create chat", "error", err, "user_id", userID, "kb_id", kbID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to create chat")
		return "", false
	}
	return newChat.ID, true
}

// queryClassification captures the routing decisions derived from the
// raw user query: whether to enable HyDE / MultiQuery for the deep
// chat path, and the QueryType bucket that downstream adaptive logic
// keys on.
type queryClassification struct {
	UseHyDE       bool
	UseMultiQuery bool
	QueryType     string
}

// classifyQuery applies the heuristic + LLM complexity check and the
// enumeration regex, records per-query telemetry and emits the
// `rag.query_classification` log line.
//
// Cheap syntactic heuristic short-circuits short factual lookups (the
// dominant case), saving a classifier round-trip. For everything else
// we defer to the LLM — the heuristic's "Complex" verdict is treated
// as "unknown" so we don't push more queries into deep chat than the
// LLM classifier would have.
//
// QueryType bucketing (priority order):
//   - complex_reasoning: LLM classifier flagged it complex (trusts
//     semantic understanding over the syntactic enumeration regex when
//     both apply)
//   - enumeration: list/aggregate-shape queries that the LLM did not
//     deem complex
//   - lookup: everything else (the dominant case)
func (h *Handler) classifyQuery(ctx context.Context, searchQuery, enhance, kbID, lang string) queryClassification {
	useHyDE := false
	useMultiQuery := false
	heuristicVerdictName := "skipped" // enhance != "" path
	llmComplexityVerdict := "skipped" // heuristic short-circuited or path bypassed
	if enhance == "" {
		hv := ai.HeuristicComplexity(searchQuery)
		switch hv {
		case ai.ComplexitySimple:
			heuristicVerdictName = "simple"
		case ai.ComplexityComplex:
			heuristicVerdictName = "complex"
		default:
			heuristicVerdictName = "unknown"
		}
		if hv != ai.ComplexitySimple {
			isComplex, classifyErr := ai.ClassifyQueryComplexity(ctx, h.aiResolver, searchQuery, kbID, lang)
			switch {
			case classifyErr != nil:
				llmComplexityVerdict = "error"
			case isComplex:
				llmComplexityVerdict = "complex"
				useHyDE = true
				useMultiQuery = true
			default:
				llmComplexityVerdict = "simple"
			}
		}
	}

	isEnum := IsEnumerationQuery(searchQuery, lang)
	queryType := vector.QueryTypeLookup
	switch {
	case useHyDE && useMultiQuery:
		queryType = vector.QueryTypeComplexReasoning
	case isEnum:
		queryType = vector.QueryTypeEnumeration
	}
	observability.RecordQueryType(queryType)

	logctx.From(ctx).Info("rag.query_classification",
		"heuristic", heuristicVerdictName,
		"llm_complexity", llmComplexityVerdict,
		"is_enumeration", isEnum,
		"query_type", queryType,
	)

	return queryClassification{UseHyDE: useHyDE, UseMultiQuery: useMultiQuery, QueryType: queryType}
}

// assembleSystemPrompt loads the KB's system prompt and prepends the
// session-memory and long-term-memory blocks when their respective
// gates are on. Returns the (possibly updated) context — the
// per-turn memory_write WriteCounter is attached when session memory
// is enabled so that goroutines spawned downstream (e.g. MCP tool
// dispatch) inherit the rate-limit counter.
//
// Order is intentional: durable longmem facts first, then per-chat
// session memory, then the KB prompt body, so the LLM reads identity
// facts before transient state.
func (h *Handler) assembleSystemPrompt(ctx context.Context, chatID, kbID, userID, lang, searchQuery string) (context.Context, string) {
	var kbSystemPrompt string
	if sp, err := h.store.GetKBSystemPrompt(ctx, kbID); err == nil && sp != nil {
		kbSystemPrompt = *sp
	}

	if h.sessionMemory != nil && ChatSessionMemoryEnabled(ctx, h.siteConfigReader) {
		ctx = sessionmem.WithWriteCounter(ctx, sessionmem.NewWriteCounter())
		if chatID != "" {
			if mem, merr := h.sessionMemory.Get(ctx, chatID); merr != nil {
				logctx.From(ctx).Warn("session memory: read failed; proceeding without memory",
					"chat_id", chatID, "error", merr)
			} else if mp := sessionmem.FormatPrompt(mem, lang, 10); mp != "" {
				kbSystemPrompt = prependBlock(mp, kbSystemPrompt)
			}
		}
	}

	if h.longmemStore != nil && ChatLongmemEnabled(ctx, h.siteConfigReader) {
		topK := ChatLongmemRecallTopK(ctx, h.siteConfigReader)
		decayDays := ChatLongmemDecayDays(ctx, h.siteConfigReader)
		recalled := h.recallUserMemory(ctx, userID, kbID, searchQuery, topK, decayDays)
		if mp := longmem.FormatMemoriesForPrompt(recalled); mp != "" {
			kbSystemPrompt = prependBlock(mp, kbSystemPrompt)
		}
	}

	// Appended AFTER the KB body (unlike the prepended memory blocks):
	// tabular guidance is a lower-priority capability note, so the
	// operator's KB identity prompt stays at the top.
	if g := maybeTabularGuidance(ctx, h.siteConfigReader, h.tabularCatalog, kbID, lang); g != "" {
		if kbSystemPrompt == "" {
			kbSystemPrompt = g
		} else {
			kbSystemPrompt = kbSystemPrompt + "\n\n" + g
		}
	}

	return ctx, kbSystemPrompt
}

// recallUserMemory picks between ANN-based RecallSemantic (T1-2) and
// the legacy recency-only Recall based on the
// chat_longmem_recall_semantic flag. The semantic path needs both
// the flag AND a non-empty searchQuery AND a working embedder; any
// of those failing falls back to Recall so the chat path always
// returns the user something. Failures emit one warning each.
//
// Note on QueryInstruction: ai.EncodeQuery applies the configured
// query-side asymmetric prefix (the Qwen3-Embedding instruction).
// The insert path uses ai.GenerateEmbedding (no prefix) so a fact
// stored as document-side embeds the same way chunks do. This keeps
// the asymmetric-encoding contract intact between chunks and
// memory.
func (h *Handler) recallUserMemory(ctx context.Context, userID, kbID, searchQuery string, topK, decayDays int) []longmem.Memory {
	useSemantic := ChatLongmemRecallSemantic(ctx, h.siteConfigReader) && strings.TrimSpace(searchQuery) != ""
	if useSemantic {
		emb, eerr := ai.EncodeQuery(ctx, h.aiResolver, searchQuery, "", kbID, nil)
		if eerr == nil && len(emb) > 0 {
			recalled, lerr := h.longmemStore.RecallSemantic(ctx, userID, kbID, emb, topK, decayDays)
			if lerr == nil {
				return recalled
			}
			// Dim mismatch or pool error — fall back to v1 and surface
			// the cause once. The error chain typically reads
			// "different vector dimensions" when the column hasn't
			// been migrated for the deployment's embedder.
			logctx.From(ctx).Warn("longmem: semantic recall failed; falling back to recency-only",
				"user_id", userID, "kb_id", kbID, "error", lerr)
		} else if eerr != nil {
			logctx.From(ctx).Warn("longmem: query embed failed; falling back to recency-only",
				"user_id", userID, "kb_id", kbID, "error", eerr)
		}
	}
	recalled, lerr := h.longmemStore.Recall(ctx, userID, kbID, topK, decayDays)
	if lerr != nil {
		logctx.From(ctx).Warn("longmem: recall failed; proceeding without long-term memory",
			"user_id", userID, "kb_id", kbID, "error", lerr)
		return nil
	}
	return recalled
}

// prependBlock returns block + "\n" + existing, or just block when
// existing is empty — the helper exists so the memory-injection paths
// don't repeat the same nil-handling logic.
func prependBlock(block, existing string) string {
	if existing == "" {
		return block
	}
	return block + "\n" + existing
}

// maybeTabularGuidance returns the Task-9 per-turn tabular-guidance snippet
// for the answer prompt: when the tabular master flag
// (chat_tabular_query_enabled) is on and the KB has materialized tables, the
// full per-KB catalog summary + rules (via prompts.TabularGuidance),
// optionally folding in chart-rendering rules when charts are also on; when
// only chart_tabular_charts_enabled is on, the pre-Task-9 chart-only
// snippet. Returns "" when neither flag is on, when cat is nil, or when the
// KB has no materialized tabular data. Fails closed on any catalog error (no
// guidance) so a transient DB issue never blocks the answer.
//
// R66: the token budget (ChatTabularGuidanceMaxTokens) is read and applied
// HERE, on every call, never inside cachedTabularCatalog — that decorator
// only memoizes ListByKB's raw []tabular.CatalogEntry for tabularCatalogTTL,
// not the compacted summary text. So a per-KB config override (or an
// operator changing the global default) takes effect on the very next turn,
// even for a KB whose catalog entries are still served from the 60s cache;
// there is no cache key to fold the budget into.

func maybeTabularGuidance(ctx context.Context, reader SiteConfigReader, cat TabularCatalogSummariser, kbID, lang string) string {
	master := ChatTabularQueryEnabled(ctx, reader)
	charts := ChatTabularChartsEnabled(ctx, reader)
	if !master && !charts {
		return ""
	}
	if cat == nil {
		return ""
	}
	has, err := cat.HasDataForKB(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Warn("chat: tabular-catalog check failed; skipping tabular guidance",
			"kb_id", kbID, "error", err)
		return ""
	}
	if !has {
		return ""
	}

	if !master {
		// Master flag off, charts on (the only remaining case given the
		// "neither" check above): keep the pre-Task-9 chart-only snippet.
		return prompts.TabularGuidance(lang, "", true)
	}

	entries, err := cat.ListByKB(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Warn("chat: tabular-catalog listing failed; skipping tabular guidance",
			"kb_id", kbID, "error", err)
		return ""
	}
	summary := tabular.CompactSchema(entries, nil, "", ChatTabularGuidanceMaxTokens(ctx, reader)).Text
	if summary == "" {
		// R41: HasDataForKB reported materialized tables, but the
		// compacted rendering came back empty. An empty catalogSummary
		// tells prompts.TabularGuidance to emit chart guidance only (or
		// nothing) — fall back to a placeholder line so the "## Tables"
		// rules block still renders.
		if lang == "de" {
			summary = "(keine Tabellen katalogisiert)"
		} else {
			summary = "(no tables catalogued)"
		}
	}
	return prompts.TabularGuidance(lang, summary, charts)
}

// tabularCatalogTTL bounds how long cachedTabularCatalog reuses a KB's
// ListByKB result before re-reading the catalog. maybeTabularGuidance runs
// on every complex_reasoning chat turn of every KB, but the catalog only
// changes when a spreadsheet file is ingested or deleted — a minute of
// staleness costs at most a one-turn-late pickup of a fresh upload.
const tabularCatalogTTL = 60 * time.Second

type tabularCatalogCacheEntry struct {
	entries []tabular.CatalogEntry
	at      time.Time
}

// cachedTabularCatalog wraps a TabularCatalogSummariser so ListByKB results
// are memoized per KB for tabularCatalogTTL. HasDataForKB passes straight
// through the embedded interface — it's already a cheap indexed EXISTS
// query, so caching it separately isn't worth the staleness. now is
// injectable for tests; WithTabularCatalog wires time.Now in production.
type cachedTabularCatalog struct {
	TabularCatalogSummariser
	now func() time.Time
	m   sync.Map // kbID -> tabularCatalogCacheEntry
}

func newCachedTabularCatalog(c TabularCatalogSummariser, now func() time.Time) *cachedTabularCatalog {
	return &cachedTabularCatalog{TabularCatalogSummariser: c, now: now}
}

func (c *cachedTabularCatalog) ListByKB(ctx context.Context, kbID string) ([]tabular.CatalogEntry, error) {
	now := c.now()
	if v, ok := c.m.Load(kbID); ok {
		if e, ok := v.(tabularCatalogCacheEntry); ok && now.Sub(e.at) < tabularCatalogTTL {
			return e.entries, nil
		}
	}
	entries, err := c.TabularCatalogSummariser.ListByKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	c.m.Store(kbID, tabularCatalogCacheEntry{entries: entries, at: now})
	return entries, nil
}

// resolveReasoningLevel returns the effective reasoning level for this
// turn: "" when reasoning is disabled, or the explicit ReasoningLevel
// (defaulting to "medium" when reasoning is enabled but unspecified).
func resolveReasoningLevel(body sendMessageRequest) string {
	if !body.ReasoningEnabled {
		return ""
	}
	if body.ReasoningLevel == "" {
		return "medium"
	}
	return body.ReasoningLevel
}

// resolveGraphRouting computes the AP-C4 graph-traversal decision and
// the matched subgraph chunk IDs. Telemetry: emits
// `rag_graph_routing_chunks_injected_total` with the appropriate
// outcome bucket when the gate fired but injection produced nothing
// (no chunks resolved, or chunk-injection sub-flag disabled). The
// "happy path" — gate fired AND chunks resolved AND injection on —
// records inside the search pipeline via SearchOptions.GraphChunkIDs.
func (h *Handler) resolveGraphRouting(ctx context.Context, kbID, searchQuery, queryType string) (GraphTraversalDecision, []string, map[string]int) {
	graphDec := NeedsGraphTraversal(ctx, h.kgStore, h.siteConfigReader, kbID, searchQuery, queryType)
	// HippoRAG-2 recognition memory: refine PPR seeds before chunk resolution.
	// Scoped to ppr mode so neighbours/paths modes are unaffected. Best-effort.
	if graphDec.Fired &&
		ChatGraphRoutingPathMode(ctx, h.siteConfigReader) == "ppr" &&
		ChatGraphRoutingPPRTripleFilterEnabled(ctx, h.siteConfigReader) {
		fn := func(c context.Context, prompt, system, kb, model string, spec *ai.StructuredSpec) (string, error) {
			res, gerr := ai.GenerateCompletionStructured(c, h.aiResolver, prompt, system, kb, model, spec)
			if gerr != nil {
				return "", gerr
			}
			return res.Content, nil
		}
		graphDec.MatchedEntities = refineSeedsWithTripleFilter(
			ctx, fn, h.kgStore, kbID, searchQuery,
			ChatGraphRoutingPPRTripleFilterModel(ctx, h.siteConfigReader),
			ChatGraphRoutingPPRTripleFilterMaxTriples(ctx, h.siteConfigReader),
			graphDec.MatchedEntities,
		)
	}
	graphChunkIDs := ResolveGraphChunksIfEnabled(ctx, h.kgStore, h.siteConfigReader, kbID, graphDec)

	// Bridge-evidence reranking: tally chunks lying on KG paths between the
	// (post-triple-filter) matched entities, so the search pipeline can boost
	// multi-hop bridge chunks. Inert when the flag is off (nil tally → no boost).
	//
	// Known minor redundancy: when path_mode=="paths" AND inject_chunks is on,
	// ResolveGraphChunksIfEnabled above already ran bridgeChunkTally internally
	// (resolveGraphChunksPaths). Re-running it here with the same entities
	// double-queries PathChunks in that specific both-on operator config. Left
	// as-is (bounded: ≤ N² pairs, disconnected pairs short-circuit; both flags
	// default off) rather than thread the tally back through the mode-dispatch
	// switch (8+ call sites). Revisit if both features ship enabled by default.
	var bridgeChunks map[string]int
	if graphDec.Fired && ChatGraphRoutingBridgeRerankEnabled(ctx, h.siteConfigReader) {
		bridgeChunks = bridgeChunkTally(ctx, h.kgStore, h.siteConfigReader, kbID,
			graphDec.MatchedEntities, ChatGraphRoutingMaxChunks(ctx, h.siteConfigReader))
	}

	if graphDec.Fired && len(graphChunkIDs) == 0 && ChatGraphRoutingInjectChunks(ctx, h.siteConfigReader) {
		observability.RecordGraphRoutingChunksInjected("no_chunks", 0)
	} else if graphDec.Fired && !ChatGraphRoutingInjectChunks(ctx, h.siteConfigReader) {
		observability.RecordGraphRoutingChunksInjected("skipped_disabled", 0)
	}
	return graphDec, graphChunkIDs, bridgeChunks
}

// chatResponseParams carries the state shared between the streaming
// and non-streaming response writers. Extracted as a struct because
// the alternative — a 12-argument function — obscures the per-call
// pipeline data the writers need.
type chatResponseParams struct {
	span               trace.Span
	chatID             string
	kbID               string
	lang               string
	userMessage        string
	reasoningLevel     string
	userMsgID          string
	chatCtx            *ChatContext
	bufferedTrajectory []map[string]any
	chatStartTime      time.Time
	// history is the recent-conversation block passed to the answer LLM
	// (ai.BuildAnswerMessages). Nil when chat_answer_history_enabled is off
	// or the chat has no prior turns.
	history []ai.ChatHistoryEntry
	// agentMode overrides the mode recorded in agent_decisions; empty means
	// the legacy "crag" (standard path).
	agentMode string
	// policyRule is the chat_orchestrator_policy rule index that pinned this
	// turn's route (W6-R6), recorded on the agent_decisions row. nil means
	// the flag ladder decided — which is what both constructors of this
	// struct pass today, since the policy is evaluated inside tryDeepChat
	// (see the note at each construction site).
	policyRule *int
	// queryType is the classifier's verdict for this turn (cls.QueryType),
	// used by the per-route answer-tool allowlist (W6-R8,
	// chat_answer_tools_by_route). Empty on handleTransformFollowUp, which
	// skips retrieval/classification entirely — an empty query type never
	// matches a configured route key, so Allowlist correctly reports "no
	// restriction" for that turn.
	queryType string
	// isGlobalSynthesis mirrors IsGlobalSynthesisQuery(searchQuery) at
	// construction time; the "global_synthesis" route key wins over
	// queryType's own entry only when this is true. False (the zero value)
	// on handleTransformFollowUp for the same reason as queryType above.
	isGlobalSynthesis bool
}

// handleTransformFollowUp answers a transform follow-up ("kannst du das als
// Tabelle erstellen?") without any retrieval: the system prompt embeds the
// previous AI answer verbatim (BuildTransformChatContext) and the sources of
// that answer are carried over. The previous answer is excluded from the
// answer-history block — it is already in the system prompt in full, while
// the history block caps each message.
func (h *Handler) handleTransformFollowUp(
	ctx context.Context,
	w http.ResponseWriter,
	span trace.Span,
	body sendMessageRequest,
	chatID, kbID, lang, userID string,
	anchor turnAnchor,
	prev *MessageRow,
	convRows []MessageRow,
	streamMode bool,
) {
	ctx, kbSystemPrompt := h.assembleSystemPrompt(ctx, chatID, kbID, userID, lang, body.Message)
	chatCtx := BuildTransformChatContext(*prev, kbSystemPrompt, lang)

	logctx.From(ctx).Info("rag.transform_followup.dispatch",
		"prev_message_id", prev.ID,
		"prev_answer_len", len(prev.Content),
		"source_count", len(chatCtx.Sources))

	userMsg, err := h.resolveTurnUserMessage(ctx, AddMessageParams{
		ChatID:  chatID,
		Role:    "user",
		Content: body.Message,
	}, anchor)
	if err != nil {
		logctx.From(ctx).Error("chat.send: save user message (transform)", "error", err, "chat_id", chatID, "kb_id", kbID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to save user message")
		return
	}

	var bufferedTrajectory []map[string]any
	if streamMode {
		emitTrajectory(func(d map[string]any) { bufferedTrajectory = append(bufferedTrajectory, d) },
			TrajectoryEvent{
				Stage:    "decision",
				Decision: "transform_followup",
				Reason:   "reformat previous answer; retrieval skipped",
			}, nil)
	}

	rp := chatResponseParams{
		span:               span,
		chatID:             chatID,
		kbID:               kbID,
		lang:               lang,
		userMessage:        body.Message,
		reasoningLevel:     resolveReasoningLevel(body),
		userMsgID:          userMsg.ID,
		chatCtx:            chatCtx,
		bufferedTrajectory: bufferedTrajectory,
		chatStartTime:      time.Now(),
		history:            h.answerHistory(ctx, historyRowsExcluding(convRows, prev.ID)),
		agentMode:          "transform_followup",
	}
	if streamMode {
		h.writeStreamingResponse(ctx, w, rp)
		return
	}
	h.writeJSONResponse(ctx, w, rp)
}

// writeStreamingResponse handles the SSE branch of SendMessage: sets
// headers, streams the AI response, persists the AI message, runs
// post-response tasks, and records the final agent_decision row.
// Caller has already saved the user message; ctx already carries the
// turn budget + tool-call recorder.
func (h *Handler) writeStreamingResponse(ctx context.Context, w http.ResponseWriter, p chatResponseParams) {
	// Freshness dates for the cited files (one batch query, fail-soft).
	// Runs before the `sources` frame below AND before the AddMessage that
	// persists the same slice, so the SSE payload and messages.sources agree.
	enrichSourceDates(ctx, h.fileDates, p.chatCtx.Sources)
	sources := p.chatCtx.Sources
	enhancedQuery := p.chatCtx.EnhancedQuery
	systemPrompt := p.chatCtx.SystemPrompt

	httputil.EnableSSE(w)

	sseFinished := false
	defer func() {
		if !sseFinished {
			writeSSEDone(ctx, w)
		}
	}()

	writeOpeningFrames(ctx, w, sources, enhancedQuery, p.chatID, p.userMsgID, p.chatCtx.Conflicts)

	// Replay any trajectory events that were buffered during
	// PrepareChatContext (CRAG branch decisions, etc.) so the
	// frontend's reasoning panel sees them in order.
	for _, evt := range p.bufferedTrajectory {
		writeSSE(ctx, w, evt)
	}

	// Stream AI completion. Mirror the tryDeepChat branch's conditional
	// so lookup/enumeration queries also get answer-time tool calling
	// when chat_answer_tools_enabled is on. The legacy ai.StreamCompletion
	// path runs byte-identically when the flag is off.
	streamStart := time.Now()
	var responseBuf, reasoningBuf strings.Builder
	// Degenerate-run guard (W5-R4) — same wiring as the orchestrator tail in
	// tryDeepChat: only the completion runs under the cancellable child ctx,
	// so a guard cancel ends generation without disturbing the SSE writes,
	// the AddMessage or the post-response tasks that follow.
	genCtx, cancelGen := context.WithCancel(ctx)
	defer cancelGen()
	guard := newAnswerGuard(ChatAnswerDegenerateRunLimit(ctx, h.siteConfigReader), p.lang, "web", cancelGen)
	streamEmit := newGuardedEmit(guard, &responseBuf, &reasoningBuf,
		func(s string) { writeSSE(ctx, w, map[string]string{"content": s}) },
		func(s string) { writeSSE(ctx, w, map[string]string{"reasoning": s}) },
	)
	useAnswerTools := ChatAnswerToolsEnabled(ctx, h.siteConfigReader) && h.toolDispatcher != nil
	// answerToolsDispatcher/catalog default to the unrestricted pair; a
	// per-route allowlist (W6-R8) narrows both together below so the catalog
	// projection and the dispatch boundary can never drift apart.
	var answerToolsDispatcher ToolDispatcher = h.toolDispatcher
	var catalog []ai.ChatTool
	if useAnswerTools {
		mcpDisp, _ := h.toolDispatcher.(*MCPDispatcher)
		if mcpDisp != nil {
			catalog = mcpDisp.AnswerToolCatalog(p.kbID)
		}
		byRoute := ChatAnswerToolsByRoute(ctx, h.siteConfigReader)
		if allow, ok := byRoute.Allowlist(p.queryType, p.isGlobalSynthesis); ok {
			answerToolsDispatcher, catalog = restrictToolsForRoute(h.toolDispatcher, catalog, allow, true)
			routeEvt := TrajectoryEvent{
				Stage:    "answer_tools_route",
				Decision: answerToolsRouteDecision(byRoute, p.queryType, p.isGlobalSynthesis),
				Findings: len(catalog),
			}
			if len(catalog) == 0 {
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
		err := RunAnswerWithTools(genCtx, AnswerToolsParams{
			AIResolver:      h.aiResolver,
			KbID:            p.kbID,
			ChatID:          p.chatID,
			SystemPrompt:    systemPrompt,
			UserPrompt:      p.userMessage,
			History:         p.history,
			Tools:           catalog,
			Dispatcher:      answerToolsDispatcher,
			MaxRounds:       ChatAnswerToolsMaxRounds(ctx, h.siteConfigReader),
			ReasoningEffort: p.reasoningLevel,
			Temperature:     ChatAnswerTemperature(ctx, h.siteConfigReader),
		}, streamEmit, answerTrace)
		// A guard trip cancels genCtx, which the tool loop reports as a
		// context error — a deliberate abort with a usable answer behind it,
		// not a stream failure. Only a real error bails.
		if err != nil && !guard.tripped() {
			logctx.From(ctx).Error("chat.send: run answer-tools", "error", err, "chat_id", p.chatID, "kb_id", p.kbID)
			writeSSE(ctx, w, map[string]string{"error": "failed to run AI stream"})
			writeSSEDone(ctx, w)
			sseFinished = true
			return
		}
	} else {
		events, err := ai.StreamCompletionWithHistory(genCtx, h.aiResolver, p.history, p.userMessage, systemPrompt, p.kbID, p.reasoningLevel, ChatAnswerTemperature(ctx, h.siteConfigReader))
		if err != nil {
			logctx.From(ctx).Error("chat.send: start AI stream", "error", err, "chat_id", p.chatID, "kb_id", p.kbID)
			writeSSE(ctx, w, map[string]string{"error": "failed to start AI stream"})
			writeSSEDone(ctx, w)
			sseFinished = true
			return
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
			logctx.From(ctx).Error("chat.send: AI stream aborted mid-answer", "error", streamErr, "chat_id", p.chatID, "kb_id", p.kbID)
			writeSSE(ctx, w, map[string]string{"error": "AI stream interrupted"})
			writeSSEDone(ctx, w)
			sseFinished = true
			return
		}
	}

	fullResponse := responseBuf.String()
	if guard.tripped() {
		// Strip the run from the persisted answer, append the notice, and
		// stream that notice as the final content frame so the user sees why
		// the answer stops. The turn completes normally from here.
		guarded, appended := guard.finish(fullResponse)
		fullResponse = guarded
		writeSSE(ctx, w, map[string]string{"content": appended})
		guard.recordTrajectory(func(pl map[string]any) { writeSSE(ctx, w, pl) })
		logctx.From(ctx).Warn("chat.send: degenerate answer run truncated",
			"chat_id", p.chatID, "kb_id", p.kbID,
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
		"reasoning_level", p.reasoningLevel,
		"source_count", len(sources),
		"low_confidence", len(sources) < 3,
		"stream", true,
		"answer_tools_path", runAnswerTools,
		"tool_calls", toolCallsThisTurn,
	)
	p.span.SetAttributes(
		attribute.Int("chat.source_count", len(sources)),
		attribute.Int("chat.answer_len", len(fullResponse)),
	)
	observability.RecordCompletion(true, time.Since(streamStart).Seconds())
	if len(sources) < 3 {
		observability.RecordLowConfidence()
	}

	var reasoningPtr *string
	if reasoningBuf.Len() > 0 {
		fullReasoning := reasoningBuf.String()
		reasoningPtr = &fullReasoning
	}
	aiMsg, err := h.store.AddMessage(ctx, AddMessageParams{
		ChatID:          p.chatID,
		Role:            "ai",
		Content:         fullResponse,
		Sources:         sources,
		Reasoning:       reasoningPtr,
		ParentMessageID: &p.userMsgID,
		Conflicts:       ConflictsForWire(p.chatCtx.Conflicts),
	})
	if err != nil {
		logctx.From(ctx).Error("chat.send: save AI message (stream)", "error", err, "chat_id", p.chatID, "kb_id", p.kbID)
		writeSSE(ctx, w, map[string]string{"error": "failed to save AI message"})
		writeSSEDone(ctx, w)
		sseFinished = true
		return
	}

	writeSSE(ctx, w, map[string]string{"aiMessageId": aiMsg.ID})

	// Follow-ups + factcheck have no data dependency on each other; run
	// them concurrently so the user sees verification as soon as the
	// slower of the two finishes instead of both serially.
	// refinedAnswer (AP-A1) is intentionally discarded here: AP-A2
	// streams the diff via the emit callback below, so the frontend
	// mutates the painted answer in place. The full refined text
	// is still persisted to DB by runPostResponseTasks.
	emit := func(pl map[string]any) { writeSSE(ctx, w, pl) }
	followUps, verification, _ := h.runPostResponseTasks(ctx, p.userMessage, fullResponse, p.chatCtx.Context, p.kbID, p.lang, aiMsg.ID, p.chatCtx.Sources, emit, p.chatCtx.TabularTrace)
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

	// Standard streaming path always exercises CRAG (via
	// PrepareChatContext) and never the orchestrators. The CRAG
	// outcome is recorded in the global metrics; per-chat logging
	// for the admin panel uses mode=crag with outcome derived from
	// the buffered trajectory's final decision event (when present).
	stdOutcome, _, _ := agentOutcomeFromEvents(p.bufferedTrajectory)
	if stdOutcome == "" {
		stdOutcome = "answered"
	}
	mode := p.agentMode
	if mode == "" {
		mode = "crag"
	}
	h.recordAgentDecision(ctx, p.kbID, mode, stdOutcome, 0, 0, time.Since(p.chatStartTime).Milliseconds(), nil, nil, p.policyRule)

	writeSSEDone(ctx, w)
	sseFinished = true
}

// writeJSONResponse handles the non-streaming JSON branch of
// SendMessage: generates the AI completion, persists the AI message,
// runs post-response tasks, and writes a single JSON response. The
// non-streaming path returns refinedAnswer when AP-A1 produced one
// (no painted-text mismatch concern since there's no SSE channel).
func (h *Handler) writeJSONResponse(ctx context.Context, w http.ResponseWriter, p chatResponseParams) {
	// Same one-shot enrichment as the streaming branch — see there.
	enrichSourceDates(ctx, h.fileDates, p.chatCtx.Sources)
	sources := p.chatCtx.Sources
	enhancedQuery := p.chatCtx.EnhancedQuery
	systemPrompt := p.chatCtx.SystemPrompt

	nonStreamStart := time.Now()
	result, err := ai.GenerateCompletionWithHistory(ctx, h.aiResolver, p.history, p.userMessage, systemPrompt, p.kbID, p.reasoningLevel != "")
	if err != nil {
		logctx.From(ctx).Error("chat.send: generate completion", "error", err, "chat_id", p.chatID, "kb_id", p.kbID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to generate response")
		return
	}
	// Degenerate-run guard, post hoc (W5-R4): nothing to abort on a
	// non-streaming completion, but the answer must not be persisted or
	// returned with the run in it.
	if guarded, appended := GuardAnswerText(ctx, h.siteConfigReader, result.Content, p.lang, "web"); appended != "" {
		logctx.From(ctx).Warn("chat.send: degenerate answer run stripped (non-streaming)",
			"chat_id", p.chatID, "kb_id", p.kbID)
		result.Content = guarded
	}

	logctx.From(ctx).Info("rag.completion",
		"stage", "llm_completion",
		"answer_len", len(result.Content),
		"reasoning_len", len(result.Reasoning),
		"source_count", len(sources),
		"low_confidence", len(sources) < 3,
		"stream", false,
	)
	p.span.SetAttributes(
		attribute.Int("chat.source_count", len(sources)),
		attribute.Int("chat.answer_len", len(result.Content)),
	)
	observability.RecordCompletion(false, time.Since(nonStreamStart).Seconds())
	if len(sources) < 3 {
		observability.RecordLowConfidence()
	}

	var reasoningPtr *string
	if result.Reasoning != "" {
		reasoningPtr = &result.Reasoning
	}
	aiMsg, err := h.store.AddMessage(ctx, AddMessageParams{
		ChatID:          p.chatID,
		Role:            "ai",
		Content:         result.Content,
		Sources:         sources,
		Reasoning:       reasoningPtr,
		ParentMessageID: &p.userMsgID,
		Conflicts:       ConflictsForWire(p.chatCtx.Conflicts),
	})
	if err != nil {
		logctx.From(ctx).Error("chat.send: save AI message", "error", err, "chat_id", p.chatID, "kb_id", p.kbID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to save AI message")
		return
	}

	// Non-streaming path: no SSE channel exists, so emit is nil.
	// The refined answer surfaces via the JSON `answer` field instead.
	followUps, verification, refinedAnswer := h.runPostResponseTasks(ctx, p.userMessage, result.Content, p.chatCtx.Context, p.kbID, p.lang, aiMsg.ID, p.chatCtx.Sources, nil, p.chatCtx.TabularTrace)

	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
		if err := h.store.UpdateMessageTraceID(ctx, aiMsg.ID, sc.TraceID().String()); err != nil {
			logctx.From(ctx).Warn("failed to persist message trace_id", "messageId", aiMsg.ID, "error", err)
		}
	}

	answerForClient := result.Content
	if refinedAnswer != "" {
		answerForClient = refinedAnswer
	}

	payload := map[string]any{
		"answer":            answerForClient,
		"reasoning":         result.Reasoning,
		"sources":           sources,
		"enhancedQuery":     enhancedQuery,
		"chatId":            p.chatID,
		"userMessageId":     p.userMsgID,
		"aiMessageId":       aiMsg.ID,
		"followUpQuestions": followUps,
		"verification":      verification,
	}
	// Added only when there is something to report, so a client that never
	// sees a conflict sees byte-identical JSON to before this existed —
	// same rule the SSE frame follows.
	if cs := ConflictsForWire(p.chatCtx.Conflicts); cs != nil {
		payload["conflicts"] = cs
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, payload)
}
