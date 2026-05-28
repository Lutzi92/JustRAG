// Package confluence provides HTTP handlers for Confluence connection and
// source management within a knowledge base.
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// ConfluenceConnectionRow is the full connection shape stored in the database.
type ConfluenceConnectionRow struct {
	ID             string     `json:"id"             db:"id"`
	UserID         string     `json:"-"              db:"user_id"`
	DisplayName    *string    `json:"displayName"    db:"display_name"`
	Token          string     `json:"-"              db:"token"` // encrypted, never exposed
	Status         string     `json:"status"         db:"status"`
	ErrorMessage   *string    `json:"errorMessage"   db:"error_message"`
	LastVerifiedAt *time.Time `json:"lastVerifiedAt" db:"last_verified_at"`
	CreatedAt      time.Time  `json:"createdAt"      db:"created_at"`
}

// ConfluenceSourceRow is the full source shape returned to API consumers.
type ConfluenceSourceRow struct {
	ID                  string     `json:"id"                  db:"id"`
	KbID                string     `json:"kbId"                db:"kb_id"`
	ConnectionID        string     `json:"connectionId"        db:"connection_id"`
	SpaceKey            string     `json:"spaceKey"            db:"space_key"`
	RootPageID          *string    `json:"rootPageId"          db:"root_page_id"`
	RootPageTitle       *string    `json:"rootPageTitle"       db:"root_page_title"`
	IncludeAttachments  bool       `json:"includeAttachments"  db:"include_attachments"`
	SyncInterval        *int       `json:"syncInterval"        db:"sync_interval"`
	Status              string     `json:"status"              db:"status"`
	ErrorMessage        *string    `json:"errorMessage"        db:"error_message"`
	ConsecutiveFailures int        `json:"consecutiveFailures" db:"consecutive_failures"`
	LastSyncedAt        *time.Time `json:"lastSyncedAt"        db:"last_synced_at"`
	PageCount           int        `json:"pageCount"           db:"page_count"`
	SyncProgress        int        `json:"syncProgress"        db:"sync_progress"`
	SyncTotal           int        `json:"syncTotal"           db:"sync_total"`
	CreatedAt           time.Time  `json:"createdAt"           db:"created_at"`
}

// ConfluenceSourceUpdate carries optional fields for a PATCH update.
type ConfluenceSourceUpdate struct {
	SpaceKey            *string
	RootPageID          *string
	RootPageTitle       *string
	IncludeAttachments  *bool
	SyncInterval        *int
	Status              *string
	ErrorMessage        *string
	ConsecutiveFailures *int
	PageCount           *int
	SyncProgress        *int
	SyncTotal           *int
	LastSyncedAt        *time.Time
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// ConfluenceConnectionUpdate carries optional fields for updating a connection.
// It mirrors dbstore.ConfluenceConnectionUpdate to avoid an import cycle.
type ConfluenceConnectionUpdate struct {
	Token          *string
	DisplayName    *string
	Status         *string
	ErrorMessage   *string
	LastVerifiedAt *time.Time
}

// ConfluenceStore is the persistence interface required by Handler.
type ConfluenceStore interface {
	// Connections
	GetConfluenceConnectionByUserID(ctx context.Context, userID string) (*ConfluenceConnectionRow, error)
	GetConfluenceConnectionByID(ctx context.Context, id string) (*ConfluenceConnectionRow, error)
	DeleteConfluenceConnection(ctx context.Context, id string) error
	CreateConfluenceConnection(ctx context.Context, userID, encryptedToken string, displayName *string) (*ConfluenceConnectionRow, error)
	UpdateConfluenceConnection(ctx context.Context, id string, updates ConfluenceConnectionUpdate) (*ConfluenceConnectionRow, error)
	// Sources
	CreateConfluenceSource(ctx context.Context, kbID, connectionID, spaceKey string, rootPageID, rootPageTitle *string, includeAttachments bool, syncInterval *int) (*ConfluenceSourceRow, error)
	ListConfluenceSources(ctx context.Context, kbID string) ([]ConfluenceSourceRow, error)
	GetConfluenceSourceByID(ctx context.Context, sourceID string) (*ConfluenceSourceRow, error)
	UpdateConfluenceSource(ctx context.Context, sourceID string, updates ConfluenceSourceUpdate) (*ConfluenceSourceRow, error)
	DeleteConfluenceSource(ctx context.Context, sourceID string) error
	ListActiveConfluenceSources(ctx context.Context) ([]ConfluenceSourceRow, error)
	// Files
	CreateConfluenceFile(ctx context.Context, data CreateConfluenceFileData) (*ConfluenceFileRow, error)
	GetFilesByConfluenceSourceID(ctx context.Context, sourceID string) ([]ConfluenceFileRow, error)
	GetConfluenceSourceIDForFile(ctx context.Context, fileID string) (string, error)
	DeleteFilesByIDs(ctx context.Context, ids []string) error
	GetConfluenceSourceFileProgress(ctx context.Context, sourceID string) (total, done int, err error)
	// Site config
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// CreateConfluenceFileData holds the parameters for creating a confluence-linked file.
type CreateConfluenceFileData struct {
	KbID               string
	Name               string
	Type               string // mime type
	Size               int
	Origin             string // "confluence"
	StoragePath        string
	ConfluenceSourceID string
	ConfluencePageID   string
}

// ConfluenceFileRow is a file record with confluence-specific fields.
type ConfluenceFileRow struct {
	ID                 string    `json:"id"                 db:"id"`
	KbID               string    `json:"kbId"               db:"kb_id"`
	Name               string    `json:"name"               db:"name"`
	Type               string    `json:"type"               db:"type"`
	Size               *int      `json:"size"               db:"size"`
	Status             string    `json:"status"             db:"status"`
	Origin             string    `json:"origin"             db:"origin"`
	StoragePath        *string   `json:"-"                  db:"storage_path"`
	ConfluenceSourceID *string   `json:"confluenceSourceId" db:"confluence_source_id"`
	ConfluencePageID   *string   `json:"confluencePageId"   db:"confluence_page_id"`
	CreatedAt          time.Time `json:"createdAt"          db:"created_at"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the Confluence endpoints.
type Handler struct {
	store       ConfluenceStore
	jwtSecret   string
	asynqClient *asynq.Client
}

// NewHandler creates a Handler backed by store, using jwtSecret for token
// decryption and masking in the GET connection endpoint.
func NewHandler(store ConfluenceStore, jwtSecret string, asynqClient ...*asynq.Client) *Handler {
	h := &Handler{store: store, jwtSecret: jwtSecret}
	if len(asynqClient) > 0 {
		h.asynqClient = asynqClient[0]
	}
	return h
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// kbIDFromContext returns the KB ID from the kbaccess middleware context or
// falls back to the {id} path parameter.
func kbIDFromContext(r *http.Request) string {
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		return access.KB.ID
	}
	return r.PathValue("id")
}

// ---------------------------------------------------------------------------
// connectionResponse is the shape returned for GET /api/confluence/connections.
// ---------------------------------------------------------------------------

type connectionResponse struct {
	ID             string     `json:"id"`
	DisplayName    *string    `json:"displayName"`
	Token          string     `json:"token"` // masked
	Status         string     `json:"status"`
	ErrorMessage   *string    `json:"errorMessage"`
	LastVerifiedAt *time.Time `json:"lastVerifiedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
}

type getConnectionsResponse struct {
	Connection *connectionResponse `json:"connection"`
	BaseURL    *string             `json:"baseUrl"`
	Enabled    bool                `json:"enabled"`
}

// ---------------------------------------------------------------------------
// GET /api/confluence/connections
// ---------------------------------------------------------------------------

// GetConnection handles GET /api/confluence/connections.
// Returns the current user's connection with a masked token plus site config status.
func (h *Handler) GetConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Fetch site config values.
	enabledVal, err := h.store.GetSiteConfigValue(ctx, "confluence_enabled")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch site config")
		return
	}
	enabled := enabledVal != nil && *enabledVal == "true"

	baseURL, err := h.store.GetSiteConfigValue(ctx, "confluence_base_url")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch site config")
		return
	}

	// Fetch connection.
	conn, err := h.store.GetConfluenceConnectionByUserID(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch connection")
		return
	}

	if conn == nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, getConnectionsResponse{
			Connection: nil,
			BaseURL:    baseURL,
			Enabled:    enabled,
		})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, getConnectionsResponse{
		Connection: h.connectionToResponse(conn),
		BaseURL:    baseURL,
		Enabled:    enabled,
	})
}

// ---------------------------------------------------------------------------
// DELETE /api/confluence/connections/{id}
// ---------------------------------------------------------------------------

// DeleteConnection handles DELETE /api/confluence/connections/{id}.
// Only the owning user may delete their connection.
func (h *Handler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	connectionID := r.PathValue("id")
	if connectionID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "connection id is required")
		return
	}

	// Verify ownership: the connection must belong to this user.
	conn, err := h.store.GetConfluenceConnectionByUserID(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch connection")
		return
	}
	if conn == nil || conn.ID != connectionID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "connection not found")
		return
	}

	if err := h.store.DeleteConfluenceConnection(ctx, connectionID); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete connection")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/confluence-sources
// ---------------------------------------------------------------------------

// createSourceRequest is the expected JSON body for creating a Confluence source.
type createSourceRequest struct {
	ConnectionID       string  `json:"connectionId"`
	SpaceKey           string  `json:"spaceKey"`
	RootPageID         *string `json:"rootPageId"`
	RootPageTitle      *string `json:"rootPageTitle"`
	IncludeAttachments bool    `json:"includeAttachments"`
	SyncInterval       *int    `json:"syncInterval"`
}

// CreateSource handles POST /api/kb/{id}/confluence-sources.
func (h *Handler) CreateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	var body createSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.ConnectionID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "connectionId is required")
		return
	}
	if body.SpaceKey == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "spaceKey is required")
		return
	}

	// Verify the connection belongs to the authenticated user.
	user := auth.UserFromContext(ctx)
	if user != nil {
		conn, connErr := h.store.GetConfluenceConnectionByUserID(ctx, user.ID)
		if connErr != nil || conn == nil || conn.ID != body.ConnectionID {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "connection does not belong to you")
			return
		}
	}

	source, err := h.store.CreateConfluenceSource(ctx, kbID, body.ConnectionID, body.SpaceKey,
		body.RootPageID, body.RootPageTitle, body.IncludeAttachments, body.SyncInterval)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create Confluence source")
		return
	}

	// Enqueue initial sync job so the worker fetches pages immediately.
	if h.asynqClient != nil {
		payload, marshalErr := json.Marshal(map[string]string{"sourceId": source.ID})
		if marshalErr == nil {
			if _, enqErr := h.asynqClient.Enqueue(
				asynq.NewTask(jobs.TypeConfluenceSync, payload),
				asynq.Queue(jobs.QueueHeavy),
				asynq.MaxRetry(3),
			); enqErr != nil {
				slog.Error("failed to enqueue initial confluence sync", "sourceId", source.ID, "error", enqErr)
			}
		}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, source)
}

// ---------------------------------------------------------------------------
// GET /api/kb/{id}/confluence-sources
// ---------------------------------------------------------------------------

// ListSources handles GET /api/kb/{id}/confluence-sources.
func (h *Handler) ListSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	sources, err := h.store.ListConfluenceSources(ctx, kbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to list Confluence sources")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, sources)
}

// ---------------------------------------------------------------------------
// PATCH /api/kb/{id}/confluence-sources/{sourceId}
// ---------------------------------------------------------------------------

// updateSourceRequest is the expected JSON body for updating a Confluence source.
type updateSourceRequest struct {
	SpaceKey           *string `json:"spaceKey"`
	RootPageID         *string `json:"rootPageId"`
	RootPageTitle      *string `json:"rootPageTitle"`
	IncludeAttachments *bool   `json:"includeAttachments"`
	SyncInterval       *int    `json:"syncInterval"`
	Status             *string `json:"status"`
}

// UpdateSource handles PATCH /api/kb/{id}/confluence-sources/{sourceId}.
func (h *Handler) UpdateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	sourceID := r.PathValue("sourceId")

	// Verify source belongs to this KB.
	existing, err := h.store.GetConfluenceSourceByID(ctx, sourceID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch Confluence source")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Confluence source not found")
		return
	}
	if existing.KbID != kbID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Confluence source not found")
		return
	}

	var body updateSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate status if provided.
	if body.Status != nil && *body.Status != "active" && *body.Status != "paused" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, `status must be "active" or "paused"`)
		return
	}

	updates := ConfluenceSourceUpdate{
		SpaceKey:           body.SpaceKey,
		RootPageID:         body.RootPageID,
		RootPageTitle:      body.RootPageTitle,
		IncludeAttachments: body.IncludeAttachments,
		SyncInterval:       body.SyncInterval,
		Status:             body.Status,
	}

	// Clear error state when re-activating.
	if body.Status != nil && *body.Status == "active" {
		empty := ""
		zero := 0
		updates.ErrorMessage = &empty
		updates.ConsecutiveFailures = &zero
	}

	source, err := h.store.UpdateConfluenceSource(ctx, sourceID, updates)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update Confluence source")
		return
	}
	if source == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Confluence source not found")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, source)
}

// ---------------------------------------------------------------------------
// DELETE /api/kb/{id}/confluence-sources/{sourceId}
// ---------------------------------------------------------------------------

// DeleteSource handles DELETE /api/kb/{id}/confluence-sources/{sourceId}.
func (h *Handler) DeleteSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	sourceID := r.PathValue("sourceId")

	// Verify source belongs to this KB.
	existing, err := h.store.GetConfluenceSourceByID(ctx, sourceID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch Confluence source")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Confluence source not found")
		return
	}
	if existing.KbID != kbID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Confluence source not found")
		return
	}

	if err := h.store.DeleteConfluenceSource(ctx, sourceID); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete Confluence source")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/confluence-sources/{sourceId}/sync
// ---------------------------------------------------------------------------

// TriggerSync handles POST /api/kb/{id}/confluence-sources/{sourceId}/sync.
// Verifies the source belongs to the KB, then enqueues a confluence-sync Asynq job.
func (h *Handler) TriggerSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)
	sourceID := r.PathValue("sourceId")

	// Verify source belongs to this KB.
	existing, err := h.store.GetConfluenceSourceByID(ctx, sourceID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch Confluence source")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Confluence source not found")
		return
	}
	if existing.KbID != kbID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Confluence source not found")
		return
	}

	// Enqueue Asynq confluence-sync job.
	if h.asynqClient != nil {
		payload, marshalErr := json.Marshal(map[string]string{"sourceId": sourceID})
		if marshalErr == nil {
			if _, enqErr := h.asynqClient.Enqueue(
				asynq.NewTask(jobs.TypeConfluenceSync, payload),
				asynq.Queue(jobs.QueueHeavy),
				asynq.MaxRetry(3),
			); enqErr != nil {
				slog.Error("failed to enqueue confluence sync", "sourceId", sourceID, "error", enqErr)
				httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to trigger sync")
				return
			}
		}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusAccepted, map[string]string{"message": "Sync triggered"})
}

// ---------------------------------------------------------------------------
// connectionToResponse converts a ConfluenceConnectionRow to the API response
// shape, decrypting and masking the token.
// ---------------------------------------------------------------------------

func (h *Handler) connectionToResponse(conn *ConfluenceConnectionRow) *connectionResponse {
	status := conn.Status
	errMsg := conn.ErrorMessage

	plaintext, decErr := DecryptToken(conn.Token, h.jwtSecret)
	var maskedToken string
	if decErr != nil {
		status = "error"
		decErrStr := "token decryption failed"
		errMsg = &decErrStr
		maskedToken = "••••••••"
	} else {
		maskedToken = MaskToken(plaintext)
	}

	return &connectionResponse{
		ID:             conn.ID,
		DisplayName:    conn.DisplayName,
		Token:          maskedToken,
		Status:         status,
		ErrorMessage:   errMsg,
		LastVerifiedAt: conn.LastVerifiedAt,
		CreatedAt:      conn.CreatedAt,
	}
}

// markConnectionAuthFailure flips the stored connection's status to 'error'
// after a Confluence call rejected the token. Best-effort: any DB error is
// logged but not propagated — the caller has already decided to fail the
// request, and a stale 'active' status will be re-corrected on the next call.
//
// This is what closes the loop where Profile keeps showing a green checkmark
// while every modal call fails: as soon as any list endpoint sees a 401/403,
// the stored status flips and the Profile reflects reality on next fetch.
func (h *Handler) markConnectionAuthFailure(ctx context.Context, userID string, callErr error) {
	conn, err := h.store.GetConfluenceConnectionByUserID(ctx, userID)
	if err != nil || conn == nil {
		return
	}
	status := "error"
	msg := callErr.Error()
	if _, err := h.store.UpdateConfluenceConnection(ctx, conn.ID, ConfluenceConnectionUpdate{
		Status:       &status,
		ErrorMessage: &msg,
	}); err != nil {
		slog.Warn("failed to mark confluence connection as error after auth failure",
			"connectionId", conn.ID, "error", err)
	}
}

// writeConfluenceCallError translates a Confluence client error into an HTTP
// response. Auth failures (401/403 from Confluence) flip the stored connection
// status to 'error' and return 401 to the caller so the frontend can switch to
// a "reconnect" UI; other errors propagate as 502 Bad Gateway.
func (h *Handler) writeConfluenceCallError(ctx context.Context, w http.ResponseWriter, userID, action string, err error) {
	if errors.Is(err, ErrConfluenceAuthFailed) {
		h.markConnectionAuthFailure(ctx, userID, err)
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "Confluence rejected the token: "+err.Error())
		return
	}
	httputil.WriteErrorCtx(ctx, w, http.StatusBadGateway, action+": "+err.Error())
}

// confluenceClientForUser retrieves the user's connection, decrypts its token,
// fetches the Confluence base URL from site config, and returns a ready client.
func (h *Handler) confluenceClientForUser(ctx context.Context, userID string) (*ConfluenceClient, error) {
	conn, err := h.store.GetConfluenceConnectionByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}
	if conn == nil {
		return nil, fmt.Errorf("no connection")
	}

	plaintext, err := DecryptToken(conn.Token, h.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt token: %w", err)
	}

	baseURL, err := h.store.GetSiteConfigValue(ctx, "confluence_base_url")
	if err != nil || baseURL == nil || *baseURL == "" {
		return nil, fmt.Errorf("confluence_base_url not configured")
	}

	return NewConfluenceClient(*baseURL, plaintext), nil
}

// ---------------------------------------------------------------------------
// POST /api/confluence/connections
// ---------------------------------------------------------------------------

type createConnectionRequest struct {
	Token       string  `json:"token"`
	DisplayName *string `json:"displayName"`
}

// CreateConnection handles POST /api/confluence/connections.
func (h *Handler) CreateConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Check Confluence is enabled.
	enabledVal, err := h.store.GetSiteConfigValue(ctx, "confluence_enabled")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch site config")
		return
	}
	if enabledVal == nil || *enabledVal != "true" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Confluence integration is not enabled")
		return
	}

	baseURL, err := h.store.GetSiteConfigValue(ctx, "confluence_base_url")
	if err != nil || baseURL == nil || *baseURL == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Confluence base URL is not configured")
		return
	}

	var body createConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Token == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "token is required")
		return
	}

	// Verify token against the Confluence API.
	client := NewConfluenceClient(*baseURL, body.Token)
	if err := client.VerifyConnection(ctx); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "token verification failed: "+err.Error())
		return
	}

	// Reject if a connection already exists for this user.
	existing, err := h.store.GetConfluenceConnectionByUserID(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to check existing connection")
		return
	}
	if existing != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "a Confluence connection already exists for this user")
		return
	}

	// Encrypt the token before persisting.
	encrypted, err := EncryptToken(body.Token, h.jwtSecret)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to encrypt token")
		return
	}

	conn, err := h.store.CreateConfluenceConnection(ctx, user.ID, encrypted, body.DisplayName)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create connection")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, h.connectionToResponse(conn))
}

// ---------------------------------------------------------------------------
// PUT /api/confluence/connections/{id}
// ---------------------------------------------------------------------------

type updateConnectionRequest struct {
	Token       *string `json:"token"`
	DisplayName *string `json:"displayName"`
}

// UpdateConnection handles PUT /api/confluence/connections/{id}.
func (h *Handler) UpdateConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	connectionID := r.PathValue("id")
	if connectionID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "connection id is required")
		return
	}

	// Verify ownership.
	existing, err := h.store.GetConfluenceConnectionByUserID(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch connection")
		return
	}
	if existing == nil || existing.ID != connectionID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "connection not found")
		return
	}

	var body updateConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := ConfluenceConnectionUpdate{
		DisplayName: body.DisplayName,
	}

	// If a new token is provided, verify and encrypt it.
	if body.Token != nil && *body.Token != "" {
		baseURL, err := h.store.GetSiteConfigValue(ctx, "confluence_base_url")
		if err != nil || baseURL == nil || *baseURL == "" {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Confluence base URL is not configured")
			return
		}

		client := NewConfluenceClient(*baseURL, *body.Token)
		if err := client.VerifyConnection(ctx); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "token verification failed: "+err.Error())
			return
		}

		encrypted, err := EncryptToken(*body.Token, h.jwtSecret)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to encrypt token")
			return
		}
		updates.Token = &encrypted
	}

	conn, err := h.store.UpdateConfluenceConnection(ctx, connectionID, updates)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update connection")
		return
	}
	if conn == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "connection not found")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, h.connectionToResponse(conn))
}

// ---------------------------------------------------------------------------
// POST /api/confluence/connections/{id}/verify
// ---------------------------------------------------------------------------

// VerifyConnection handles POST /api/confluence/connections/{id}/verify.
func (h *Handler) VerifyConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	connectionID := r.PathValue("id")
	if connectionID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "connection id is required")
		return
	}

	// Verify ownership.
	conn, err := h.store.GetConfluenceConnectionByUserID(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch connection")
		return
	}
	if conn == nil || conn.ID != connectionID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "connection not found")
		return
	}

	// Decrypt token.
	plaintext, err := DecryptToken(conn.Token, h.jwtSecret)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"ok": false, "error": "token decryption failed"})
		return
	}

	// Fetch base URL.
	baseURL, err := h.store.GetSiteConfigValue(ctx, "confluence_base_url")
	if err != nil || baseURL == nil || *baseURL == "" {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"ok": false, "error": "confluence_base_url not configured"})
		return
	}

	// Call Confluence API.
	client := NewConfluenceClient(*baseURL, plaintext)
	verifyErr := client.VerifyConnection(ctx)

	// Update last_verified_at (only on success) and status.
	now := time.Now().UTC()
	status := "active"
	var errMsg *string

	if verifyErr != nil {
		status = "error"
		msg := verifyErr.Error()
		errMsg = &msg
	}

	updates := ConfluenceConnectionUpdate{
		Status:       &status,
		ErrorMessage: errMsg,
	}
	if verifyErr == nil {
		updates.LastVerifiedAt = &now
	}
	_, _ = h.store.UpdateConfluenceConnection(ctx, conn.ID, updates)

	if verifyErr != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"ok": false, "error": verifyErr.Error()})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// GET /api/confluence/spaces
// ---------------------------------------------------------------------------

// ListSpaces handles GET /api/confluence/spaces.
func (h *Handler) ListSpaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	client, err := h.confluenceClientForUser(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, err.Error())
		return
	}

	spaces, err := client.ListSpaces(ctx)
	if err != nil {
		h.writeConfluenceCallError(ctx, w, user.ID, "failed to list Confluence spaces", err)
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, spaces)
}

// ---------------------------------------------------------------------------
// GET /api/confluence/spaces/{spaceKey}/pages
// ---------------------------------------------------------------------------

// ListSpacePages handles GET /api/confluence/spaces/{spaceKey}/pages.
func (h *Handler) ListSpacePages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	spaceKey := r.PathValue("spaceKey")
	if spaceKey == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "spaceKey is required")
		return
	}

	client, err := h.confluenceClientForUser(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, err.Error())
		return
	}

	pages, err := client.ListSpacePages(ctx, spaceKey)
	if err != nil {
		h.writeConfluenceCallError(ctx, w, user.ID, "failed to list pages", err)
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, pages)
}

// ---------------------------------------------------------------------------
// GET /api/confluence/pages/{pageId}/children
// ---------------------------------------------------------------------------

// ListPageChildren handles GET /api/confluence/pages/{pageId}/children.
func (h *Handler) ListPageChildren(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	pageID := r.PathValue("pageId")
	if pageID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "pageId is required")
		return
	}

	client, err := h.confluenceClientForUser(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, err.Error())
		return
	}

	children, err := client.ListPageChildren(ctx, pageID)
	if err != nil {
		h.writeConfluenceCallError(ctx, w, user.ID, "failed to list page children", err)
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, children)
}

// ---------------------------------------------------------------------------
// GET /api/confluence/spaces/{spaceKey}/pages/all
// ---------------------------------------------------------------------------

// ListAllSpacePages handles GET /api/confluence/spaces/{spaceKey}/pages/all.
// Returns every page in the space with its ancestor titles, used by the
// import-modal search flow to filter pages client-side.
func (h *Handler) ListAllSpacePages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	spaceKey := r.PathValue("spaceKey")
	if spaceKey == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "spaceKey is required")
		return
	}

	client, err := h.confluenceClientForUser(ctx, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, err.Error())
		return
	}

	pages, err := client.GetAllSpacePagesWithAncestors(ctx, spaceKey)
	if err != nil {
		h.writeConfluenceCallError(ctx, w, user.ID, "failed to list pages", err)
		return
	}
	if pages == nil {
		pages = []ConfluencePageWithPath{}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, pages)
}
