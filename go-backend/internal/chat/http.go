package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chatattach"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/longmem"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/sessionmem"
	"github.com/justrag/go-backend/internal/store"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the chat CRUD endpoints.
type Handler struct {
	store              Store
	aiResolver         *ai.ConfigResolver
	searchService      vector.Searcher
	rdb                *redis.Client // optional, for deep chat relay
	siteConfigReader   SiteConfigReader
	asynqClient        *asynq.Client           // optional, for Phase 3 §G RAGAS sampling
	decisionRecorder   DecisionRecorder        // optional, Phase 1 §1.4 admin metrics panel
	toolDispatcher     ToolDispatcher          // optional, Phase 2 §2.1 MCP tool dispatch
	sessionMemory      sessionmem.Store        // optional, Phase 2 §2.3 chat-session memory
	kbRouterCandidates KBRouterCandidateLister // optional, AP-A4 sub-KB router
	kgStore            kg.Store                // optional, AP-C4 graph-routing heuristic
	longmemStore       longmem.Store           // optional, AP-D1 per-user memory
	tabularCatalog     TabularCatalogChecker   // optional, Phase-3 chart-guidance gate
	recencyLister      RecencyLister           // optional, deterministic recency-listing path
	// raptorDescendants is the Phase F bridge that resolves a set of
	// RAPTOR summary chunk ids to their transitive leaf descendants.
	// Used by runPostResponseTasks to feed the citation validator's
	// n-gram check the leaf bodies before checking a summary
	// citation. Optional — when nil, summary citations validate
	// against summary content alone (legacy n-gram path; the
	// semantic-similarity fallback still applies).
	raptorDescendants RaptorDescendantsResolver
	// kbConfigStore loads per-KB site_config overrides (kb_site_configs).
	// Optional — when nil, forKB is a no-op and every KB uses the global
	// reader + shared SearchService (zero added cost).
	kbConfigStore KBConfigOverrideLister
	// corpusChunks reads full per-file chunk text for corpus-comparison
	// table queries. Optional — when nil, RunCorpusTableChat falls back
	// to an error path (guarded by the caller before dispatch).
	corpusChunks CorpusChunkReader
	// attachmentStore persists parsed in-chat comparison attachments
	// (uploaded documents compared against a KB, never ingested).
	// Optional — when nil, UploadAttachment guards the call with an
	// explicit 500 ("feature not fully initialized") rather than
	// panicking on a nil-interface .Put; the CompareEnabled gate
	// short-circuits before that on deployments where the feature is
	// off. Wired in a later task.
	attachmentStore chatattach.Store
	// parserFactory parses uploaded comparison attachments in memory
	// (via a temp file, since the parser API reads from disk). Built
	// from parser.DefaultFactoryWith(nil) when unset so UploadAttachment
	// always has a working factory without extra wiring.
	parserFactory *parser.Factory
}

// RaptorDescendantsResolver is the narrow interface
// runPostResponseTasks uses to expand RAPTOR summary citations.
// Returns a map keyed by summary chunk id → list of descendant leaf
// contents. Empty input yields nil. Missing ids (summary deleted
// between search and validation) are silently absent from the map.
// vector.ChunkService.GetRaptorDescendantLeafContentsAcrossDims
// satisfies this interface — wire it via
// WithRaptorDescendantsResolver. The "AcrossDims" suffix on the
// concrete method is what lets the chat handler stay
// dimension-agnostic; threading the dim through every orchestrator
// just for citation validation would be over-engineering.
type RaptorDescendantsResolver interface {
	GetRaptorDescendantLeafContentsAcrossDims(ctx context.Context, summaryIDs []string) (map[string][]string, error)
}

// DecisionRecorder is the persistence surface the chat handler uses to log
// per-chat agent-decision rows for the admin metrics panel. Inserts are
// fire-and-forget in a goroutine so a transient DB hiccup never propagates
// to the user-visible chat response. The interface is intentionally tiny so
// production (adminagentmetrics.PgStore) and tests can each satisfy it
// without dragging in unrelated dependencies.
type DecisionRecorder interface {
	// Record persists one chat-decision row. toolCalls is the AP-B4
	// per-turn tool-dispatch sequence captured by ToolCallRecorder;
	// nil/empty signals "no MCP calls this turn" and lands as an
	// empty JSONB array on disk. Implementations are fire-and-forget
	// — failures should log and drop, never propagate.
	Record(ctx context.Context, kbID, mode, outcome string, hops, rounds, latencyMs int, toolCalls []ToolCallRecord)
}

// TabularCatalogChecker reports whether a KB has materialized tabular sheets.
// Satisfied by *tabular.Catalog. Optional — when nil, the Phase-3 chart
// guidance is never injected.
type TabularCatalogChecker interface {
	HasDataForKB(ctx context.Context, kbID string) (bool, error)
}

// HandlerOption is a functional option for NewHandler.
type HandlerOption func(*Handler)

// WithRedis attaches a Redis client to the Handler (used for deep chat relay).
func WithRedis(rdb *redis.Client) HandlerOption {
	return func(h *Handler) {
		h.rdb = rdb
	}
}

// WithSiteConfigReader attaches a SiteConfigReader to the Handler (used to
// gate factchecking via the factcheck_in_chat site-config key).
func WithSiteConfigReader(r SiteConfigReader) HandlerOption {
	return func(h *Handler) {
		h.siteConfigReader = r
	}
}

// WithAsynqClient attaches an Asynq client to the Handler so post-response
// tasks (Phase 3 §G RAGAS sampling) can be enqueued. Optional — when
// absent, the sampling code path skips enqueue entirely.
func WithAsynqClient(c *asynq.Client) HandlerOption {
	return func(h *Handler) {
		h.asynqClient = c
	}
}

// WithDecisionRecorder attaches a DecisionRecorder so the handler can
// emit per-chat agent-decision rows for the Phase 1 §1.4 admin metrics
// panel. Optional — when absent, the recording call short-circuits so
// deployments that don't run the admin panel pay zero overhead.
func WithDecisionRecorder(r DecisionRecorder) HandlerOption {
	return func(h *Handler) {
		h.decisionRecorder = r
	}
}

// WithToolDispatcher attaches a Phase 2 §2.1 MCP tool dispatcher to the
// handler. When provided AND the chat_use_mcp_tools site_config is true,
// the plan-execute orchestrator routes its iterate-stage searches
// through the registry. Optional — when absent, both orchestrators
// fall back to direct vector.SearchService.Search calls.
func WithToolDispatcher(d ToolDispatcher) HandlerOption {
	return func(h *Handler) {
		h.toolDispatcher = d
	}
}

// WithSessionMemory attaches the Phase 2 §2.3 session-memory store. When
// provided, every chat turn prepends the session's Conversation memory:
// block to the orchestrator system prompt so multi-turn conversations
// can carry durable facts forward. Optional — when absent, no memory
// is read or written and the orchestrators run as if the chat were
// stateless.
func WithSessionMemory(s sessionmem.Store) HandlerOption {
	return func(h *Handler) {
		h.sessionMemory = s
	}
}

// WithLongmemStore attaches the AP-D1 long-term memory store.
// When provided AND chat_longmem_enabled is on, the chat handler
// recalls the user's durable facts at turn start (prepended to
// the system prompt) and writes new facts at turn end (extracted
// via ai.ExtractSalientFacts, fire-and-forget). Optional — when
// absent, the longmem branch short-circuits and chat behaves as
// if memory were off.
//
// Privacy: enabling this requires the DSGVO drawer (per-row
// delete + bulk-clear + JSON export) to be wired in the frontend.
// The store's DeleteByUser method handles the bulk path; the
// drawer UI is a separate iteration.
func WithLongmemStore(s longmem.Store) HandlerOption {
	return func(h *Handler) {
		h.longmemStore = s
	}
}

// WithKGStore attaches the AP-C4 graph-routing kg.Store. When
// provided AND chat_graph_routing_enabled is on, the chat handler
// runs `NeedsGraphTraversal` before the standard search pipeline
// and emits a `graph_traversal` trajectory event when a query
// matches a kg_entity alias. Optional — when absent, the
// heuristic short-circuits with `db_error` outcome (see metric
// `rag_graph_traversal_total`).
func WithKGStore(s kg.Store) HandlerOption {
	return func(h *Handler) {
		h.kgStore = s
	}
}

// WithKBRouterCandidates attaches the AP-A4 sub-KB router candidate
// lister. When provided AND `chat_kb_router_enabled` is on AND the
// request signals `?route=auto`, the chat handler asks an LLM to pick
// the right KB for the query and overrides the URL kb_id with the
// router's choice. Optional — when absent (or the gate is off, or
// auto isn't requested), the URL kb_id is used as-is.
func WithKBRouterCandidates(l KBRouterCandidateLister) HandlerOption {
	return func(h *Handler) {
		h.kbRouterCandidates = l
	}
}

// WithTabularCatalog attaches the Phase-3 tabular-catalog checker used to gate
// chart guidance to KBs that actually have spreadsheet data.
func WithTabularCatalog(c TabularCatalogChecker) HandlerOption {
	return func(h *Handler) {
		h.tabularCatalog = c
	}
}

// WithRecencyLister attaches the recency-listing file lookup backing the
// deterministic "what is new / recently added" path (recency_listing.go).
// Production wiring adapts mcp/builtin.PgxRecentDocsStore. Optional —
// when absent, recency queries fall back to plain semantic retrieval.
func WithRecencyLister(l RecencyLister) HandlerOption {
	return func(h *Handler) {
		h.recencyLister = l
	}
}

// WithRaptorDescendantsResolver attaches the Phase F bridge that
// resolves RAPTOR summary chunk ids to their transitive leaf
// descendants. Production wiring passes vector.ChunkService —
// which implements GetRaptorDescendantLeafContents over the
// recursive CTE on raptor_parent_id. Optional — when absent,
// summary citations validate against summary content alone.
func WithRaptorDescendantsResolver(r RaptorDescendantsResolver) HandlerOption {
	return func(h *Handler) {
		h.raptorDescendants = r
	}
}

// WithKBConfigStore attaches the per-KB site_config override lister. When
// provided, the chat handler overlays a KB's kb_site_configs values onto the
// global reader for that turn (registry keys only). Optional — when absent,
// per-KB overrides are inert and all KBs read global config.
func WithKBConfigStore(s KBConfigOverrideLister) HandlerOption {
	return func(h *Handler) {
		h.kbConfigStore = s
	}
}

// WithCorpusChunks attaches the corpus-table per-file chunk reader used by
// RunCorpusTableChat to assemble full file text for comparison queries.
// Production wiring passes *vector.ChunkService, which satisfies
// CorpusChunkReader via GetChunksByFileID. Optional — when absent, the
// corpus-table dispatch path is inert.
func WithCorpusChunks(r CorpusChunkReader) HandlerOption {
	return func(h *Handler) {
		h.corpusChunks = r
	}
}

// WithAttachmentStore attaches the in-chat comparison-attachment store used by
// UploadAttachment to persist parsed uploaded documents (compared against a KB,
// never ingested). Production wiring passes chatattach.NewRedisStore. Optional —
// when absent, UploadAttachment guards the call with an explicit 500 rather than
// dereferencing a nil store.
func WithAttachmentStore(s chatattach.Store) HandlerOption {
	return func(h *Handler) {
		h.attachmentStore = s
	}
}

// NewHandler creates a Handler backed by the given store, AI resolver, and
// search service. Optional functional options (e.g. WithRedis) configure
// additional dependencies.
func NewHandler(store Store, aiResolver *ai.ConfigResolver, searchService vector.Searcher, opts ...HandlerOption) *Handler {
	h := &Handler{
		store:         store,
		aiResolver:    aiResolver,
		searchService: searchService,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// GET /api/kb/{id}/chats
// ---------------------------------------------------------------------------

// ListChats handles GET /api/kb/{id}/chats.
// Returns all chats for the authenticated user in the specified knowledge base.
// Auth + KB view permission are enforced by middleware in main.go.
func (h *Handler) ListChats(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbID := r.PathValue("id")

	chats, err := h.store.GetChats(r.Context(), kbID, user.ID)
	if err != nil {
		logctx.From(r.Context()).Error("chat.list: get chats", "error", err, "kb_id", kbID, "user_id", user.ID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch chats")
		return
	}
	if chats == nil {
		chats = []ChatRow{}
	}

	w.Header().Set("Cache-Control", "no-cache")
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, chats)
}

// ---------------------------------------------------------------------------
// GET /api/chats/{id}/messages
// ---------------------------------------------------------------------------

// GetMessages handles GET /api/chats/{id}/messages.
// Returns all messages for the specified chat, verified to belong to the caller.
// Auth is enforced by middleware in main.go.
func (h *Handler) GetMessages(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	chatID := r.PathValue("id")

	chat, err := h.store.GetChatByID(r.Context(), chatID)
	if err != nil {
		logctx.From(r.Context()).Error("chat.get_messages: get chat", "error", err, "chat_id", chatID, "user_id", user.ID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch chat")
		return
	}
	if chat == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "chat not found")
		return
	}

	if chat.UserID != user.ID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "chat not found")
		return
	}

	messages, err := h.store.GetChatMessages(r.Context(), chatID)
	if err != nil {
		logctx.From(r.Context()).Error("chat.get_messages: list messages", "error", err, "chat_id", chatID, "user_id", user.ID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch messages")
		return
	}
	if messages == nil {
		messages = []MessageRow{}
	}

	w.Header().Set("Cache-Control", "no-cache")
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, messages)
}

// ---------------------------------------------------------------------------
// DELETE /api/chats/{id}
// ---------------------------------------------------------------------------

// DeleteChat handles DELETE /api/chats/{id}.
// Deletes the specified chat after verifying it belongs to the caller.
// Auth is enforced by middleware in main.go.
func (h *Handler) DeleteChat(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	chatID := r.PathValue("id")

	chat, err := h.store.GetChatByID(r.Context(), chatID)
	if err != nil {
		logctx.From(r.Context()).Error("chat.delete: get chat", "error", err, "chat_id", chatID, "user_id", user.ID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch chat")
		return
	}
	if chat == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "chat not found")
		return
	}

	if chat.UserID != user.ID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "chat not found")
		return
	}

	if err := h.store.DeleteChat(r.Context(), chatID); err != nil {
		logctx.From(r.Context()).Error("chat.delete: delete chat", "error", err, "chat_id", chatID, "user_id", user.ID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete chat")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/chats/{chatId}/messages/{messageId}/feedback
// ---------------------------------------------------------------------------

// feedbackRequest is the parsed JSON body for the feedback endpoint.
type feedbackRequest struct {
	Feedback *string `json:"feedback"`
	Comment  *string `json:"comment,omitempty"`
}

const maxFeedbackCommentLen = 2000

// SubmitFeedback handles POST /api/kb/{id}/chats/{chatId}/messages/{messageId}/feedback.
// Updates message feedback after verifying the chat belongs to the caller.
// Auth + KB view permission are enforced by middleware in main.go.
func (h *Handler) SubmitFeedback(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	chatID := r.PathValue("chatId")
	messageID := r.PathValue("messageId")

	chat, err := h.store.GetChatByID(r.Context(), chatID)
	if err != nil {
		logctx.From(r.Context()).Error("chat.feedback: get chat", "error", err, "chat_id", chatID, "message_id", messageID, "user_id", user.ID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch chat")
		return
	}
	if chat == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "chat not found")
		return
	}
	if chat.UserID != user.ID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "chat not found")
		return
	}

	var body feedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validation: comment length cap.
	if body.Comment != nil && len(*body.Comment) > maxFeedbackCommentLen {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest,
			fmt.Sprintf("comment too long (max %d chars)", maxFeedbackCommentLen))
		return
	}
	// Validation: cleared rating cannot carry a non-empty comment.
	if body.Feedback == nil && body.Comment != nil && *body.Comment != "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest,
			"cannot clear feedback with a comment")
		return
	}

	if err := h.store.UpdateMessageFeedback(r.Context(), messageID, chatID, user.ID, body.Feedback, body.Comment); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "message not found")
		} else {
			logctx.From(r.Context()).Error("chat.feedback: update feedback", "error", err, "chat_id", chatID, "message_id", messageID, "user_id", user.ID)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update feedback")
		}
		return
	}

	// Determine the metric label: "positive", "negative", or "cleared".
	rating := "cleared"
	if body.Feedback != nil {
		rating = *body.Feedback
	}
	observability.RecordChatFeedback(rating)

	// Structured log carries the high-cardinality fields.
	hasComment := body.Comment != nil && *body.Comment != ""
	commentLen := 0
	if hasComment {
		commentLen = len(*body.Comment)
	}
	logctx.From(r.Context()).Info("chat.feedback.submitted",
		"message_id", messageID,
		"chat_id", chatID,
		"kb_id", chat.KbID,
		"user_id", user.ID,
		"rating", rating,
		"has_comment", hasComment,
		"comment_len", commentLen,
	)

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]bool{"success": true})
}
