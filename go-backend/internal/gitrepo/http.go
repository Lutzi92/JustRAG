// Package gitrepo provides HTTP handlers for Git repository source management
// within a knowledge base.
package gitrepo

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// Handler holds the dependencies for the Git repo source HTTP endpoints.
type Handler struct {
	store       Store
	jwtSecret   string
	asynqClient *asynq.Client
}

// NewHandler creates a Handler backed by store, using jwtSecret to encrypt
// access tokens before storage and asynqClient to enqueue sync jobs.
func NewHandler(store Store, jwtSecret string, asynqClient *asynq.Client) *Handler {
	return &Handler{store: store, jwtSecret: jwtSecret, asynqClient: asynqClient}
}

// kbIDFromContext returns the KB ID from the kbaccess middleware context or
// falls back to the {id} path parameter.
func kbIDFromContext(r *http.Request) string {
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		return access.KB.ID
	}
	return r.PathValue("id")
}

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

type gitRepoSourceDTO struct {
	ID                  string  `json:"id"`
	KbID                string  `json:"kbId"`
	RepoURL             string  `json:"repoUrl"`
	IsPrivate           bool    `json:"isPrivate"`
	Branch              *string `json:"branch"`
	HasToken            bool    `json:"hasToken"`
	Status              string  `json:"status"`
	ErrorMessage        *string `json:"errorMessage"`
	ConsecutiveFailures int     `json:"consecutiveFailures"`
	LastSyncedAt        *string `json:"lastSyncedAt"`
	LastCommitSHA       *string `json:"lastCommitSha"`
	FileCount           int     `json:"fileCount"`
	SyncProgress        int     `json:"syncProgress"`
	SyncTotal           int     `json:"syncTotal"`
	CreatedAt           string  `json:"createdAt"`
}

func toDTO(r GitRepoSourceRow) gitRepoSourceDTO {
	var last *string
	if r.LastSyncedAt != nil {
		s := r.LastSyncedAt.Format("2006-01-02T15:04:05Z07:00")
		last = &s
	}
	return gitRepoSourceDTO{
		ID:                  r.ID,
		KbID:                r.KbID,
		RepoURL:             r.RepoURL,
		IsPrivate:           r.IsPrivate,
		Branch:              r.Branch,
		HasToken:            r.AccessTokenEncrypted != nil && *r.AccessTokenEncrypted != "",
		Status:              r.Status,
		ErrorMessage:        r.ErrorMessage,
		ConsecutiveFailures: r.ConsecutiveFailures,
		LastSyncedAt:        last,
		LastCommitSHA:       r.LastCommitSHA,
		FileCount:           r.FileCount,
		SyncProgress:        r.SyncProgress,
		SyncTotal:           r.SyncTotal,
		CreatedAt:           r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ---------------------------------------------------------------------------
// Request types
// ---------------------------------------------------------------------------

type createSourceRequest struct {
	RepoURL     string `json:"repoUrl"`
	IsPrivate   bool   `json:"isPrivate"`
	AccessToken string `json:"accessToken"`
	Branch      string `json:"branch"`
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/git-repos
// ---------------------------------------------------------------------------

// CreateSource handles POST /api/kb/{id}/git-repos.
func (h *Handler) CreateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	enabledVal, err := h.store.GetSiteConfigValue(ctx, "git_repo_enabled")
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch site config")
		return
	}
	if enabledVal == nil || *enabledVal != "true" {
		httputil.WriteErrorCtx(ctx, w, http.StatusForbidden, "git repository sources are not enabled")
		return
	}

	var body createSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}

	u, err := url.Parse(body.RepoURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "repoUrl must be an https URL")
		return
	}

	var encTok *string
	if body.IsPrivate {
		if body.AccessToken == "" {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "accessToken is required for private repositories")
			return
		}
		enc, encErr := confluence.EncryptToken(body.AccessToken, h.jwtSecret)
		if encErr != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to secure access token")
			return
		}
		encTok = &enc
	}

	var branch *string
	if body.Branch != "" {
		branch = &body.Branch
	}

	src, err := h.store.CreateGitRepoSource(ctx, CreateGitRepoSourceInput{
		KbID:                 kbID,
		RepoURL:              body.RepoURL,
		IsPrivate:            body.IsPrivate,
		AccessTokenEncrypted: encTok,
		Branch:               branch,
	})
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to create git repo source")
		return
	}

	h.enqueueSync(src.ID)
	httputil.WriteJSONCtx(ctx, w, http.StatusCreated, toDTO(*src))
}

// ---------------------------------------------------------------------------
// GET /api/kb/{id}/git-repos
// ---------------------------------------------------------------------------

// ListSources handles GET /api/kb/{id}/git-repos.
func (h *Handler) ListSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.store.ListGitRepoSources(ctx, kbIDFromContext(r))
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to list git repo sources")
		return
	}
	out := make([]gitRepoSourceDTO, len(rows))
	for i, row := range rows {
		out[i] = toDTO(row)
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// PATCH /api/kb/{id}/git-repos/{sourceId}
// ---------------------------------------------------------------------------

// UpdateSource handles PATCH /api/kb/{id}/git-repos/{sourceId}.
func (h *Handler) UpdateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	sourceID := r.PathValue("sourceId")

	var body struct {
		Status *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Status != nil && *body.Status != "active" && *body.Status != "paused" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, `status must be "active" or "paused"`)
		return
	}

	existing, err := h.store.GetGitRepoSourceByID(ctx, sourceID)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch git repo source")
		return
	}
	if existing == nil || existing.KbID != kbID {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "git repo source not found")
		return
	}

	if err := h.store.UpdateGitRepoSource(ctx, sourceID, GitRepoSourceUpdate{Status: body.Status}); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to update git repo source")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]string{"message": "updated"})
}

// ---------------------------------------------------------------------------
// DELETE /api/kb/{id}/git-repos/{sourceId}
// ---------------------------------------------------------------------------

// DeleteSource handles DELETE /api/kb/{id}/git-repos/{sourceId}.
func (h *Handler) DeleteSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	sourceID := r.PathValue("sourceId")

	existing, err := h.store.GetGitRepoSourceByID(ctx, sourceID)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch git repo source")
		return
	}
	if existing == nil || existing.KbID != kbID {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "git repo source not found")
		return
	}

	if err := h.store.DeleteGitRepoSource(ctx, sourceID); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to delete git repo source")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]string{"message": "deleted"})
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/git-repos/{sourceId}/sync
// ---------------------------------------------------------------------------

// TriggerSync handles POST /api/kb/{id}/git-repos/{sourceId}/sync.
// Verifies the source belongs to the KB before enqueuing the sync job.
func (h *Handler) TriggerSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	sourceID := r.PathValue("sourceId")

	enabledVal, err := h.store.GetSiteConfigValue(ctx, "git_repo_enabled")
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch site config")
		return
	}
	if enabledVal == nil || *enabledVal != "true" {
		httputil.WriteErrorCtx(ctx, w, http.StatusForbidden, "git repository sources are not enabled")
		return
	}

	existing, err := h.store.GetGitRepoSourceByID(ctx, sourceID)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch git repo source")
		return
	}
	if existing == nil || existing.KbID != kbID {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "git repo source not found")
		return
	}

	h.enqueueSync(sourceID)
	httputil.WriteJSONCtx(ctx, w, http.StatusAccepted, map[string]string{"message": "Sync triggered"})
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (h *Handler) enqueueSync(sourceID string) {
	if h.asynqClient == nil {
		return
	}
	payload, err := json.Marshal(jobs.GitRepoSyncPayload{SourceID: sourceID})
	if err != nil {
		slog.Error("failed to marshal git repo sync payload", "sourceId", sourceID, "error", err)
		return
	}
	if _, err := h.asynqClient.Enqueue(
		asynq.NewTask(jobs.TypeGitRepoSync, payload),
		asynq.Queue(jobs.QueueHeavy),
		asynq.MaxRetry(3),
		asynq.Timeout(jobs.TimeoutFor(jobs.TypeGitRepoSync)),
	); err != nil {
		slog.Error("failed to enqueue git repo sync", "sourceId", sourceID, "error", err)
	}
}
