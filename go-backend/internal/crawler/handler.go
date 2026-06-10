// Package crawler implements the HTTP façade for the async BFS web crawler.
// The actual crawl work runs in a worker (see internal/worker/crawl.go) so
// that the server process is not tied up launching Chromium and waiting on
// per-page fetches. The handler here only enqueues jobs and proxies status
// lookups from Redis back to the client.
package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
)

// CrawlPage is a single crawled page. Also returned to the client in the
// status response once the crawl is complete.
type CrawlPage struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// CrawlJobPayload is the JSON payload for the crawl Asynq task. Matches the
// podcast pattern: the handler enqueues this, the worker reads it back.
type CrawlJobPayload struct {
	KbID     string `json:"kbId"`
	UserID   string `json:"userId"`
	SeedURL  string `json:"seedUrl"`
	MaxPages int    `json:"maxPages"`
}

// CrawlProgressKey returns the Redis key used to store crawl job progress.
// Kept in the package (not the worker) so both the worker producer and the
// HTTP consumer agree on the key format without a cross-package dependency.
func CrawlProgressKey(jobID string) string {
	return "crawl:progress:" + jobID
}

// Handler owns the crawl HTTP endpoints. It holds an Asynq client for
// enqueuing jobs and a Redis client for reading worker-written progress.
type Handler struct {
	asynq     *asynq.Client
	inspector *asynq.Inspector
	redis     *redis.Client
}

// NewHandler constructs a Handler. asynqClient enqueues crawl jobs,
// inspector provides a fallback view into Asynq when Redis progress has
// expired, redisClient reads worker-written progress.
func NewHandler(asynqClient *asynq.Client, inspector *asynq.Inspector, redisClient *redis.Client) *Handler {
	return &Handler{
		asynq:     asynqClient,
		inspector: inspector,
		redis:     redisClient,
	}
}

// crawlRequest is the parsed JSON body for POST /api/kb/{id}/crawl.
type crawlRequest struct {
	URL      string `json:"url"`
	MaxPages int    `json:"maxPages"`
}

// Crawl handles POST /api/kb/{id}/crawl. It validates the seed URL,
// enqueues a TypeCrawl job to rag-quick, and returns 202 with the job ID.
// The client then polls GetCrawlStatus until the job reaches a terminal
// state. KB edit permission is enforced by middleware.
func (h *Handler) Crawl(w http.ResponseWriter, r *http.Request) {
	kbID := r.PathValue("id")
	if kbID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing kb id")
		return
	}

	caller := auth.UserFromContext(r.Context())
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body crawlRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.URL == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "url is required")
		return
	}
	if _, err := url.ParseRequestURI(body.URL); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "url is invalid")
		return
	}

	// Clamp to the same bounds the old synchronous handler used.
	if body.MaxPages <= 0 {
		body.MaxPages = 5
	}
	if body.MaxPages > 50 {
		body.MaxPages = 50
	}

	payload := CrawlJobPayload{
		KbID:     kbID,
		UserID:   caller.ID,
		SeedURL:  body.URL,
		MaxPages: body.MaxPages,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to marshal job payload")
		return
	}

	if h.asynq == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, "crawl worker not configured")
		return
	}

	task := asynq.NewTask(jobs.TypeCrawl, payloadBytes)
	info, err := h.asynq.Enqueue(task, asynq.Queue(jobs.QueueQuick), asynq.Timeout(jobs.TimeoutFor(jobs.TypeCrawl)))
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to enqueue crawl job")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusAccepted, map[string]string{"jobId": info.ID})
}

// GetCrawlStatus handles GET /api/kb/{id}/crawl/status/{jobId}.
// Mirrors the podcast status endpoint: reads progress from Redis first,
// falls back to the Asynq inspector so we don't report "active" forever
// when the worker crashed or the Redis key expired.
func (h *Handler) GetCrawlStatus(w http.ResponseWriter, r *http.Request) {
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

	// Primary: Redis progress written by the worker.
	if status, ok := h.readRedisProgress(r.Context(), jobID, caller.ID); ok {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, status)
		return
	}

	// Fallback: query Asynq for the actual job state.
	if h.inspector != nil {
		info, err := h.inspector.GetTaskInfo(jobs.QueueQuick, jobID)
		if err != nil {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
				"state": "failed",
				"error": "job not found",
			})
			return
		}

		// Verify ownership from the job payload.
		var payload CrawlJobPayload
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
				"progress": map[string]any{"step": "crawling", "message": "Crawling..."},
			})
		case asynq.TaskStateCompleted:
			httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
				"state": "completed",
				"pages": []CrawlPage{},
			})
		default:
			httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
				"state": "failed",
				"error": fmt.Sprintf("task state: %s", info.State),
			})
		}
		return
	}

	httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "job not found")
}

// readRedisProgress fetches and validates the progress blob from Redis.
// Returns (status, true) on success, (nil, false) when the key doesn't
// exist or ownership verification fails. Ownership mismatch writes a 403
// directly — callers should only treat `true` as "status is ready".
func (h *Handler) readRedisProgress(ctx context.Context, jobID, userID string) (map[string]any, bool) {
	if h.redis == nil {
		return nil, false
	}
	data, err := h.redis.Get(ctx, CrawlProgressKey(jobID)).Result()
	if err != nil || data == "" {
		return nil, false
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(data), &status); err != nil {
		return nil, false
	}
	if uid, ok := status["userId"].(string); ok && uid != userID {
		// Ownership mismatch — return not-found so we don't leak job existence.
		return nil, false
	}
	return status, true
}
