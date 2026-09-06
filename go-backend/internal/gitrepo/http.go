// Package gitrepo provides HTTP handlers for Git repository source management
// within a knowledge base.
package gitrepo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/syncwindow"
)

// Handler holds the dependencies for the Git repo source HTTP endpoints.
type Handler struct {
	store        Store
	jwtSecret    string
	asynqClient  *asynq.Client
	tableDropper TableDropper
}

// NewHandler creates a Handler backed by store, using jwtSecret to encrypt
// access tokens before storage and asynqClient to enqueue sync jobs.
func NewHandler(store Store, jwtSecret string, asynqClient *asynq.Client) *Handler {
	return &Handler{store: store, jwtSecret: jwtSecret, asynqClient: asynqClient}
}

// SetTableDropper injects the spreadsheet table cleanup hook for
// DeleteSource. Optional — nil (the default) leaves materialised tables in
// place. TableDropper is defined in store_pg.go and shared with PGStore's
// own per-file delete path.
func (h *Handler) SetTableDropper(d TableDropper) { h.tableDropper = d }

// dropTablesForSource drops every file's materialised spreadsheet tables
// for the given git repo source. Nil-safe: returns immediately when no
// dropper is wired. Best effort per file: a failure is logged and the rest
// still run.
func (h *Handler) dropTablesForSource(ctx context.Context, sourceID string) {
	if h.tableDropper == nil {
		return
	}
	srcFiles, err := h.store.ListGitRepoFiles(ctx, sourceID)
	if err != nil {
		logctx.From(ctx).Warn("tabular: list files for git repo source delete failed", "sourceId", sourceID, "error", err)
		return
	}
	for _, f := range srcFiles {
		if err := h.tableDropper.DropTablesForFile(ctx, f.FileID); err != nil {
			logctx.From(ctx).Warn("tabular: drop tables for deleted git repo file failed", "fileId", f.FileID, "error", err)
		}
	}
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
	SyncSchedule        string  `json:"syncSchedule"`
	NextSyncAt          *string `json:"nextSyncAt"`
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
	var next *string
	if r.NextSyncAt != nil {
		s := r.NextSyncAt.Format("2006-01-02T15:04:05Z07:00")
		next = &s
	}
	return gitRepoSourceDTO{
		ID:                  r.ID,
		KbID:                r.KbID,
		RepoURL:             r.RepoURL,
		IsPrivate:           r.IsPrivate,
		Branch:              r.Branch,
		HasToken:            r.AccessTokenEncrypted != nil && *r.AccessTokenEncrypted != "",
		SyncSchedule:        r.SyncSchedule,
		NextSyncAt:          next,
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
	RepoURL      string `json:"repoUrl"`
	IsPrivate    bool   `json:"isPrivate"`
	AccessToken  string `json:"accessToken"`
	Branch       string `json:"branch"`
	SyncSchedule string `json:"syncSchedule"`
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

	// Validate syncSchedule (empty string means manual).
	syncSchedule := syncwindow.ScheduleManual
	if body.SyncSchedule != "" {
		syncSchedule = body.SyncSchedule
	}
	if !syncwindow.Valid(syncSchedule) {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "syncSchedule must be manual, daily or weekly")
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
		SyncSchedule:         syncSchedule,
	})
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to create git repo source")
		return
	}

	h.enqueueSync(ctx, src.ID)
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
		SyncSchedule *string `json:"syncSchedule"`
		Status       *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.SyncSchedule != nil && !syncwindow.Valid(*body.SyncSchedule) {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "syncSchedule must be manual, daily or weekly")
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

	update := GitRepoSourceUpdate{SyncSchedule: body.SyncSchedule, Status: body.Status}

	// A schedule change takes effect immediately: clearing next_sync_at makes
	// the sweeper re-stamp on its next tick, including a change back to
	// "manual" — leaving the old stamp in place would leave a source on its
	// previous cadence until the next slot fires.
	if body.SyncSchedule != nil {
		var null *time.Time
		update.NextSyncAt = &null
	}

	// Resuming a paused source must not fire an immediate daytime sync from a
	// next_sync_at stamped before the pause (possibly days or weeks stale).
	// Clearing it drops the row into ListUnscheduled, which stamps a fresh
	// slot in the next window occurrence WITHOUT enqueuing. Skip if a
	// schedule change already cleared it above. Unlike rss/confluence, git
	// repo sources have no error_message/consecutive_failures fields to
	// clear here — GitRepoSourceUpdate carries no such fields.
	if body.Status != nil && *body.Status == "active" && update.NextSyncAt == nil {
		var null *time.Time
		update.NextSyncAt = &null
	}

	if err := h.store.UpdateGitRepoSource(ctx, sourceID, update); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to update git repo source")
		return
	}

	// Return the updated row, matching rss.UpdateRSSFeed and
	// confluence.UpdateSource: the frontend's updateGitRepoSource hook
	// applies the response body as the new source state, so a bare
	// {"message":"updated"} here would overwrite the row with itself,
	// discarding every field the UI needs to keep rendering it (repoUrl,
	// status, syncSchedule, nextSyncAt, ...).
	updated, err := h.store.GetGitRepoSourceByID(ctx, sourceID)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch updated git repo source")
		return
	}
	if updated == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "git repo source not found")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, toDTO(*updated))
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

	// R60: drop this source's files' materialised spreadsheet tables
	// BEFORE the source delete. DeleteGitRepoSource relies on
	// files.git_repo_source_id ON DELETE CASCADE, which removes the files
	// rows (and their tabular_catalog rows) but never drops the physical
	// tables — deleting the source first would orphan them beyond any
	// future reach.
	h.dropTablesForSource(ctx, sourceID)

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

	h.enqueueSync(ctx, sourceID)
	httputil.WriteJSONCtx(ctx, w, http.StatusAccepted, map[string]string{"message": "Sync triggered"})
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (h *Handler) enqueueSync(ctx context.Context, sourceID string) {
	if h.asynqClient == nil {
		return
	}
	payload, err := json.Marshal(jobs.GitRepoSyncPayload{SourceID: sourceID})
	if err != nil {
		logctx.From(ctx).Error("failed to marshal git repo sync payload", "sourceId", sourceID, "error", err)
		return
	}
	if _, err := h.asynqClient.Enqueue(
		asynq.NewTask(jobs.TypeGitRepoSync, payload),
		asynq.Queue(jobs.QueueHeavy),
		asynq.MaxRetry(3),
		asynq.Timeout(jobs.TimeoutFor(jobs.TypeGitRepoSync)),
	); err != nil {
		logctx.From(ctx).Error("failed to enqueue git repo sync", "sourceId", sourceID, "error", err)
	}
}
