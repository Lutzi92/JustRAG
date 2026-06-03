// Package publicapi provides HTTP handlers for the public API v1.
// It exposes programmatic access to JustRAG features via API key authentication.
package publicapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/research"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the persistence interface required by Handler.
// It combines KB listing with the chat operations used by the internal chat package.
type Store interface {
	// KB listing
	ListKnowledgeBases(ctx context.Context, userID string, limit, offset int) ([]kb.KBRow, error)
	// Chat operations
	GetChats(ctx context.Context, kbID, userID string) ([]chat.ChatRow, error)
	GetChatByID(ctx context.Context, chatID string) (*chat.ChatRow, error)
	GetChatMessages(ctx context.Context, chatID string) ([]chat.MessageRow, error)
	CreateChat(ctx context.Context, kbID, userID, title string) (*chat.ChatRow, error)
	AddMessage(ctx context.Context, params chat.AddMessageParams) (*chat.MessageRow, error)
	GetKBSystemPrompt(ctx context.Context, kbID string) (*string, error)
	// Required by chat.CondenseFollowUp via GetChatMessages above, and GetMessageAncestors.
	GetMessageAncestors(ctx context.Context, messageID, chatID string) ([]chat.MessageRow, error)
}

// ---------------------------------------------------------------------------
// ResearchStore interface
// ---------------------------------------------------------------------------

// ResearchStore is the persistence interface for research operations.
type ResearchStore interface {
	CreateResearchSession(ctx context.Context, kbID, userID, goal, sessionType string) (*chat.ChatRow, error)
	SaveResearchResults(ctx context.Context, chatID, goal, report string, findings any) error
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the public API v1 endpoints.
type Handler struct {
	store         Store
	aiResolver    *ai.ConfigResolver
	searchService *vector.SearchService

	// Optional research dependencies — set via SetResearchDeps.
	researchStore ResearchStore
	redis         *redis.Client
}

// NewHandler creates a Handler backed by the given store, AI resolver, and
// search service.
func NewHandler(store Store, aiResolver *ai.ConfigResolver, searchSvc *vector.SearchService) *Handler {
	return &Handler{
		store:         store,
		aiResolver:    aiResolver,
		searchService: searchSvc,
	}
}

// SetResearchDeps injects the optional dependencies needed by the Research
// endpoint. When not called, Research returns 501.
func (h *Handler) SetResearchDeps(rs ResearchStore, rdb *redis.Client) {
	h.researchStore = rs
	h.redis = rdb
}

// ---------------------------------------------------------------------------
// chatStoreAdapter
// ---------------------------------------------------------------------------

// chatStoreAdapter wraps a publicapi.Store so it satisfies chat.Store.
// Methods not used by the publicapi package (DeleteChat, UpdateMessageFeedback)
// are stubbed out and should never be called.
type chatStoreAdapter struct {
	Store
}

func (a chatStoreAdapter) DeleteChat(_ context.Context, _ string) error { return nil }
func (a chatStoreAdapter) UpdateMessageFeedback(_ context.Context, _, _, _ string, _ *string, _ *string) error {
	return nil
}
func (a chatStoreAdapter) UpdateMessageVerification(_ context.Context, _ string, _ *chat.MessageVerification) error {
	return nil
}
func (a chatStoreAdapter) UpdateMessageContent(_ context.Context, _ string, _ string) error {
	return nil
}
func (a chatStoreAdapter) UpdateMessageTraceID(_ context.Context, _ string, _ string) error {
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// truncateGoal returns the first 100 runes of s followed by "…" if s is longer,
// so user-supplied goals are never logged in full.
func truncateGoal(s string) string {
	const maxRunes = 100
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

// writeSSE encodes data as JSON and writes a Server-Sent Events data frame,
// flushing the response writer if it supports http.Flusher.
func writeSSE(w http.ResponseWriter, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeSSEDone writes the SSE stream terminator and flushes.
func writeSSEDone(w http.ResponseWriter) {
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/kb
// ---------------------------------------------------------------------------

// ListKBs handles GET /api/v1/kb.
// Returns all KBs accessible to the API key user, with optional pagination via
// ?limit and ?offset query parameters. Auth is enforced by middleware in main.go.
func (h *Handler) ListKBs(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	limit := 50
	offset := 0

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	kbs, err := h.store.ListKnowledgeBases(r.Context(), user.ID, limit, offset)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch knowledge bases")
		return
	}
	if kbs == nil {
		kbs = []kb.KBRow{}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, kbs)
}

// ---------------------------------------------------------------------------
// GET /api/v1/kb/{id}/chats
// ---------------------------------------------------------------------------

// ListChats handles GET /api/v1/kb/{id}/chats.
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
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch chats")
		return
	}
	if chats == nil {
		chats = []chat.ChatRow{}
	}

	w.Header().Set("Cache-Control", "no-cache")
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, chats)
}

// ---------------------------------------------------------------------------
// GET /api/v1/kb/{id}/chats/{chatId}/messages
// ---------------------------------------------------------------------------

// GetMessages handles GET /api/v1/kb/{id}/chats/{chatId}/messages.
// Returns all messages for the specified chat, verifying that the chat belongs
// to the authenticated user AND to the requested KB. Auth is enforced by
// middleware in main.go.
func (h *Handler) GetMessages(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbID := r.PathValue("id")
	chatID := r.PathValue("chatId")

	chatRow, err := h.store.GetChatByID(r.Context(), chatID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch chat")
		return
	}
	if chatRow == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "chat not found")
		return
	}

	// Verify both ownership and KB membership.
	if chatRow.UserID != user.ID || chatRow.KbID != kbID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "chat not found")
		return
	}

	messages, err := h.store.GetChatMessages(r.Context(), chatID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch messages")
		return
	}
	if messages == nil {
		messages = []chat.MessageRow{}
	}

	w.Header().Set("Cache-Control", "no-cache")
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, messages)
}

// ---------------------------------------------------------------------------
// POST /api/v1/kb/{id}/chat
// ---------------------------------------------------------------------------

// sendMessageRequest is the parsed JSON body for the SendMessage endpoint.
type sendMessageRequest struct {
	Message          string   `json:"message"`
	ChatID           string   `json:"chatId"`
	ParentMessageID  string   `json:"parentMessageId"`
	Language         string   `json:"language"`
	SelectedFileIDs  []string `json:"selectedFileIds"`
	Enhance          string   `json:"enhance"`
	ReasoningEnabled bool     `json:"reasoningEnabled"`
	ReasoningLevel   string   `json:"reasoningLevel"`
}

// SendMessage handles POST /api/v1/kb/{id}/chat.
// Behaviour is identical to the internal chat SendMessage, with one difference:
// streaming is the default — ?stream=false must be passed explicitly to opt out.
// Auth + KB view permission are enforced by middleware in main.go.
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbID := r.PathValue("id")

	// Public API defaults to streaming; caller must pass ?stream=false to opt out.
	streamParam := r.URL.Query().Get("stream")
	streamMode := streamParam != "false"

	// ------------------------------------------------------------------
	// 1. Parse request body
	// ------------------------------------------------------------------
	var body sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// ------------------------------------------------------------------
	// 2. Validate message
	// ------------------------------------------------------------------
	if body.Message == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "message is required")
		return
	}
	if len([]rune(body.Message)) > 32_000 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "message exceeds maximum length of 32,000 characters")
		return
	}

	// ------------------------------------------------------------------
	// 3. Resolve or create chat
	// ------------------------------------------------------------------
	chatID := body.ChatID

	if chatID != "" {
		existing, err := h.store.GetChatByID(ctx, chatID)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch chat")
			return
		}
		if existing == nil || existing.UserID != user.ID || existing.KbID != kbID {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "access denied")
			return
		}
	} else {
		title := body.Message
		runes := []rune(title)
		if len(runes) > 50 {
			title = string(runes[:50])
		}
		newChat, err := h.store.CreateChat(ctx, kbID, user.ID, title)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create chat")
			return
		}
		chatID = newChat.ID
	}

	// ------------------------------------------------------------------
	// 4. Determine language
	// ------------------------------------------------------------------
	lang := body.Language
	if lang != "de" && lang != "en" {
		lang = "de"
	}

	// ------------------------------------------------------------------
	// 5. Condense follow-up query
	// ------------------------------------------------------------------
	var parentMsgID *string
	if body.ParentMessageID != "" {
		parentMsgID = &body.ParentMessageID
	}

	searchQuery, _ := chat.CondenseFollowUp(ctx, h.aiResolver, chatStoreAdapter{h.store}, chatID, parentMsgID, body.Message, kbID, lang)

	// ------------------------------------------------------------------
	// 6. Prepare chat context (RAG pipeline)
	// ------------------------------------------------------------------
	var kbSystemPrompt string
	if sp, err := h.store.GetKBSystemPrompt(ctx, kbID); err == nil && sp != nil {
		kbSystemPrompt = *sp
	}

	params := chat.ChatContextParams{
		KbID:           kbID,
		SearchQuery:    searchQuery,
		Language:       lang,
		Enhance:        body.Enhance,
		FileIDs:        body.SelectedFileIDs,
		KbSystemPrompt: kbSystemPrompt,
	}

	// nil siteConfig: public API runs CRAG only when explicitly opted in by
	// the chat handler path; the public-key endpoint stays on the legacy
	// behaviour for now.
	chatCtx, err := chat.PrepareChatContext(ctx, h.aiResolver, h.searchService, nil, params)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to prepare context")
		return
	}

	sources := chatCtx.Sources
	enhancedQuery := chatCtx.EnhancedQuery
	systemPrompt := chatCtx.SystemPrompt

	// ------------------------------------------------------------------
	// 7. Save user message
	// ------------------------------------------------------------------
	hasEnhanced := enhancedQuery != ""
	var enhancedQueryPtr *string
	if hasEnhanced {
		enhancedQueryPtr = &enhancedQuery
	}

	userMsg, err := h.store.AddMessage(ctx, chat.AddMessageParams{
		ChatID:          chatID,
		Role:            "user",
		Content:         body.Message,
		IsEnhanced:      hasEnhanced,
		EnhancedQuery:   enhancedQueryPtr,
		ParentMessageID: parentMsgID,
	})
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save user message")
		return
	}

	// ------------------------------------------------------------------
	// 8. Determine reasoning level
	// ------------------------------------------------------------------
	reasoningLevel := ""
	if body.ReasoningEnabled {
		reasoningLevel = body.ReasoningLevel
		if reasoningLevel == "" {
			reasoningLevel = "medium"
		}
	}

	// ------------------------------------------------------------------
	// Streaming path
	// ------------------------------------------------------------------
	if streamMode {
		httputil.EnableSSE(w)

		writeSSE(w, map[string]any{
			"sources":       sources,
			"enhancedQuery": enhancedQuery,
			"chatId":        chatID,
			"userMessageId": userMsg.ID,
		})

		events, err := ai.StreamCompletion(ctx, h.aiResolver, body.Message, systemPrompt, kbID, reasoningLevel != "")
		if err != nil {
			writeSSE(w, map[string]string{"error": "failed to start AI stream"})
			writeSSEDone(w)
			return
		}

		var fullResponse, fullReasoning string
		for event := range events {
			if event.Done {
				break
			}
			if event.Content != "" {
				fullResponse += event.Content
				writeSSE(w, map[string]string{"content": event.Content})
			}
			if event.Reasoning != "" {
				fullReasoning += event.Reasoning
				writeSSE(w, map[string]string{"reasoning": event.Reasoning})
			}
		}

		var reasoningPtr *string
		if fullReasoning != "" {
			reasoningPtr = &fullReasoning
		}
		aiMsg, err := h.store.AddMessage(ctx, chat.AddMessageParams{
			ChatID:          chatID,
			Role:            "ai",
			Content:         fullResponse,
			Sources:         sources,
			Reasoning:       reasoningPtr,
			ParentMessageID: &userMsg.ID,
		})
		if err != nil {
			writeSSE(w, map[string]string{"error": "failed to save AI message"})
			writeSSEDone(w)
			return
		}

		writeSSE(w, map[string]string{"aiMessageId": aiMsg.ID})

		followUps, _ := ai.GenerateFollowUpQuestions(ctx, h.aiResolver, body.Message, fullResponse, kbID, lang)
		if len(followUps) > 0 {
			writeSSE(w, map[string]any{"followUpQuestions": followUps})
		}

		writeSSEDone(w)
		return
	}

	// ------------------------------------------------------------------
	// Non-streaming path
	// ------------------------------------------------------------------
	result, err := ai.GenerateCompletion(ctx, h.aiResolver, body.Message, systemPrompt, kbID, reasoningLevel != "")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to generate response")
		return
	}

	var reasoningPtr *string
	if result.Reasoning != "" {
		reasoningPtr = &result.Reasoning
	}
	aiMsg, err := h.store.AddMessage(ctx, chat.AddMessageParams{
		ChatID:          chatID,
		Role:            "ai",
		Content:         result.Content,
		Sources:         sources,
		Reasoning:       reasoningPtr,
		ParentMessageID: &userMsg.ID,
	})
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save AI message")
		return
	}

	followUps, _ := ai.GenerateFollowUpQuestions(ctx, h.aiResolver, body.Message, result.Content, kbID, lang)

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
		"answer":            result.Content,
		"reasoning":         result.Reasoning,
		"sources":           sources,
		"enhancedQuery":     enhancedQuery,
		"chatId":            chatID,
		"userMessageId":     userMsg.ID,
		"aiMessageId":       aiMsg.ID,
		"followUpQuestions": followUps,
	})
}

// ---------------------------------------------------------------------------
// POST /api/v1/kb/{id}/research
// ---------------------------------------------------------------------------

// researchRequest is the parsed JSON body for the Research endpoint.
type researchRequest struct {
	Goal     string   `json:"goal"`
	MaxSteps int      `json:"maxSteps"`
	Language string   `json:"language"`
	FileIDs  []string `json:"fileIds"`
}

// Research handles POST /api/v1/kb/{id}/research.
// Behaviour mirrors the internal research endpoint. Streaming is the default;
// pass ?stream=false to receive a single JSON response instead.
// Auth + KB view permission are enforced by middleware.
func (h *Handler) Research(w http.ResponseWriter, r *http.Request) {
	if h.researchStore == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotImplemented, "research is not available")
		return
	}

	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbID := r.PathValue("id")

	// Public API defaults to streaming; caller must pass ?stream=false to opt out.
	streamParam := r.URL.Query().Get("stream")
	streamMode := streamParam != "false"

	var body researchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Goal == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "goal is required")
		return
	}
	if len([]rune(body.Goal)) > 32_000 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "goal exceeds maximum length of 32,000 characters")
		return
	}

	// Clamp maxSteps: default 5, max 20.
	if body.MaxSteps <= 0 {
		body.MaxSteps = 5
	}
	if body.MaxSteps > 20 {
		body.MaxSteps = 20
	}

	if body.Language == "" {
		body.Language = "de"
	}

	agent := research.NewAgent(kbID, body.Goal, research.Options{
		MaxSteps: body.MaxSteps,
		Language: body.Language,
		FileIDs:  body.FileIDs,
		Mode:     "kb",
	}, h.aiResolver, h.searchService, h.redis, nil)

	if streamMode {
		h.streamResearch(w, ctx, agent, kbID, user.ID, body)
		return
	}

	h.runResearchSync(w, ctx, agent, kbID, user.ID, body)
}

// streamResearch runs the research agent and streams progress events as SSE.
func (h *Handler) streamResearch(
	w http.ResponseWriter,
	ctx context.Context,
	agent *research.Agent,
	kbID, userID string,
	body researchRequest,
) {
	httputil.EnableSSE(w)

	var report string
	var findings []research.Finding
	var sessionID string

	for evt := range agent.Run(ctx) {
		switch evt.Type {
		case "complete":
			report = evt.Report
			findings = evt.Findings
		case "error":
			writeSSE(w, map[string]any{"type": "error", "message": evt.Message})
			logctx.From(ctx).Error("public api research error", "kbId", kbID, "goal", truncateGoal(body.Goal), "error", evt.Message)
		case "cancelled":
			writeSSE(w, map[string]any{"type": "cancelled", "message": "research was cancelled"})
			logctx.From(ctx).Info("public api research cancelled", "kbId", kbID, "goal", truncateGoal(body.Goal))
		default:
			// Forward all progress events (started, planning, searching, synthesizing, finding).
			writeSSE(w, evt)
		}
	}

	// Persist results (best-effort) using a detached context so persistence
	// completes even if the client disconnects and the request ctx is canceled.
	if report != "" {
		// Detach cancellation but preserve tracing / request-id values so the
		// persistence still correlates with the originating request.
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer persistCancel()
		session, dbErr := h.researchStore.CreateResearchSession(persistCtx, kbID, userID, body.Goal, "research")
		if dbErr != nil {
			logctx.From(persistCtx).Error("failed to create research session", "error", dbErr, "kbId", kbID)
		} else {
			sessionID = session.ID
			if saveErr := h.researchStore.SaveResearchResults(persistCtx, session.ID, body.Goal, report, findings); saveErr != nil {
				logctx.From(persistCtx).Error("failed to save research results", "error", saveErr, "sessionId", session.ID, "kbId", kbID)
			}
		}
	}

	// Send the final complete event with report and sessionId.
	writeSSE(w, map[string]any{
		"type":      "complete",
		"report":    report,
		"sessionId": sessionID,
	})
	writeSSEDone(w)
}

// runResearchSync runs the research agent synchronously and returns the
// final report as a single JSON response.
func (h *Handler) runResearchSync(
	w http.ResponseWriter,
	ctx context.Context,
	agent *research.Agent,
	kbID, userID string,
	body researchRequest,
) {
	var report string
	var findings []research.Finding
	var agentErr string

	for evt := range agent.Run(ctx) {
		switch evt.Type {
		case "complete":
			report = evt.Report
			findings = evt.Findings
		case "error":
			agentErr = evt.Message
			logctx.From(ctx).Error("public api research error", "kbId", kbID, "goal", truncateGoal(body.Goal), "error", evt.Message)
		case "cancelled":
			agentErr = "research was cancelled"
			logctx.From(ctx).Info("public api research cancelled", "kbId", kbID, "goal", truncateGoal(body.Goal))
		}
	}

	if report == "" {
		msg := "research failed"
		if agentErr != "" {
			msg = agentErr
		}
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, msg)
		return
	}

	// Persist results (best-effort) using a detached context so persistence
	// completes even if the client disconnects and the request ctx is canceled.
	// Detach cancellation but preserve tracing / request-id values so the
	// persistence still correlates with the originating request.
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer persistCancel()
	session, dbErr := h.researchStore.CreateResearchSession(persistCtx, kbID, userID, body.Goal, "research")
	if dbErr != nil {
		logctx.From(persistCtx).Error("failed to create research session", "error", dbErr, "kbId", kbID)
	} else if saveErr := h.researchStore.SaveResearchResults(persistCtx, session.ID, body.Goal, report, findings); saveErr != nil {
		logctx.From(persistCtx).Error("failed to save research results", "error", saveErr, "sessionId", session.ID, "kbId", kbID)
	}

	resp := map[string]any{
		"report": report,
	}
	if session != nil {
		resp["sessionId"] = session.ID
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, resp)
}
