// Package adminglobalkbs provides CRUD handlers for admin global knowledge bases
// and their editors.
package adminglobalkbs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbmembers"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// GlobalKBRow is the shape returned to API consumers for a global knowledge base.
// The frontend GlobalKbSettings form round-trips the same fields it edits, so
// this row must include every column the admin UI renders — otherwise the
// PATCH response wipes local state and the next render shows stale values.
type GlobalKBRow struct {
	ID             string    `json:"id"             db:"id"`
	Name           string    `json:"name"           db:"name"`
	Description    *string   `json:"description"    db:"description"`
	Language       string    `json:"language"       db:"language"`
	IsPublished    bool      `json:"isPublished"    db:"is_published"`
	AutoSubscribe  bool      `json:"autoSubscribe"  db:"auto_subscribe"`
	SystemPrompt   *string   `json:"systemPrompt"   db:"system_prompt"`
	HeaderText     *string   `json:"headerText"     db:"header_text"`
	ExamplePrompts *string   `json:"examplePrompts" db:"example_prompts"`
	AIConfigID     *string   `json:"aiConfigId"     db:"ai_config_id"`
	ChatModel      *string   `json:"chatModel"      db:"chat_model"`
	EmbeddingModel *string   `json:"embeddingModel" db:"embedding_model"`
	RerankModel    *string   `json:"rerankModel"    db:"rerank_model"`
	TTSModel       *string   `json:"ttsModel"       db:"tts_model"`
	SttModel       *string   `json:"sttModel"       db:"stt_model"`
	CreatedAt      time.Time `json:"createdAt"      db:"created_at"`
}

// GlobalKBEditorRow is the shape returned for an editor of a global KB — a
// kb_members row with role='admin'. ID is the *user* id: the wire shape
// predates kb_members and is kept unchanged so the admin UI keeps working.
type GlobalKBEditorRow struct {
	ID        string    `json:"id"        db:"id"`
	Username  string    `json:"username"  db:"username"`
	FirstName *string   `json:"firstName" db:"first_name"`
	LastName  *string   `json:"lastName"  db:"last_name"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// GlobalKBCreate carries the fields for creating a new global KB.
type GlobalKBCreate struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Language    string  `json:"language"` // default "de"
	// CreatedBy is the admin performing the creation. They are enrolled as a
	// KB admin (kb_members role='admin') on the new KB — a public KB has no
	// owner, so a curator membership is the only thing that ties it to a
	// person. Without it the creator would not see their own new KB in the
	// overview: the Favoriten list is subscription-filtered for admins too
	// (kb.Handler.ListGlobalKnowledgeBases), a fresh KB has no subscribers,
	// and auto_subscribe defaults to false. Empty is tolerated (no
	// membership is written) so non-HTTP callers and tests need not supply
	// one.
	CreatedBy string `json:"-"`
}

// GlobalKBUpdate carries the fields to update on a global KB. Only non-nil
// fields are applied. NullFields tracks JSON fields that were explicitly set
// to null so the store can clear nullable columns (instead of skipping them).
type GlobalKBUpdate struct {
	Name           *string         `json:"name"`
	Description    *string         `json:"description"`
	Language       *string         `json:"language"`
	IsPublished    *bool           `json:"isPublished"`
	AutoSubscribe  *bool           `json:"autoSubscribe"`
	SystemPrompt   *string         `json:"systemPrompt"`
	HeaderText     *string         `json:"headerText"`
	ExamplePrompts *string         `json:"examplePrompts"`
	AIConfigID     *string         `json:"aiConfigId"`
	ChatModel      *string         `json:"chatModel"`
	EmbeddingModel *string         `json:"embeddingModel"`
	RerankModel    *string         `json:"rerankModel"`
	TTSModel       *string         `json:"ttsModel"`
	SttModel       *string         `json:"sttModel"`
	NullFields     map[string]bool `json:"-"`
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the persistence interface required by Handler.
type Store interface {
	ListGlobalKBs(ctx context.Context) ([]GlobalKBRow, error)
	CreateGlobalKB(ctx context.Context, data GlobalKBCreate) (*GlobalKBRow, error)
	UpdateGlobalKB(ctx context.Context, id string, data GlobalKBUpdate) (*GlobalKBRow, error)
	ListGlobalKBEditors(ctx context.Context, kbID string) ([]GlobalKBEditorRow, error)
	// AddGlobalKBEditor grants role='admin'. grantedBy is the acting operator
	// (recorded as kb_members.created_by); may be empty.
	AddGlobalKBEditor(ctx context.Context, kbID, userID, grantedBy string) error
	RemoveGlobalKBEditor(ctx context.Context, kbID, userID string) error
	LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error
}

// CascadeDeleter is the interface used by Handler to remove a global KB and
// all its associated assets (vector chunks, storage files, and database rows).
type CascadeDeleter interface {
	DeleteGlobalKB(ctx context.Context, kbID string) error
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the global KB admin endpoints.
type Handler struct {
	store   Store
	deleter CascadeDeleter
}

// NewHandler creates a Handler backed by store. No cascade deleter — use NewHandlerWithDeleter.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// NewHandlerWithDeleter creates a Handler with a CascadeDeleter for full cascade KB deletion.
func NewHandlerWithDeleter(store Store, deleter CascadeDeleter) *Handler {
	return &Handler{store: store, deleter: deleter}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// operatorID extracts the authenticated user's ID from the request context.
// Returns an empty string if not authenticated (should not happen behind admin middleware).
func operatorID(r *http.Request) string {
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		return ""
	}
	return claims.ID
}

// ---------------------------------------------------------------------------
// Endpoint handlers — Global KBs
// ---------------------------------------------------------------------------

// ListGlobalKBs handles GET /api/admin/global-kbs.
func (h *Handler) ListGlobalKBs(w http.ResponseWriter, r *http.Request) {
	kbs, err := h.store.ListGlobalKBs(r.Context())
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch global KBs")
		return
	}
	if kbs == nil {
		kbs = []GlobalKBRow{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, kbs)
}

// createGlobalKBRequest is the parsed JSON body for POST /api/admin/global-kbs.
type createGlobalKBRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Language    string  `json:"language"`
}

// CreateGlobalKB handles POST /api/admin/global-kbs.
func (h *Handler) CreateGlobalKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body createGlobalKBRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "name is required")
		return
	}

	lang := strings.TrimSpace(body.Language)
	if lang == "" {
		lang = "de"
	}

	kb, err := h.store.CreateGlobalKB(ctx, GlobalKBCreate{
		Name:        name,
		Description: body.Description,
		Language:    lang,
		CreatedBy:   operatorID(r),
	})
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create global KB")
		return
	}

	// Audit log (best-effort; do not fail the request on log errors).
	_ = h.store.LogAuditAction(ctx, operatorID(r), "global_kb.create", "global_kb", kb.ID, map[string]any{"name": kb.Name})

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, kb)
}

// UnmarshalJSON parses a GlobalKBUpdate body and tracks which nullable
// fields were explicitly set to null, so the store knows to SET NULL on
// those columns instead of skipping them.
func (u *GlobalKBUpdate) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	u.NullFields = make(map[string]bool)
	nullable := []string{"description", "systemPrompt", "headerText", "examplePrompts", "aiConfigId", "chatModel", "embeddingModel", "rerankModel", "ttsModel", "sttModel"}
	for _, f := range nullable {
		if v, ok := raw[f]; ok && string(v) == "null" {
			u.NullFields[f] = true
		}
	}
	type alias GlobalKBUpdate
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	a.NullFields = u.NullFields
	*u = GlobalKBUpdate(a)
	return nil
}

// UpdateGlobalKB handles PATCH /api/admin/global-kbs/{id}.
func (h *Handler) UpdateGlobalKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing KB id")
		return
	}

	var body GlobalKBUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	kb, err := h.store.UpdateGlobalKB(ctx, id, body)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update global KB")
		return
	}
	if kb == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "global KB not found")
		return
	}

	_ = h.store.LogAuditAction(ctx, operatorID(r), "global_kb.update", "global_kb", id, map[string]any{"name": kb.Name})

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, kb)
}

// DeleteGlobalKB handles DELETE /api/admin/global-kbs/{id}.
//
// The cascade deleter removes all files, vector chunks, storage objects,
// chats, generated content, editors, shares, and finally the KB record itself.
func (h *Handler) DeleteGlobalKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing KB id")
		return
	}

	if err := h.deleter.DeleteGlobalKB(ctx, id); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete global KB")
		return
	}

	_ = h.store.LogAuditAction(ctx, operatorID(r), "global_kb.delete", "global_kb", id, nil)

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Endpoint handlers — Editors
// ---------------------------------------------------------------------------
//
// "Editor" of a global KB means a kb_members row with role='admin' — the
// single authority kbaccess.EffectiveRole resolves from. These endpoints used
// to read and write global_kb_editors, which EffectiveRole stopped consulting
// when migration 0064 landed: adding an editor was a silent no-op grant, and
// removing one left the backfilled kb_members admin row in place, i.e. an
// invisible and un-revokable privilege. They now go through kbmembers.Store.

// ListEditors handles GET /api/admin/global-kbs/{id}/editors.
func (h *Handler) ListEditors(w http.ResponseWriter, r *http.Request) {
	kbID := r.PathValue("id")
	if kbID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing KB id")
		return
	}

	editors, err := h.store.ListGlobalKBEditors(r.Context(), kbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch editors")
		return
	}
	if editors == nil {
		editors = []GlobalKBEditorRow{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, editors)
}

// addEditorRequest is the parsed JSON body for POST /api/admin/global-kbs/{id}/editors.
type addEditorRequest struct {
	UserID string `json:"userId"`
}

// AddEditor handles POST /api/admin/global-kbs/{id}/editors.
func (h *Handler) AddEditor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")
	if kbID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing KB id")
		return
	}

	var body addEditorRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	userID := strings.TrimSpace(body.UserID)
	if userID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "userId is required")
		return
	}

	operator := operatorID(r)
	switch err := h.store.AddGlobalKBEditor(ctx, kbID, userID, operator); {
	case errors.Is(err, kbmembers.ErrOwnerImmutable):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict,
			"that user owns this knowledge base — their role cannot be changed here")
		return
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	_ = h.store.LogAuditAction(ctx, operator, "global_kb.editor.add", "global_kb", kbID,
		map[string]any{"userId": userID, "role": kbaccess.RoleAdmin})

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, map[string]bool{"success": true})
}

// RemoveEditor handles DELETE /api/admin/global-kbs/{id}/editors/{userId}.
func (h *Handler) RemoveEditor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")
	userID := r.PathValue("userId")
	if kbID == "" || userID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing KB id or user id")
		return
	}

	switch err := h.store.RemoveGlobalKBEditor(ctx, kbID, userID); {
	case errors.Is(err, kbmembers.ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "editor not found")
		return
	case errors.Is(err, kbmembers.ErrOwnerImmutable):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict,
			"that user owns this knowledge base and cannot be removed here")
		return
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	_ = h.store.LogAuditAction(ctx, operatorID(r), "global_kb.editor.remove", "global_kb", kbID,
		map[string]any{"userId": userID})

	w.WriteHeader(http.StatusNoContent)
}
