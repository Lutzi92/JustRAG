// Package openaicompat provides an OpenAI-compatible API surface that maps
// JustRAG knowledge bases to OpenAI "models" and routes chat completions
// through the RAG pipeline.
package openaicompat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the subset of database operations required by Handler.
type Store interface {
	ListKnowledgeBases(ctx context.Context, userID string, limit, offset int) ([]kb.KBRow, error)
	ListGlobalKnowledgeBases(ctx context.Context, userID string, isAdmin bool) ([]kb.KBRow, error)
	GetKBByID(ctx context.Context, id string) (*kbaccess.KnowledgeBase, error)
	GetKBRole(ctx context.Context, kbID, userID string) (string, error)
	GetKBSystemPrompt(ctx context.Context, kbID string) (*string, error)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the OpenAI-compatible endpoints.
type Handler struct {
	store         Store
	aiResolver    *ai.ConfigResolver
	searchService *vector.SearchService
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

// ---------------------------------------------------------------------------
// OpenAI wire types
// ---------------------------------------------------------------------------

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Name    string `json:"name"`
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type completionChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type completionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   completionUsage    `json:"usage"`
}

// Streaming chunk types.

type chunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type completionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
}

// OpenAI error envelope.

type apiErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type apiError struct {
	Error apiErrorDetail `json:"error"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeAPIError(ctx context.Context, w http.ResponseWriter, status int, message string) {
	httputil.WriteJSONCtx(ctx, w, status, apiError{
		Error: apiErrorDetail{
			Message: message,
			Type:    "invalid_request_error",
			Code:    "invalid_request",
		},
	})
}

// randomBytes returns n cryptographically random bytes.
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// newCompletionID generates a unique completion ID.
func newCompletionID() string {
	return "chatcmpl-" + hex.EncodeToString(randomBytes(12))
}

// extractKBID strips the "kb-" prefix from a model name and returns the raw
// UUID. Returns an error if the model name is not in the expected format.
func extractKBID(model string) (string, error) {
	if !strings.HasPrefix(model, "kb-") {
		return "", fmt.Errorf("model must be in the format 'kb-{uuid}'")
	}
	id := strings.TrimPrefix(model, "kb-")
	if id == "" {
		return "", fmt.Errorf("model must be in the format 'kb-{uuid}'")
	}
	return id, nil
}

// checkKBAccess verifies that the authenticated user has at least view access
// to kbID, using the same role ladder (kbaccess.EffectiveRole) the in-app
// kbaccess middleware enforces. Returns a non-nil error string when access
// should be denied, along with the appropriate HTTP status code.
func (h *Handler) checkKBAccess(ctx context.Context, kbID, userID, userRole string) (int, string) {
	kbRow, err := h.store.GetKBByID(ctx, kbID)
	if err != nil {
		return http.StatusInternalServerError, "internal server error"
	}
	if kbRow == nil {
		return http.StatusNotFound, "knowledge base not found"
	}

	memberRole, err := h.store.GetKBRole(ctx, kbID, userID)
	if err != nil {
		return http.StatusInternalServerError, "internal server error"
	}

	role := kbaccess.EffectiveRole(kbRow, userRole, memberRole)
	if !kbaccess.AtLeast(role, kbaccess.RoleView) {
		return http.StatusForbidden, "access denied"
	}

	return 0, ""
}

// ---------------------------------------------------------------------------
// GET /openai/v1/models
// ---------------------------------------------------------------------------

// ListModels handles GET /openai/v1/models.
// It returns all KBs accessible to the authenticated user formatted as OpenAI
// model objects.
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		writeAPIError(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	isAdmin := user.Role == "admin" || user.Role == "superadmin"

	// Fetch personal + shared KBs (owned and shared with the user).
	personal, err := h.store.ListKnowledgeBases(ctx, user.ID, 1000, 0)
	if err != nil {
		writeAPIError(ctx, w, http.StatusInternalServerError, "failed to list knowledge bases")
		return
	}

	// Fetch global KBs.
	globals, err := h.store.ListGlobalKnowledgeBases(ctx, user.ID, isAdmin)
	if err != nil {
		writeAPIError(ctx, w, http.StatusInternalServerError, "failed to list knowledge bases")
		return
	}

	// Deduplicate by ID (personal list may already include globals for some
	// store implementations).
	seen := make(map[string]struct{}, len(personal)+len(globals))
	var all []kb.KBRow
	for _, row := range personal {
		if _, ok := seen[row.ID]; !ok {
			seen[row.ID] = struct{}{}
			all = append(all, row)
		}
	}
	for _, row := range globals {
		if _, ok := seen[row.ID]; !ok {
			seen[row.ID] = struct{}{}
			all = append(all, row)
		}
	}

	data := make([]modelObject, len(all))
	for i, row := range all {
		data[i] = modelObject{
			ID:      "kb-" + row.ID,
			Object:  "model",
			Created: row.CreatedAt.Unix(),
			OwnedBy: "justrag",
			Name:    row.Name,
		}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, modelList{
		Object: "list",
		Data:   data,
	})
}

// ---------------------------------------------------------------------------
// POST /openai/v1/chat/completions
// ---------------------------------------------------------------------------

// ChatCompletions handles POST /openai/v1/chat/completions.
// It validates the request, checks KB access, runs the RAG pipeline, and
// returns either a streaming or non-streaming OpenAI-format response.
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		writeAPIError(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	ctx = logctx.WithUser(ctx, user.ID)

	// Mirror the in-app chat handler so downstream RAG spans (rag.search,
	// rag.embed, rag.llm_completion, rag.factcheck) nest under the same
	// logical operation regardless of which entry point the client used.
	ctx, span := observability.Tracer().Start(ctx, "chat.send_message")
	defer span.End()

	// ------------------------------------------------------------------
	// 1. Parse request body.
	// ------------------------------------------------------------------
	var body completionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}

	// ------------------------------------------------------------------
	// 2. Validate required fields.
	// ------------------------------------------------------------------
	if body.Model == "" {
		writeAPIError(ctx, w, http.StatusBadRequest, "model is required")
		return
	}
	if len(body.Messages) == 0 {
		writeAPIError(ctx, w, http.StatusBadRequest, "messages must be a non-empty array")
		return
	}

	// ------------------------------------------------------------------
	// 3. Extract kbID from model name.
	// ------------------------------------------------------------------
	kbID, err := extractKBID(body.Model)
	if err != nil {
		writeAPIError(ctx, w, http.StatusBadRequest, httputil.SanitizeError(err))
		return
	}

	ctx = logctx.WithKB(ctx, kbID)
	span.SetAttributes(
		attribute.String("chat.kb_id", kbID),
		attribute.Bool("chat.stream", body.Stream),
	)

	// ------------------------------------------------------------------
	// 4. Check user has access to the KB.
	// ------------------------------------------------------------------
	if status, msg := h.checkKBAccess(ctx, kbID, user.ID, user.Role); status != 0 {
		writeAPIError(ctx, w, status, msg)
		return
	}

	// ------------------------------------------------------------------
	// 5. Parse messages: collect system content, conversation history,
	//    and the last user message.
	// ------------------------------------------------------------------
	var systemParts []string
	var history []ai.ChatHistoryEntry
	var lastUserMessage string

	for _, msg := range body.Messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)
		case "user", "assistant":
			history = append(history, ai.ChatHistoryEntry{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	// The last user message is the query; strip it from history so it is not
	// sent twice to CondenseQuestion.
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			lastUserMessage = history[i].Content
			history = append(history[:i], history[i+1:]...)
			break
		}
	}

	if lastUserMessage == "" {
		writeAPIError(ctx, w, http.StatusBadRequest, "messages must contain at least one user message")
		return
	}

	// ------------------------------------------------------------------
	// 6. Condense follow-up query if there is conversation history.
	// ------------------------------------------------------------------
	searchQuery := lastUserMessage
	if len(history) >= 2 {
		condensed, err := ai.CondenseQuestion(ctx, h.aiResolver, history, lastUserMessage, kbID, "en")
		if err == nil && condensed != "" {
			searchQuery = condensed
		}
	}

	// ------------------------------------------------------------------
	// 7. Load KB system prompt.
	// ------------------------------------------------------------------
	var kbSystemPrompt string
	if sp, err := h.store.GetKBSystemPrompt(ctx, kbID); err == nil && sp != nil {
		kbSystemPrompt = *sp
	}

	// Prepend any system messages from the request to the KB prompt.
	if len(systemParts) > 0 {
		prefix := strings.Join(systemParts, "\n\n")
		if kbSystemPrompt != "" {
			kbSystemPrompt = prefix + "\n\n" + kbSystemPrompt
		} else {
			kbSystemPrompt = prefix
		}
	}

	// ------------------------------------------------------------------
	// 8. Run the RAG context pipeline.
	// ------------------------------------------------------------------
	params := chat.ChatContextParams{
		KbID:           kbID,
		SearchQuery:    searchQuery,
		Language:       "en",
		KbSystemPrompt: kbSystemPrompt,
	}

	// OpenAI-compat clients don't read site_configs — pass nil so CRAG is
	// implicitly disabled here (its toggles only meaningfully apply via
	// the in-app chat UI).
	chatCtx, err := chat.PrepareChatContext(ctx, h.aiResolver, h.searchService, nil, params)
	if err != nil {
		logctx.From(ctx).Error("openaicompat: PrepareChatContext failed",
			"error", err,
			"kbId", kbID,
		)
		writeAPIError(ctx, w, http.StatusInternalServerError, "failed to prepare context")
		return
	}

	// ------------------------------------------------------------------
	// 9. Generate completion (streaming or non-streaming).
	// ------------------------------------------------------------------
	completionID := newCompletionID()
	created := time.Now().Unix()

	// Pass the (capped) client-supplied conversation history through to the
	// answer call — OpenAI-compat clients (ILIAS, OpenWebUI) send the full
	// transcript and expect multi-turn semantics; dropping it made the model
	// claim it had no previous conversation on follow-ups.
	answerHistory := capAnswerHistory(history)

	if body.Stream {
		h.streamResponse(w, ctx, answerHistory, lastUserMessage, chatCtx.SystemPrompt, kbID, body.Model, completionID, created)
		return
	}

	h.nonStreamResponse(w, ctx, answerHistory, lastUserMessage, chatCtx.SystemPrompt, kbID, body.Model, completionID, created)
}

// capAnswerHistory bounds the client-supplied history before it reaches the
// answer LLM: last 6 messages, each truncated to 4000 runes — the same
// defaults as the in-app chat's chat_answer_history_* knobs (this endpoint
// has no site-config reader, so the caps are fixed).
func capAnswerHistory(history []ai.ChatHistoryEntry) []ai.ChatHistoryEntry {
	const maxMessages, maxRunes = 6, 4000
	if len(history) == 0 {
		return nil
	}
	if len(history) > maxMessages {
		history = history[len(history)-maxMessages:]
	}
	out := make([]ai.ChatHistoryEntry, 0, len(history))
	for _, h := range history {
		content := h.Content
		if r := []rune(content); len(r) > maxRunes {
			content = string(r[:maxRunes])
		}
		out = append(out, ai.ChatHistoryEntry{Role: h.Role, Content: content})
	}
	return out
}

// ---------------------------------------------------------------------------
// Non-streaming path
// ---------------------------------------------------------------------------

func (h *Handler) nonStreamResponse(
	w http.ResponseWriter,
	ctx context.Context,
	history []ai.ChatHistoryEntry,
	prompt, systemPrompt, kbID, model, completionID string,
	created int64,
) {
	result, err := ai.GenerateCompletionWithHistory(ctx, h.aiResolver, history, prompt, systemPrompt, kbID, false)
	if err != nil {
		writeAPIError(ctx, w, http.StatusInternalServerError, "failed to generate response")
		return
	}

	resp := completionResponse{
		ID:      completionID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []completionChoice{
			{
				Index: 0,
				Message: chatMessage{
					Role:    "assistant",
					Content: result.Content,
				},
				FinishReason: "stop",
			},
		},
		Usage: completionUsage{},
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Streaming path
// ---------------------------------------------------------------------------

func writeSSEChunk(w http.ResponseWriter, chunk completionChunk) {
	b, _ := json.Marshal(chunk)
	// Sliding per-frame deadline: bounds how long a half-open client can
	// block this goroutine (see httputil.SSEWriteTimeout).
	httputil.RearmSSEWriteDeadline(w)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeSSEDone(w http.ResponseWriter) {
	httputil.RearmSSEWriteDeadline(w)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *Handler) streamResponse(
	w http.ResponseWriter,
	ctx context.Context,
	history []ai.ChatHistoryEntry,
	prompt, systemPrompt, kbID, model, completionID string,
	created int64,
) {
	httputil.EnableSSE(w)

	// Initial chunk with role.
	writeSSEChunk(w, completionChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []chunkChoice{
			{
				Index:        0,
				Delta:        chunkDelta{Role: "assistant"},
				FinishReason: nil,
			},
		},
	})

	events, err := ai.StreamCompletionWithHistory(ctx, h.aiResolver, history, prompt, systemPrompt, kbID, "", ai.DefaultAnswerTemperature)
	if err != nil {
		// The initial assistant-role chunk has already gone out, so we can't
		// fall back to an HTTP error. Log for operators and emit an
		// OpenAI-compat error object before closing the stream so clients see
		// something diagnostic instead of a silent termination.
		logctx.From(ctx).Error("openaicompat: StreamCompletion failed",
			"error", err,
			"kbId", kbID,
		)
		errPayload, _ := json.Marshal(apiError{Error: apiErrorDetail{
			Message: httputil.SanitizeError(err),
			Type:    "internal_error",
			Code:    "stream_failed",
		}})
		fmt.Fprintf(w, "data: %s\n\n", errPayload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		writeSSEDone(w)
		return
	}

	var streamErr error
	for event := range events {
		if event.Done {
			streamErr = event.Err
			break
		}
		if event.Content != "" {
			writeSSEChunk(w, completionChunk{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []chunkChoice{
					{
						Index:        0,
						Delta:        chunkDelta{Content: event.Content},
						FinishReason: nil,
					},
				},
			})
		}
	}

	if streamErr != nil {
		// Mid-stream abort: don't pretend the completion finished with
		// "stop" — emit an OpenAI-compat error object so clients see a
		// diagnostic instead of a silently truncated answer.
		logctx.From(ctx).Error("openaicompat: AI stream aborted mid-answer",
			"error", streamErr,
			"kbId", kbID,
		)
		errPayload, _ := json.Marshal(apiError{Error: apiErrorDetail{
			Message: streamErr.Error(),
			Type:    "internal_error",
			Code:    "stream_interrupted",
		}})
		fmt.Fprintf(w, "data: %s\n\n", errPayload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		writeSSEDone(w)
		return
	}

	// Final chunk with finish_reason.
	stopReason := "stop"
	writeSSEChunk(w, completionChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []chunkChoice{
			{
				Index:        0,
				Delta:        chunkDelta{},
				FinishReason: &stopReason,
			},
		},
	})

	writeSSEDone(w)
}
