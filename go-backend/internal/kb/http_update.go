// This file contains the KB update and file-listing handlers.
// KBRow is defined in handler.go (same package).
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/store"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// KBUpdate carries the fields to update on a knowledge base.
// Only non-nil fields are applied (partial update / PATCH semantics).
// NullFields tracks which fields were explicitly set to null in the JSON body
// (so the store can SET them to NULL instead of ignoring them).
type KBUpdate struct {
	Name           *string         `json:"name"`
	Description    *string         `json:"description"`
	Language       *string         `json:"language"`
	SystemPrompt   *string         `json:"systemPrompt"`
	HeaderText     *string         `json:"headerText"`
	ExamplePrompts *string         `json:"examplePrompts"`
	AIConfigID     *string         `json:"aiConfigId"`
	ChatModel      *string         `json:"chatModel"`
	EmbeddingModel *string         `json:"embeddingModel"`
	RerankModel    *string         `json:"rerankModel"`
	TTSModel       *string         `json:"ttsModel"`
	SttModel       *string         `json:"sttModel"`
	ChunkSize      *int            `json:"chunkSize"`
	ChunkOverlap   *int            `json:"chunkOverlap"`
	IsPublished    *bool           `json:"isPublished"`
	StudioConfig   json.RawMessage `json:"studioConfig"`
	NullFields     map[string]bool `json:"-"` // set of field names explicitly sent as null
}

// UnmarshalJSON detects explicit nulls so the store can clear nullable columns.
func (u *KBUpdate) UnmarshalJSON(data []byte) error {
	// First, decode into a raw map to detect explicit nulls.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	u.NullFields = make(map[string]bool)
	nullableFields := []string{"description", "systemPrompt", "headerText", "examplePrompts", "aiConfigId", "chatModel", "embeddingModel", "rerankModel", "ttsModel", "sttModel"}
	for _, f := range nullableFields {
		if v, exists := raw[f]; exists && string(v) == "null" {
			u.NullFields[f] = true
		}
	}

	// Now do a standard decode via an alias to avoid infinite recursion.
	type Alias KBUpdate
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	alias.NullFields = u.NullFields
	*u = KBUpdate(alias)
	return nil
}

// FileRow is the shape returned to API consumers for a file belonging to a KB.
type FileRow struct {
	ID                 string    `json:"id"                 db:"id"`
	Name               string    `json:"name"               db:"name"`
	Type               string    `json:"type"               db:"type"`
	Size               *int      `json:"size"               db:"size"`
	Status             string    `json:"status"             db:"status"`
	Progress           int       `json:"progress"           db:"progress"`
	Origin             string    `json:"origin"             db:"origin"`
	RSSFeedID          *string   `json:"rssFeedId"          db:"rss_feed_id"`
	ConfluenceSourceID *string   `json:"confluenceSourceId" db:"confluence_source_id"`
	CreatedAt          time.Time `json:"createdAt"           db:"created_at"`
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// UpdateStore is the narrow persistence contract required by UpdateHandler —
// a KB-level update, file listing, and chunk-config lookup. Kept separate
// from kb.Store (list/create KBs) and kb.ShareStore (sharing) so each
// handler's tests can use a minimal fake.
type UpdateStore interface {
	// UpdateKnowledgeBase applies non-nil fields from data to the KB identified
	// by id. Returns nil, nil if the KB does not exist.
	UpdateKnowledgeBase(ctx context.Context, id string, data KBUpdate) (*KBRow, error)

	// ListFiles returns a page of files belonging to kbID together with the
	// total count across all pages.
	ListFiles(ctx context.Context, kbID string, limit, offset int) ([]FileRow, int, error)

	// GetKBChunkConfig returns the current chunk_size and chunk_overlap for a KB.
	GetKBChunkConfig(ctx context.Context, kbID string) (chunkSize, chunkOverlap int, err error)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// UpdateHandler holds the dependencies for the KB update and file-listing endpoints.
type UpdateHandler struct {
	store            UpdateStore
	onKBConfigChange func(kbID string) // called after AI-related KB fields change
}

// NewUpdateHandler creates an UpdateHandler backed by store.
// onKBConfigChange is called after updates that affect AI config (model overrides, aiConfigId).
func NewUpdateHandler(store UpdateStore, onKBConfigChange func(kbID string)) *UpdateHandler {
	return &UpdateHandler{store: store, onKBConfigChange: onKBConfigChange}
}

// ---------------------------------------------------------------------------
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
// PATCH /api/kb/{id}
// ---------------------------------------------------------------------------

// UpdateKB handles PATCH /api/kb/{id}.
// It accepts a partial JSON body matching KBUpdate, applies only the supplied
// fields, and returns the full updated KB record.
func (h *UpdateHandler) UpdateKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := kbIDFromContext(r)

	var body KBUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate fields that have constraints.
	if body.Name != nil && utf8.RuneCountInString(*body.Name) > 255 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "name must not exceed 255 characters")
		return
	}
	if body.Description != nil && utf8.RuneCountInString(*body.Description) > 2000 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "description must not exceed 2000 characters")
		return
	}
	if body.SystemPrompt != nil && utf8.RuneCountInString(*body.SystemPrompt) > 4000 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "systemPrompt must not exceed 4000 characters")
		return
	}
	if body.ChunkSize != nil {
		cs := *body.ChunkSize
		if cs < 128 || cs > 4096 {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "chunkSize must be between 128 and 4096")
			return
		}
	}
	if body.ChunkOverlap != nil {
		co := *body.ChunkOverlap
		if co < 0 || co > 512 {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "chunkOverlap must be between 0 and 512")
			return
		}
		// Determine the effective chunkSize: use the incoming value if provided,
		// otherwise fetch the current value from the DB.
		var effectiveChunkSize int
		if body.ChunkSize != nil {
			effectiveChunkSize = *body.ChunkSize
		} else {
			existingCS, _, csErr := h.store.GetKBChunkConfig(ctx, id)
			if csErr != nil {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to validate chunk configuration")
				return
			}
			effectiveChunkSize = existingCS
		}
		if effectiveChunkSize > 0 && co >= effectiveChunkSize {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "chunkOverlap must be less than chunkSize")
			return
		}
	}

	kb, err := h.store.UpdateKnowledgeBase(ctx, id, body)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "knowledge base not found")
			return
		}
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update knowledge base")
		return
	}

	// Invalidate AI config cache if model-related fields changed
	if h.onKBConfigChange != nil && (body.AIConfigID != nil || body.ChatModel != nil ||
		body.EmbeddingModel != nil || body.RerankModel != nil ||
		body.NullFields["aiConfigId"] || body.NullFields["chatModel"] ||
		body.NullFields["embeddingModel"] || body.NullFields["rerankModel"]) {
		h.onKBConfigChange(id)
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, kb)
}

// ---------------------------------------------------------------------------
// GET /api/kb/{id}/files
// ---------------------------------------------------------------------------

// ListFiles handles GET /api/kb/{id}/files.
// Query parameters:
//   - limit  (default 50, max 10000)
//   - offset (default 0)
//
// The chat UI asks for up to 10000 files so users can see and select the full
// corpus (Confluence imports regularly exceed 500 pages). A lower cap silently
// truncated the selection set and caused search to ignore older files.
func (h *UpdateHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFromContext(r)

	limit := 50
	offset := 0

	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 10000 {
		limit = 10000
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}

	files, _, err := h.store.ListFiles(ctx, kbID, limit, offset)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to list files")
		return
	}
	if files == nil {
		files = []FileRow{}
	}

	// Return flat array to match the Node.js API contract.
	// The frontend calls res.data.map(...) expecting an array, not an envelope.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, files)
}
