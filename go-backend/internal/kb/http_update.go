// This file contains the KB update and file-listing handlers.
// KBRow is defined in handler.go (same package).
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/auth"
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
	ErrorStage         *string   `json:"errorStage,omitempty"   db:"error_stage"`
	ErrorMessage       *string   `json:"errorMessage,omitempty" db:"error_message"`
	CurrentStage       *string   `json:"currentStage,omitempty" db:"current_stage"`
	StageIndex         *int      `json:"stageIndex,omitempty"   db:"stage_index"`
	StageTotal         *int      `json:"stageTotal,omitempty"   db:"stage_total"`
	StageDetail        *string   `json:"stageDetail,omitempty"  db:"stage_detail"`
	RSSFeedID          *string   `json:"rssFeedId"          db:"rss_feed_id"`
	ConfluenceSourceID *string   `json:"confluenceSourceId" db:"confluence_source_id"`
	CreatedAt          time.Time `json:"createdAt"           db:"created_at"`
	// InjectionFlag / InjectionDetail carry the ingest-time prompt-injection
	// screening verdict (migration 0072, W5-R8). The flag is advisory: it
	// never affected what was ingested, chunked or retrieved, it only tells
	// an operator that this externally sourced document contains
	// instruction-shaped text. InjectionDetail is {rule, position, snippet,
	// screened_at} on a hit, {screened_at} alone on a clean pass, and absent
	// when the file was never screened; the snippet is untrusted,
	// document-derived text and must be rendered as data (a tooltip), never
	// re-sent to a model.
	InjectionFlag   bool            `json:"injectionFlag"             db:"injection_flag"`
	InjectionDetail json.RawMessage `json:"injectionDetail,omitempty" db:"injection_detail"`
	// UploadedBy is who added the file (migration 0075). Nil for
	// source-owned origins and for rows that predate the column; the
	// handler also nils it for callers below KB role "edit", so the key is
	// absent rather than null in both cases.
	UploadedBy *FileUploader `json:"uploadedBy,omitempty" db:"-"`
	// UserFileID links a KB copy back to its user-library file (migration
	// 0076); nil for non-library files, so the key is omitted.
	UserFileID *string `json:"userFileId,omitempty" db:"-"`
}

// FileUploader is the display identity behind FileRow.UploadedBy — the same
// identity the KB member list shows (first + last name, falling back to the
// username), never the email.
type FileUploader struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// UpdateStore is the narrow persistence contract required by UpdateHandler —
// a KB-level update, file listing, and chunk-config lookup. Kept separate
// from kb.Store (list/create KBs) so each handler's tests can use a minimal
// fake.
type UpdateStore interface {
	// UpdateKnowledgeBase applies non-nil fields from data to the KB identified
	// by id. Returns nil, nil if the KB does not exist.
	UpdateKnowledgeBase(ctx context.Context, id string, data KBUpdate) (*KBRow, error)

	// GetKnowledgeBase re-reads the KB for the caller after an update — the
	// same caller-aware row GET /api/kb/{id} returns (stats, membership,
	// favorite and categories), which the update's RETURNING clause cannot
	// produce. Returns (nil, nil) when the KB does not exist.
	GetKnowledgeBase(ctx context.Context, kbID, userID string) (*KBRow, error)

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
// fields, and returns the updated KB in the same caller-aware shape as
// GET /api/kb/{id}: card stats, the caller's membership, favorite and
// categories. The UI replaces its card with this response, so a bare
// RETURNING row would drop the star and the counts.
func (h *UpdateHandler) UpdateKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := kbIDFromContext(r)

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body KBUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Renaming is stricter than the route's kbAdminChain: only the owner of a
	// private KB, or a system admin on a public (ownerless) KB, may change the
	// name (kbaccess.CanRename). Every other PATCH field stays at KB role admin.
	if body.Name != nil {
		if !kbaccess.CanRename(kbaccess.AccessFromContext(ctx), user.Role) {
			httputil.WriteErrorCtx(ctx, w, http.StatusForbidden, "only the owner may rename a knowledge base")
			return
		}
		trimmed := strings.TrimSpace(*body.Name)
		if trimmed == "" {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "name must not be empty")
			return
		}
		body.Name = &trimmed
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
	if body.SystemPrompt != nil && utf8.RuneCountInString(*body.SystemPrompt) > 8000 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "systemPrompt must not exceed 8000 characters")
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

	if _, err := h.store.UpdateKnowledgeBase(ctx, id, body); err != nil {
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

	// The write has committed by now; a failed re-read is reported as a 500
	// rather than papered over with the RETURNING row, which is exactly the
	// incomplete shape this re-read exists to replace. PATCH is idempotent,
	// so the client may simply retry.
	kb, err := h.store.GetKnowledgeBase(ctx, id, user.ID)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("re-read knowledge base after update: %w", err))
		return
	}
	if kb == nil {
		// Deleted between the update and the re-read.
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "knowledge base not found")
		return
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

	// uploadedBy is an editor-facing field: a KB viewer learns nothing about
	// who curates the corpus. Missing access info fails closed.
	if access := kbaccess.AccessFromContext(ctx); access == nil || !kbaccess.AtLeast(access.Role, kbaccess.RoleEdit) {
		for i := range files {
			files[i].UploadedBy = nil
		}
	}

	// Return flat array to match the Node.js API contract.
	// The frontend calls res.data.map(...) expecting an array, not an envelope.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, files)
}
