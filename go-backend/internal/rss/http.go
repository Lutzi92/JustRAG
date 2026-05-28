// Package rss provides HTTP handlers for RSS feed management within a knowledge base.
package rss

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mmcdole/gofeed"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// RSSFeedRow is the full RSS feed shape returned to API consumers.
type RSSFeedRow struct {
	ID                  string     `json:"id"                  db:"id"`
	KbID                string     `json:"kbId"                db:"kb_id"`
	URL                 string     `json:"url"                 db:"url"`
	Title               *string    `json:"title"               db:"title"`
	PollInterval        int        `json:"pollInterval"        db:"poll_interval"`
	Status              string     `json:"status"              db:"status"`
	ErrorMessage        *string    `json:"errorMessage"        db:"error_message"`
	ConsecutiveFailures int        `json:"consecutiveFailures" db:"consecutive_failures"`
	LastPolledAt        *time.Time `json:"lastPolledAt"        db:"last_polled_at"`
	ItemCount           int        `json:"itemCount"           db:"item_count"`
	CreatedAt           time.Time  `json:"createdAt"           db:"created_at"`
}

// RSSFeedUpdate carries optional fields for a PATCH update.
type RSSFeedUpdate struct {
	PollInterval        *int
	Status              *string
	ErrorMessage        *string
	ConsecutiveFailures *int
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// RSSStore is the persistence interface required by Handler.
type RSSStore interface {
	CreateRSSFeed(ctx context.Context, kbID, url string, title *string, pollInterval int) (*RSSFeedRow, error)
	ListRSSFeeds(ctx context.Context, kbID string) ([]RSSFeedRow, error)
	GetRSSFeedByID(ctx context.Context, feedID string) (*RSSFeedRow, error)
	UpdateRSSFeed(ctx context.Context, feedID string, updates RSSFeedUpdate) (*RSSFeedRow, error)
	DeleteRSSFeed(ctx context.Context, feedID string) error
	ListActiveRSSFeeds(ctx context.Context) ([]RSSFeedRow, error)
	UpdateRSSFeedPollSuccess(ctx context.Context, feedID string, itemCount int) error
	UpdateRSSFeedPollFailure(ctx context.Context, feedID string, errMsg string) error
	ListFileNamesByRSSFeedID(ctx context.Context, rssFeedID string) (map[string]bool, error)
}

// ---------------------------------------------------------------------------
// FeedValidator interface
// ---------------------------------------------------------------------------

// FeedValidator validates that a URL points to a real RSS/Atom feed and
// extracts its title. It is a separate interface so tests can inject a mock.
type FeedValidator interface {
	ValidateFeed(ctx context.Context, url string) (title string, err error)
}

// ---------------------------------------------------------------------------
// gofeedValidator — production implementation
// ---------------------------------------------------------------------------

// gofeedValidator implements FeedValidator using the gofeed library.
type gofeedValidator struct{}

// ValidateFeed fetches and parses the feed at url, returning its title.
func (g *gofeedValidator) ValidateFeed(ctx context.Context, url string) (string, error) {
	parser := gofeed.NewParser()
	feed, err := parser.ParseURLWithContext(url, ctx)
	if err != nil {
		return "", err
	}
	return feed.Title, nil
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// AsynqEnqueuer abstracts the Enqueue method so tests can inject a mock.
type AsynqEnqueuer interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// Handler holds the dependencies for the RSS feed endpoints.
type Handler struct {
	store       RSSStore
	validator   FeedValidator
	asynqClient AsynqEnqueuer
}

// NewHandler creates a Handler backed by store using the production gofeed validator.
func NewHandler(store RSSStore, client ...AsynqEnqueuer) *Handler {
	var c AsynqEnqueuer
	if len(client) > 0 {
		c = client[0]
	}
	return &Handler{store: store, validator: &gofeedValidator{}, asynqClient: c}
}

// NewHandlerWithValidator creates a Handler with an injectable FeedValidator.
// Primarily used in tests to avoid real network calls.
func NewHandlerWithValidator(store RSSStore, v FeedValidator) *Handler {
	return &Handler{store: store, validator: v}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// kbIDFromContext returns the KB ID, preferring the value injected by the
// kbaccess middleware and falling back to the {id} path parameter.
func kbIDFromContext(r *http.Request) string {
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		return access.KB.ID
	}
	return r.PathValue("id")
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/rss
// ---------------------------------------------------------------------------

// createRSSFeedRequest is the expected JSON body for creating a feed.
type createRSSFeedRequest struct {
	URL          string `json:"url"`
	PollInterval *int   `json:"pollInterval"`
}

// CreateRSSFeed handles POST /api/kb/{id}/rss.
// Validates the body, fetches+parses the feed to extract its title, and inserts it.
func (h *Handler) CreateRSSFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	var body createRSSFeedRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate URL.
	body.URL = strings.TrimSpace(body.URL)
	if body.URL == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "url is required")
		return
	}
	if len(body.URL) > 2048 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "url must not exceed 2048 characters")
		return
	}
	if !strings.HasPrefix(body.URL, "http://") && !strings.HasPrefix(body.URL, "https://") {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "url must start with http:// or https://")
		return
	}

	// Validate pollInterval (default 60, must be 15-1440).
	pollInterval := 60
	if body.PollInterval != nil {
		pollInterval = *body.PollInterval
	}
	if pollInterval < 15 || pollInterval > 1440 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "pollInterval must be between 15 and 1440")
		return
	}

	// Fetch and parse the feed to confirm it is a real RSS/Atom feed.
	feedTitle, err := h.validator.ValidateFeed(ctx, body.URL)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "failed to fetch or parse feed: "+err.Error())
		return
	}

	var titlePtr *string
	if feedTitle != "" {
		titlePtr = &feedTitle
	}

	feed, err := h.store.CreateRSSFeed(ctx, kbID, body.URL, titlePtr, pollInterval)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create RSS feed")
		return
	}

	// Enqueue an immediate poll so entries appear right away.
	if h.asynqClient != nil {
		payload, _ := json.Marshal(jobs.RSSPollPayload{FeedID: feed.ID})
		if _, enqErr := h.asynqClient.Enqueue(
			asynq.NewTask(jobs.TypeRSSPoll, payload),
			asynq.Queue(jobs.QueueHeavy),
			asynq.MaxRetry(3),
		); enqErr != nil {
			// Non-fatal: the scheduled poll will eventually pick it up.
			slog.Error("failed to enqueue initial RSS poll", "feedId", feed.ID, "error", enqErr)
		}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, feed)
}

// ---------------------------------------------------------------------------
// GET /api/kb/{id}/rss
// ---------------------------------------------------------------------------

// ListRSSFeeds handles GET /api/kb/{id}/rss.
// Returns all feeds for the KB ordered by created_at DESC.
func (h *Handler) ListRSSFeeds(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	feeds, err := h.store.ListRSSFeeds(ctx, kbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to list RSS feeds")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, feeds)
}

// ---------------------------------------------------------------------------
// PATCH /api/kb/{id}/rss/{feedId}
// ---------------------------------------------------------------------------

// updateRSSFeedRequest is the expected JSON body for updating a feed.
type updateRSSFeedRequest struct {
	PollInterval *int    `json:"pollInterval"`
	Status       *string `json:"status"`
}

// UpdateRSSFeed handles PATCH /api/kb/{id}/rss/{feedId}.
// Verifies the feed belongs to the KB, then updates the requested fields.
// If status is set to "active", error_message and consecutive_failures are cleared.
func (h *Handler) UpdateRSSFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	feedID := r.PathValue("feedId")

	// Verify feed belongs to KB.
	existing, err := h.store.GetRSSFeedByID(ctx, feedID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch RSS feed")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "RSS feed not found")
		return
	}
	if existing.KbID != kbID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "RSS feed not found")
		return
	}

	var body updateRSSFeedRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate pollInterval if provided.
	if body.PollInterval != nil && (*body.PollInterval < 15 || *body.PollInterval > 1440) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "pollInterval must be between 15 and 1440")
		return
	}

	// Validate status if provided.
	if body.Status != nil && *body.Status != "active" && *body.Status != "paused" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, `status must be "active" or "paused"`)
		return
	}

	updates := RSSFeedUpdate{
		PollInterval: body.PollInterval,
		Status:       body.Status,
	}

	// Clear error fields when re-activating.
	if body.Status != nil && *body.Status == "active" {
		empty := ""
		zero := 0
		updates.ErrorMessage = &empty
		updates.ConsecutiveFailures = &zero
	}

	feed, err := h.store.UpdateRSSFeed(ctx, feedID, updates)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update RSS feed")
		return
	}
	if feed == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "RSS feed not found")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, feed)
}

// ---------------------------------------------------------------------------
// DELETE /api/kb/{id}/rss/{feedId}
// ---------------------------------------------------------------------------

// DeleteRSSFeed handles DELETE /api/kb/{id}/rss/{feedId}.
// Verifies the feed belongs to the KB, then deletes it.
func (h *Handler) DeleteRSSFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	feedID := r.PathValue("feedId")

	// Verify feed belongs to KB.
	existing, err := h.store.GetRSSFeedByID(ctx, feedID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch RSS feed")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "RSS feed not found")
		return
	}
	if existing.KbID != kbID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "RSS feed not found")
		return
	}

	if err := h.store.DeleteRSSFeed(ctx, feedID); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete RSS feed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/rss/{feedId}/poll
// ---------------------------------------------------------------------------

// TriggerPoll handles POST /api/kb/{id}/rss/{feedId}/poll.
// Verifies the feed belongs to the KB, enqueues an immediate poll job, and
// returns 202 Accepted.
func (h *Handler) TriggerPoll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	feedID := r.PathValue("feedId")

	existing, err := h.store.GetRSSFeedByID(ctx, feedID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch RSS feed")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "RSS feed not found")
		return
	}
	if existing.KbID != kbID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "RSS feed not found")
		return
	}

	if h.asynqClient != nil {
		payload, _ := json.Marshal(jobs.RSSPollPayload{FeedID: feedID})
		if _, enqErr := h.asynqClient.Enqueue(
			asynq.NewTask(jobs.TypeRSSPoll, payload),
			asynq.Queue(jobs.QueueHeavy),
			asynq.MaxRetry(3),
		); enqErr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to enqueue poll job")
			return
		}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusAccepted, map[string]string{"message": "Poll triggered"})
}
