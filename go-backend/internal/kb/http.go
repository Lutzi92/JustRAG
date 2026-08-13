// Package kb provides HTTP handlers for listing and creating knowledge bases.
package kb

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// KBRow is the full knowledge-base shape returned to API consumers.
type KBRow struct {
	ID             string          `json:"id"             db:"id"`
	Name           string          `json:"name"           db:"name"`
	UserID         *string         `json:"userId"         db:"user_id"`
	Description    *string         `json:"description"    db:"description"`
	IsGlobal       bool            `json:"isGlobal"       db:"is_global"`
	Visibility     string          `json:"visibility"     db:"visibility"`
	IsPublished    bool            `json:"isPublished"    db:"is_published"`
	Language       string          `json:"language"       db:"language"`
	SystemPrompt   *string         `json:"systemPrompt"   db:"system_prompt"`
	HeaderText     *string         `json:"headerText"     db:"header_text"`
	ExamplePrompts *string         `json:"examplePrompts" db:"example_prompts"`
	AIConfigID     *string         `json:"aiConfigId"     db:"ai_config_id"`
	ChatModel      *string         `json:"chatModel"      db:"chat_model"`
	EmbeddingModel *string         `json:"embeddingModel" db:"embedding_model"`
	RerankModel    *string         `json:"rerankModel"    db:"rerank_model"`
	TTSModel       *string         `json:"ttsModel"       db:"tts_model"`
	SttModel       *string         `json:"sttModel"       db:"stt_model"`
	ChunkSize      *int            `json:"chunkSize"      db:"chunk_size"`
	ChunkOverlap   *int            `json:"chunkOverlap"   db:"chunk_overlap"`
	StudioConfig   json.RawMessage `json:"studioConfig"   db:"studio_config"`
	CreatedAt      time.Time       `json:"createdAt"      db:"created_at"`

	// Owner attribution — populated by ListKnowledgeBases so the UI can show
	// "shared by …" for KBs the current user does not own. Null when the KB
	// has no owner row.
	OwnerFirstName *string `json:"ownerFirstName,omitempty" db:"owner_first_name"`
	OwnerLastName  *string `json:"ownerLastName,omitempty"  db:"owner_last_name"`
	OwnerUsername  *string `json:"ownerUsername,omitempty"  db:"owner_username"`

	// Card information-scent metadata — populated only by the list queries
	// (ListKnowledgeBases / ListGlobalKnowledgeBases) via cheap per-row LATERAL
	// aggregates, so the Home KB cards can show size + freshness without an
	// N+1 fetch. Zero/omitted on single-row fetches (create / get-by-id).
	FileCount           int        `json:"fileCount"`
	FailedFileCount     int        `json:"failedFileCount"`
	ProcessingFileCount int        `json:"processingFileCount"`
	MessageCount        int        `json:"messageCount"`
	LastMessageAt       *time.Time `json:"lastMessageAt,omitempty"`

	// MyRole is the caller's own kb_members.role for this KB, and MemberCount
	// the KB's total member count — populated only by the same two list
	// queries as the card metadata above, via correlated subqueries. MyRole
	// is null when the caller has no kb_members row (a published global KB
	// grants an implicit 'view' role to any authenticated user without one);
	// the frontend must treat a null MyRole as an implicit viewer, never as
	// owner. Zero/nil on single-row fetches (create / get-by-id).
	MyRole      *string `json:"myRole"`
	MemberCount int     `json:"memberCount"`
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the persistence interface required by Handler.
type Store interface {
	ListKnowledgeBases(ctx context.Context, userID string, limit, offset int) ([]KBRow, error)
	ListGlobalKnowledgeBases(ctx context.Context, userID string, isAdmin bool) ([]KBRow, error)
	CreateKnowledgeBase(ctx context.Context, name string, description *string, userID string, systemPrompt *string) (*KBRow, error)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the KB endpoints.
type Handler struct {
	store Store
}

// NewHandler creates a Handler backed by store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Endpoint handlers
// ---------------------------------------------------------------------------

// ListKnowledgeBases handles GET /api/kb.
// Returns all KBs the authenticated user can access: owned, shared, and global published.
func (h *Handler) ListKnowledgeBases(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	limit := 50
	offset := 0

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	kbs, err := h.store.ListKnowledgeBases(r.Context(), user.ID, limit, offset)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch knowledge bases")
		return
	}
	if kbs == nil {
		kbs = []KBRow{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, kbs)
}

// ListGlobalKnowledgeBases handles GET /api/kb/global — the "Favoriten"
// section of the overview.
//
// The subscription filter applies to system admins too, which is why `false`
// is passed unconditionally instead of deriving it from user.Role. Favoriten
// means "the public KBs I chose to keep", and the store's admin arm (every
// public KB, published or not, regardless of subscription) contradicts that:
// it left an admin unable to remove a tile at all, since the KB reappeared on
// the next fetch no matter what they clicked. The full inventory stays
// reachable — the discovery panel lists every published public KB, and the
// admin tabs list the staged ones as well. The store's isAdmin arm is still
// live for openaicompat, whose API surface deliberately keeps addressing
// every public KB.
func (h *Handler) ListGlobalKnowledgeBases(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbs, err := h.store.ListGlobalKnowledgeBases(r.Context(), user.ID, false)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch global knowledge bases")
		return
	}
	if kbs == nil {
		kbs = []KBRow{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, kbs)
}

// createKBRequest is the parsed JSON body for POST /api/kb.
type createKBRequest struct {
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	SystemPrompt *string `json:"systemPrompt"`
}

// CreateKnowledgeBase handles POST /api/kb.
// Creates a new knowledge base owned by the authenticated user.
func (h *Handler) CreateKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body createKBRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > 255 {
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

	kb, err := h.store.CreateKnowledgeBase(r.Context(), name, body.Description, user.ID, body.SystemPrompt)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create knowledge base")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, kb)
}
