// Package contentgen provides HTTP handlers for AI-powered content generation
// (flashcards, presentation, podcast, analysis, abstract, chart).
package contentgen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/gencontent"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/pptx"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
	"github.com/redis/go-redis/v9"
)

// Searcher is the subset of vector.SearchService used by the content generation handlers.
// Defining it here as an interface allows tests to provide a mock.
type Searcher interface {
	Search(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error)
}

// ---------------------------------------------------------------------------
// Request types
// ---------------------------------------------------------------------------

// GenerateRequest is the body for cards, presentation, podcast, and analysis.
type GenerateRequest struct {
	Topic    string `json:"topic"`
	Language string `json:"language"` // default "de"
}

// GenerateChartRequest is the body for chart generation.
type GenerateChartRequest struct {
	Topic    string `json:"topic"`
	FileID   string `json:"fileId"`
	Language string `json:"language"`
}

// GenerateAbstractRequest is the body for abstract generation.
type GenerateAbstractRequest struct {
	FileID       string `json:"fileId"`
	AbstractType string `json:"abstractType"` // "academic" or "executive"
	Language     string `json:"language"`
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Store is the persistence interface used by the content generation handlers.
type Store interface {
	CreateGeneratedContent(ctx context.Context, kbID, userID, contentType, title string, content any) (*gencontent.GenContentRow, error)
}

// AsynqEnqueuer is the subset of asynq.Client needed to enqueue tasks.
type AsynqEnqueuer interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// AsynqInspector is the subset of asynq.Inspector used to look up job state.
type AsynqInspector interface {
	GetTaskInfo(queue, id string) (*asynq.TaskInfo, error)
}

// RedisGetter is used to read podcast job progress from Redis.
// Satisfied by *redis.Client.
type RedisGetter interface {
	Get(ctx context.Context, key string) *redis.StringCmd
}

// Handler holds the dependencies for the content generation endpoints.
type Handler struct {
	store      Store
	aiResolver *ai.ConfigResolver
	searchSvc  Searcher
	asynq      AsynqEnqueuer
	inspector  AsynqInspector
	redis      RedisGetter
}

// NewHandler creates a new Handler.
// searchSvc accepts a *vector.SearchService (which satisfies Searcher) or any mock.
func NewHandler(store Store, aiResolver *ai.ConfigResolver, searchSvc Searcher, asynqClient AsynqEnqueuer, inspector AsynqInspector, redis RedisGetter) *Handler {
	return &Handler{
		store:      store,
		aiResolver: aiResolver,
		searchSvc:  searchSvc,
		asynq:      asynqClient,
		inspector:  inspector,
		redis:      redis,
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// kbIDFromContext returns the KB ID from the kbaccess context or the URL path.
func kbIDFromContext(r *http.Request) string {
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		return access.KB.ID
	}
	return r.PathValue("id")
}

// defaultLang returns lang if non-empty, otherwise "de".
func defaultLang(lang string) string {
	if lang == "" {
		return "de"
	}
	return lang
}

// ---------------------------------------------------------------------------
// GenerateCards — POST /api/kb/{id}/generate/cards
// ---------------------------------------------------------------------------

// flashcard is a single front/back learning card.
type flashcard struct {
	Front string `json:"front"`
	Back  string `json:"back"`
}

// GenerateCards creates 5–10 flashcards from KB content on the given topic.
// Runs inline (single LLM call).
func (h *Handler) GenerateCards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "topic is required")
		return
	}
	lang := defaultLang(req.Language)

	contextStr, err := getContext(ctx, h.searchSvc, kbID, req.Topic, 25)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to retrieve context")
		return
	}

	prompt := fmt.Sprintf("Topic: %s\n\nContext:\n%s\n\nCreate flashcards for the topic based on the context above.", req.Topic, contextStr)
	raw, err := generateWithLLM(ctx, h.aiResolver, prompt, prompts.FlashcardSystemPrompt(lang), kbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "LLM generation failed")
		return
	}

	// Parse JSON array from LLM response.
	raw = strings.TrimSpace(raw)
	// Strip optional markdown code fences.
	raw = stripCodeFence(raw)

	var cards []flashcard
	if err := json.Unmarshal([]byte(raw), &cards); err != nil {
		// Return raw on parse error so the client still gets something.
		cards = nil
	}

	// Content is the flashcard array directly (matching Node.js shape).
	// The frontend accesses item.content as the data payload.
	var contentVal any
	if cards != nil {
		contentVal = cards
	} else {
		contentVal = map[string]any{"raw": raw}
	}

	title := fmt.Sprintf("Flashcards: %s", req.Topic)
	record, err := h.store.CreateGeneratedContent(ctx, kbID, caller.ID, "flashcards", title, contentVal)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save generated content")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, record)
}

// ---------------------------------------------------------------------------
// GeneratePresentation — POST /api/kb/{id}/generate/presentation
// ---------------------------------------------------------------------------

// presentationData is the expected JSON structure from the LLM.
type presentationData struct {
	Title  string `json:"title"`
	Slides []struct {
		Title        string   `json:"title"`
		Content      []string `json:"content"`
		SpeakerNotes string   `json:"speakerNotes"`
	} `json:"slides"`
}

// SanitizeFilename removes characters unsafe for filenames, replacing each
// with an underscore. Exported so the worker tier reuses the same pattern
// rather than compiling a second identical regex (drift hazard).
var reUnsafeFilename = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

func SanitizeFilename(s string) string {
	return reUnsafeFilename.ReplaceAllString(s, "_")
}

// GeneratePresentation creates a PPTX file from KB content on the given topic.
// Runs inline: LLM generates slide structure, then we create an actual .pptx file.
func (h *Handler) GeneratePresentation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "topic is required")
		return
	}
	lang := defaultLang(req.Language)

	contextStr, err := getContext(ctx, h.searchSvc, kbID, req.Topic, 25)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to retrieve context")
		return
	}

	prompt := fmt.Sprintf("Topic: %s\n\nContext:\n%s\n\nCreate a presentation outline based on the context above.", req.Topic, contextStr)
	raw, err := generateWithLLM(ctx, h.aiResolver, prompt, prompts.PresentationSystemPrompt(lang), kbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "LLM generation failed")
		return
	}

	raw = strings.TrimSpace(raw)
	raw = stripCodeFence(raw)

	var pd presentationData
	if err := json.Unmarshal([]byte(raw), &pd); err != nil {
		slog.Warn("failed to parse presentation JSON, saving markdown fallback", "error", err)
		// Save as markdown fallback
		contentVal := map[string]any{"markdown": raw}
		title := fmt.Sprintf("Presentation: %s", req.Topic)
		record, storeErr := h.store.CreateGeneratedContent(ctx, kbID, caller.ID, "presentation", title, contentVal)
		if storeErr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save generated content")
			return
		}
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, record)
		return
	}

	if pd.Title == "" {
		pd.Title = req.Topic
	}

	// Build PPTX file
	pres := &pptx.Presentation{
		Title:    pd.Title,
		Subtitle: "Topic: " + req.Topic,
	}
	for _, s := range pd.Slides {
		pres.Slides = append(pres.Slides, pptx.Slide{
			Title:        s.Title,
			Points:       s.Content,
			SpeakerNotes: s.SpeakerNotes,
		})
	}

	// Write PPTX file to disk
	safeUser := SanitizeFilename(caller.Username)
	if safeUser == "" {
		safeUser = "anonymous"
	}
	safeTopic := SanitizeFilename(req.Topic)
	if len(safeTopic) > 50 {
		safeTopic = safeTopic[:50]
	}
	if safeTopic == "" {
		safeTopic = "Untitled"
	}
	fileName := fmt.Sprintf("Presentation_%s_%d.pptx", safeTopic, time.Now().UnixMilli())
	filePath := filepath.Join("data", safeUser, kbID, "generated", fileName)

	if err := pres.WriteToFile(filePath); err != nil {
		slog.Error("failed to write PPTX file", "error", err, "path", filePath)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to generate PPTX file")
		return
	}

	slideCount := len(pd.Slides)
	contentVal := map[string]any{
		"filePath": filePath,
		"summary":  fmt.Sprintf("Generated %d slides.", slideCount),
		"markdown": pres.Markdown(),
	}

	title := pd.Title
	if title == "" {
		title = fmt.Sprintf("Presentation: %s", req.Topic)
	}
	record, err := h.store.CreateGeneratedContent(ctx, kbID, caller.ID, "presentation", title, contentVal)
	if err != nil {
		// Clean up file on DB failure
		os.Remove(filePath)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save generated content")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, record)
}

// ---------------------------------------------------------------------------
// GeneratePodcast — POST /api/kb/{id}/generate/podcast
// ---------------------------------------------------------------------------

// PodcastJobPayload is the JSON payload for the podcast Asynq task.
type PodcastJobPayload struct {
	KbID     string `json:"kbId"`
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Topic    string `json:"topic"`
	Language string `json:"language"`
}

// PodcastProgressKey returns the Redis key used to store podcast job progress.
func PodcastProgressKey(jobID string) string {
	return "podcast:progress:" + jobID
}

// GeneratePodcast enqueues an async podcast generation job (script + TTS audio).
// Returns 202 with {jobId} so the frontend can poll for status.
func (h *Handler) GeneratePodcast(w http.ResponseWriter, r *http.Request) {
	kbID := kbIDFromContext(r)

	caller := auth.UserFromContext(r.Context())
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "topic is required")
		return
	}

	// Podcasts are always English: the configured TTS voices only produce
	// natural-sounding speech in English, so we hard-pin the script language
	// regardless of what the client requested or the KB's UI language.
	lang := "en"

	payload := PodcastJobPayload{
		KbID:     kbID,
		UserID:   caller.ID,
		Username: caller.Username,
		Topic:    req.Topic,
		Language: lang,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to marshal job payload")
		return
	}

	task := asynq.NewTask(jobs.TypeContentGeneration, payloadBytes)
	info, err := h.asynq.Enqueue(task, asynq.Queue(jobs.QueueHeavy), asynq.Timeout(jobs.TimeoutFor(jobs.TypeContentGeneration)))
	if err != nil {
		slog.Error("failed to enqueue podcast job", "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to enqueue podcast generation")
		return
	}

	slog.Info("podcast job enqueued", "jobId", info.ID, "kbId", kbID)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusAccepted, map[string]string{"jobId": info.ID})
}

// ---------------------------------------------------------------------------
// GetPodcastStatus — GET /api/kb/{id}/generate/podcast/status/{jobId}
// ---------------------------------------------------------------------------

// GetPodcastStatus returns the status of a podcast generation job.
// It first checks Redis for progress written by the worker (which includes
// userId for ownership verification). If Redis has no data, it falls back to
// the Asynq inspector to check the actual job state — this avoids returning
// "active" forever when the worker crashed or the Redis key expired.
func (h *Handler) GetPodcastStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobId")
	if jobID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing jobId")
		return
	}

	caller := auth.UserFromContext(r.Context())
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Try Redis first (worker writes progress here).
	key := PodcastProgressKey(jobID)
	cmd := h.redis.Get(r.Context(), key)
	if data, err := cmd.Result(); err == nil && data != "" {
		var status map[string]any
		if err := json.Unmarshal([]byte(data), &status); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to parse job status")
			return
		}
		// Verify ownership: worker stores userId in the progress data.
		if uid, ok := status["userId"].(string); ok && uid != caller.ID {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "forbidden")
			return
		}
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, status)
		return
	}

	// Fallback: query Asynq for the actual job state.
	if h.inspector != nil {
		info, err := h.inspector.GetTaskInfo(jobs.QueueHeavy, jobID)
		if err != nil {
			// Job not found in any queue — it's gone.
			httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
				"state": "failed",
				"error": "job not found",
			})
			return
		}

		// Verify ownership from the job payload.
		var payload PodcastJobPayload
		if err := json.Unmarshal(info.Payload, &payload); err == nil && payload.UserID != caller.ID {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "forbidden")
			return
		}

		switch info.State {
		case asynq.TaskStatePending, asynq.TaskStateScheduled, asynq.TaskStateRetry:
			httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
				"state":    "active",
				"progress": map[string]any{"step": "waiting", "message": "Waiting..."},
			})
		case asynq.TaskStateActive:
			httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
				"state":    "active",
				"progress": map[string]any{"step": "waiting", "message": "Processing..."},
			})
		case asynq.TaskStateCompleted:
			// Job succeeded but the Redis progress key expired. The
			// generated content is already in the DB, so report
			// completed so the frontend refreshes its content list.
			httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
				"state":    "completed",
				"progress": map[string]any{"step": "done", "message": "Done"},
			})
		default:
			// Archived, failed, or other terminal state.
			httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
				"state": "failed",
				"error": "job finished without progress data",
			})
		}
		return
	}

	// No Redis data and no inspector — report as failed so the client stops polling.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
		"state": "failed",
		"error": "job status unavailable",
	})
}

// ---------------------------------------------------------------------------
// GenerateChart — POST /api/kb/{id}/generate/chart
// ---------------------------------------------------------------------------

// GenerateChart returns 501 Not Implemented.
// Chart generation requires DuckDB which is not yet ported to the Go server.
func (h *Handler) GenerateChart(w http.ResponseWriter, r *http.Request) {
	httputil.WriteErrorCtx(r.Context(), w, http.StatusNotImplemented, "chart generation not available in Go server yet")
}

// ---------------------------------------------------------------------------
// GenerateAnalysis — POST /api/kb/{id}/generate/analysis
// ---------------------------------------------------------------------------

// GenerateAnalysis performs a structured analysis of KB content on the given topic.
// Runs inline.
func (h *Handler) GenerateAnalysis(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "topic is required")
		return
	}
	lang := defaultLang(req.Language)

	contextStr, err := getContext(ctx, h.searchSvc, kbID, req.Topic, 25)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to retrieve context")
		return
	}

	prompt := fmt.Sprintf("Analyze the following topic based on the context:\n\nTopic: %s\n\nContext:\n%s", req.Topic, contextStr)
	analysis, err := generateWithLLM(ctx, h.aiResolver, prompt, prompts.AnalysisSystemPrompt(lang), kbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "LLM generation failed")
		return
	}

	contentVal := map[string]any{"text": analysis}

	title := fmt.Sprintf("Analysis: %s", req.Topic)
	record, err := h.store.CreateGeneratedContent(ctx, kbID, caller.ID, "analysis", title, contentVal)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save generated content")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, record)
}

// ---------------------------------------------------------------------------
// GenerateAbstract — POST /api/kb/{id}/generate/abstract
// ---------------------------------------------------------------------------

// GenerateAbstract creates an academic or executive abstract for a specific file.
// Runs inline.
func (h *Handler) GenerateAbstract(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req GenerateAbstractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.FileID) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "fileId is required")
		return
	}
	if req.AbstractType != "academic" && req.AbstractType != "executive" {
		req.AbstractType = "academic"
	}
	lang := defaultLang(req.Language)

	contextStr, err := getContextForFile(ctx, h.searchSvc, kbID, req.FileID, "document summary overview", 25)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to retrieve context")
		return
	}

	prompt := fmt.Sprintf("Write an abstract for the following document content:\n\nContext:\n%s", contextStr)
	abstract, err := generateWithLLM(ctx, h.aiResolver, prompt, prompts.AbstractSystemPrompt(lang, req.AbstractType), kbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "LLM generation failed")
		return
	}

	contentVal := map[string]any{
		"text":         abstract,
		"abstractType": req.AbstractType,
		"fileId":       req.FileID,
	}

	title := fmt.Sprintf("%s Abstract", capitalize(req.AbstractType))
	record, err := h.store.CreateGeneratedContent(ctx, kbID, caller.ID, "abstract", title, contentVal)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save generated content")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, record)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// capitalize returns s with the first byte uppercased (ASCII only).
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// stripCodeFence removes optional ```json ... ``` or ``` ... ``` wrappers
// that some LLMs add around JSON responses.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	for _, fence := range []string{"```json", "```"} {
		if strings.HasPrefix(s, fence) {
			s = strings.TrimPrefix(s, fence)
			s = strings.TrimSuffix(s, "```")
			s = strings.TrimSpace(s)
			break
		}
	}
	return s
}
