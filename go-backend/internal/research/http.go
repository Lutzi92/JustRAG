// Package research implements the HTTP handler for research endpoints.
package research

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/sserelay"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// ResearchStore is the database interface used by the research handler.
type ResearchStore interface {
	CreateResearchSession(ctx context.Context, kbID, userID, goal, sessionType string) (*chat.ChatRow, error)
	SaveResearchResults(ctx context.Context, chatID, goal, report string, findings any) error
	GetChatByID(ctx context.Context, chatID string) (*chat.ChatRow, error)
	GetChatMessages(ctx context.Context, chatID string) ([]chat.MessageRow, error)
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the research HTTP endpoints.
type Handler struct {
	store      ResearchStore
	relay      *sserelay.Relay
	aiResolver *ai.ConfigResolver
	searchSvc  *vector.SearchService
	redis      *redis.Client
}

// NewHandler creates a Handler backed by the given dependencies.
func NewHandler(
	store ResearchStore,
	relay *sserelay.Relay,
	aiResolver *ai.ConfigResolver,
	searchSvc *vector.SearchService,
	rdb *redis.Client,
) *Handler {
	return &Handler{
		store:      store,
		relay:      relay,
		aiResolver: aiResolver,
		searchSvc:  searchSvc,
		redis:      rdb,
	}
}

// clampMaxSteps bounds maxSteps to [1, 20], defaulting to 10 if <= 0.
func clampMaxSteps(n int) int {
	if n <= 0 {
		return 10
	}
	if n > 20 {
		return 20
	}
	return n
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/research
// ---------------------------------------------------------------------------

// startResearchRequest is the parsed JSON body for the StartResearch endpoint.
type startResearchRequest struct {
	Goal     string   `json:"goal"`
	MaxSteps int      `json:"maxSteps"`
	Language string   `json:"language"`
	FileIDs  []string `json:"fileIds"`
}

// StartResearch handles POST /api/kb/{id}/research.
// KB view permission is enforced by middleware.
//
// If ?stream=true the request is forwarded to the research-execution worker via
// the SSE relay. Otherwise, a single-step completion is generated and returned.
func (h *Handler) StartResearch(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbID := r.PathValue("id")

	var body startResearchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Goal == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "goal is required")
		return
	}
	if len(body.Goal) > 2000 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "goal must not exceed 2000 characters")
		return
	}
	body.MaxSteps = clampMaxSteps(body.MaxSteps)
	if body.Language == "" {
		body.Language = "en"
	}

	streaming := r.URL.Query().Get("stream") == "true"

	if streaming {
		h.startStreamingResearch(w, r, kbID, user.ID, body, "kb")
		return
	}

	h.runSimpleResearch(w, r, kbID, user.ID, body)
}

// startStreamingResearch creates a session and delegates to the SSE relay.
func (h *Handler) startStreamingResearch(
	w http.ResponseWriter,
	r *http.Request,
	kbID, userID string,
	body startResearchRequest,
	mode string,
) {
	ctx := r.Context()

	// Create research session in DB first — use its ID as the researchID
	// so the abort endpoint can map the session ID to the correct Redis key.
	session, err := h.store.CreateResearchSession(ctx, kbID, userID, body.Goal, "research")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create research session")
		return
	}
	researchID := session.ID

	// Build worker payload.
	type workerPayload struct {
		ResearchID string   `json:"researchId"`
		KbID       string   `json:"kbId"`
		Goal       string   `json:"goal"`
		MaxSteps   int      `json:"maxSteps"`
		Language   string   `json:"language"`
		Mode       string   `json:"mode"`
		FileIDs    []string `json:"fileIds"`
		SessionID  string   `json:"sessionId"`
	}

	payload, err := json.Marshal(workerPayload{
		ResearchID: researchID,
		KbID:       kbID,
		Goal:       body.Goal,
		MaxSteps:   body.MaxSteps,
		Language:   body.Language,
		Mode:       mode,
		FileIDs:    body.FileIDs,
		SessionID:  session.ID,
	})
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to build job payload")
		return
	}

	channel := "research:" + researchID
	abortKey := "research:abort:" + researchID

	// Write the session-id event first, before Run() sets SSE headers, by
	// setting headers manually and flushing a preamble event.
	httputil.EnableSSE(w)

	sessionEvent, _ := json.Marshal(map[string]string{"type": "session", "sessionId": session.ID})
	_, _ = w.Write([]byte("data: " + string(sessionEvent) + "\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	if err := h.relay.Run(ctx, w, sserelay.Options{
		Channel:    channel,
		AbortKey:   abortKey,
		JobType:    jobs.TypeResearchExecution,
		JobPayload: payload,
	}); err != nil {
		// Headers already sent; nothing useful we can do here.
		return
	}
}

// runSimpleResearch performs a multi-step non-streaming research using the
// research agent and returns the final report as JSON.
func (h *Handler) runSimpleResearch(
	w http.ResponseWriter,
	r *http.Request,
	kbID, userID string,
	body startResearchRequest,
) {
	ctx := r.Context()

	agent := NewAgent(kbID, body.Goal, Options{
		MaxSteps: body.MaxSteps,
		Language: body.Language,
		FileIDs:  body.FileIDs,
		Mode:     "kb",
	}, h.aiResolver, h.searchSvc, h.redis, nil)

	// Run the agent synchronously: consume all events, keep the final report.
	var report string
	var findings []Finding
	var agentErr string
	for evt := range agent.Run(ctx) {
		switch evt.Type {
		case "complete":
			report = evt.Report
			findings = evt.Findings
		case "error":
			agentErr = evt.Message
			logctx.From(ctx).Error("research agent error", "kbId", kbID, "goal", body.Goal, "error", evt.Message)
		case "cancelled":
			agentErr = "research was cancelled"
			logctx.From(ctx).Info("research cancelled", "kbId", kbID, "goal", body.Goal)
		}
	}

	if report == "" {
		msg := "research failed"
		if agentErr != "" {
			msg = agentErr
		}
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, msg)
		return
	}

	// Persist the result (best-effort — report is still returned on failure).
	session, dbErr := h.store.CreateResearchSession(ctx, kbID, userID, body.Goal, "research")
	if dbErr != nil {
		logctx.From(ctx).Error("failed to create research session", "error", dbErr, "kbId", kbID)
	} else if saveErr := h.store.SaveResearchResults(ctx, session.ID, body.Goal, report, findings); saveErr != nil {
		logctx.From(ctx).Error("failed to save research results", "error", saveErr, "sessionId", session.ID, "kbId", kbID)
	}

	resp := map[string]any{
		"report": report,
	}
	if session != nil {
		resp["sessionId"] = session.ID
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// POST /api/research/web
// ---------------------------------------------------------------------------

// startWebResearchRequest is the parsed JSON body for the StartWebResearch endpoint.
type startWebResearchRequest struct {
	Topic    string `json:"topic"`
	MaxSteps int    `json:"maxSteps"`
	Language string `json:"language"`
}

// StartWebResearch handles POST /api/research/web.
// Auth is enforced by middleware. Always streaming.
func (h *Handler) StartWebResearch(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Check site config.
	enabled, err := h.store.GetSiteConfigValue(r.Context(), "web_search_enabled")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to read site config")
		return
	}
	if enabled == nil || *enabled != "true" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "web search is not enabled")
		return
	}

	var body startWebResearchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Topic == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "topic is required")
		return
	}
	body.MaxSteps = clampMaxSteps(body.MaxSteps)
	if body.Language == "" {
		body.Language = "en"
	}

	// Get kbID from path (web research is under /api/kb/{id}/web-research).
	kbID := r.PathValue("id")
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		kbID = access.KB.ID
	}

	kbBody := startResearchRequest{
		Goal:     body.Topic,
		MaxSteps: body.MaxSteps,
		Language: body.Language,
	}
	h.startStreamingResearch(w, r, kbID, user.ID, kbBody, "web")
}

// ---------------------------------------------------------------------------
// GET /api/research/{researchId}/status
// ---------------------------------------------------------------------------

// GetStatus handles GET /api/research/{researchId}/status.
// Auth is enforced by middleware.
func (h *Handler) GetStatus(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	sessionID := r.PathValue("researchId")

	row, err := h.store.GetChatByID(r.Context(), sessionID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch session")
		return
	}
	if row == nil || (row.UserID != user.ID && user.Role != "superadmin") {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "research session not found")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
		"sessionId": row.ID,
		"kbId":      row.KbID,
		"userId":    row.UserID,
		"title":     row.Title,
		"type":      row.Type,
		"createdAt": row.CreatedAt,
		"updatedAt": row.UpdatedAt,
	})
}

// ---------------------------------------------------------------------------
// POST /api/research/{researchId}/abort
// ---------------------------------------------------------------------------

// Abort handles POST /api/research/{researchId}/abort.
// Auth is enforced by middleware.
func (h *Handler) Abort(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	researchID := r.PathValue("researchId")

	// Verify the session belongs to the caller before allowing abort.
	session, err := h.store.GetChatByID(r.Context(), researchID)
	if err != nil || session == nil || (session.UserID != user.ID && user.Role != "superadmin") {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "research session not found")
		return
	}

	abortKey := "research:abort:" + researchID
	if err := h.redis.Set(r.Context(), abortKey, "1", 24*time.Hour).Err(); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to set abort signal")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]bool{"success": true})
}

// ---------------------------------------------------------------------------
// GET /api/research/deep/{deepChatId}
// ---------------------------------------------------------------------------

// DeepChatSSE handles GET /api/research/deep/{deepChatId}.
// Auth is enforced by middleware. The deep-chat worker job was already
// enqueued by the chat handler; this endpoint just subscribes to the Redis
// channel and relays progress events as SSE.
func (h *Handler) DeepChatSSE(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	deepChatID := r.PathValue("deepChatId")
	if deepChatID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "deepChatId is required")
		return
	}

	if err := h.relay.Run(r.Context(), w, sserelay.Options{
		Channel:     "deep-chat:" + deepChatID,
		AbortKey:    "deep-chat:abort:" + deepChatID,
		SkipEnqueue: true, // job already enqueued by the chat handler
	}); err != nil {
		// SSE headers may already be sent; nothing useful we can do here.
		return
	}
}

// ---------------------------------------------------------------------------
// GET /api/research/{sessionId}/report
// ---------------------------------------------------------------------------

// ExportReport handles GET /api/research/{sessionId}/report.
// Auth is enforced by middleware. Returns the research report as a downloadable file.
func (h *Handler) ExportReport(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	sessionID := r.PathValue("sessionId")

	row, err := h.store.GetChatByID(r.Context(), sessionID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch session")
		return
	}
	if row == nil || (row.UserID != user.ID && user.Role != "superadmin") {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "research session not found")
		return
	}

	// Try to load the report from: query param → request body → saved AI message in DB.
	var reportContent string
	if qr := r.URL.Query().Get("report"); qr != "" {
		reportContent = qr
	} else {
		var body struct {
			Report string `json:"report"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.Report != "" {
			reportContent = body.Report
		}
	}

	// If no report in request, load from the saved chat messages (the AI's last message).
	if reportContent == "" {
		msgs, msgErr := h.store.GetChatMessages(r.Context(), sessionID)
		if msgErr == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				if (msgs[i].Role == "ai" || msgs[i].Role == "assistant") && msgs[i].Content != "" {
					reportContent = msgs[i].Content
					break
				}
			}
		}
	}

	if reportContent == "" {
		reportContent = "# Research Report\n\n**Goal:** " + row.Title + "\n\n(No report content available.)"
	}

	docxBytes := GenerateDocx(row.Title, reportContent)

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="research_report.docx"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(docxBytes)
}
