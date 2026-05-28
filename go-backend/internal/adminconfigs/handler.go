// Package adminconfigs provides CRUD handlers for admin AI provider configs.
package adminconfigs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/store"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// AIModelInput is the request shape for a single model within a create/update body.
type AIModelInput struct {
	Name        string `json:"name"`
	IsReasoning bool   `json:"isReasoning"`
	Dimensions  int    `json:"dimensions,omitempty"` // for embedding models
}

// AIModelRow is the response shape for a single AI model.
type AIModelRow struct {
	ID          string `json:"id"          db:"id"`
	Name        string `json:"name"        db:"name"`
	IsReasoning bool   `json:"isReasoning" db:"is_reasoning"`
	IsEmbedding bool   `json:"isEmbedding" db:"is_embedding"`
	IsRerank    bool   `json:"isRerank"    db:"is_rerank"`
	IsTts       bool   `json:"isTts"       db:"is_tts"`
	IsStt       bool   `json:"isStt"       db:"is_stt"`
	Dimensions  int    `json:"dimensions,omitempty" db:"dimensions"`
}

// AIConfigResponse is the full config shape returned to API consumers (key masked).
type AIConfigResponse struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Provider        string       `json:"provider"`
	APIKey          string       `json:"api_key"` // masked
	BaseURL         *string      `json:"base_url"`
	ChatModels      []AIModelRow `json:"chat_models"`
	EmbeddingModels []AIModelRow `json:"embedding_models"`
	ReasoningModels []AIModelRow `json:"reasoning_models"`
	RerankModels    []AIModelRow `json:"rerank_models"`
	TtsModels       []AIModelRow `json:"tts_models"`
	SttModels       []AIModelRow `json:"stt_models"`
	IsActive        bool         `json:"is_active"`
	CreatedAt       time.Time    `json:"created_at"`
}

// CreateAIConfigInput is passed to Store.CreateAIConfig.
type CreateAIConfigInput struct {
	Name            string
	Provider        string
	APIKey          string
	BaseURL         *string
	ChatModels      []AIModelInput
	EmbeddingModels []AIModelInput
	ReasoningModels []AIModelInput
	RerankModels    []AIModelInput
	TtsModels       []AIModelInput
	SttModels       []AIModelInput
}

// UpdateAIConfigInput is passed to Store.UpdateAIConfig.  Nil slices mean "no change".
type UpdateAIConfigInput struct {
	Name            *string
	APIKey          *string
	BaseURL         *string
	ChatModels      []AIModelInput
	EmbeddingModels []AIModelInput
	ReasoningModels []AIModelInput
	RerankModels    []AIModelInput
	TtsModels       []AIModelInput
	SttModels       []AIModelInput
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the persistence interface required by Handler.
type Store interface {
	ListAIConfigs(ctx context.Context) ([]AIConfigResponse, error)
	GetAIConfigByID(ctx context.Context, id string) (*AIConfigResponse, error)
	GetAIProviderByID(ctx context.Context, id string) (*ai.AIProviderInfo, error)
	CreateAIConfig(ctx context.Context, input CreateAIConfigInput) (*AIConfigResponse, error)
	UpdateAIConfig(ctx context.Context, id string, input UpdateAIConfigInput) (*AIConfigResponse, error)
	DeleteAIConfig(ctx context.Context, id string) error
	ActivateAIConfig(ctx context.Context, id string) error
	LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the AI configs admin endpoints.
type Handler struct {
	store          Store
	onConfigChange func() // called after any mutation to invalidate caches
}

// NewHandler creates a Handler backed by store. onConfigChange is called after
// create/update/delete/activate to invalidate AI config caches.
func NewHandler(store Store, onConfigChange func()) *Handler {
	return &Handler{store: store, onConfigChange: onConfigChange}
}

// notifyConfigChange calls the registered callback (if any) to invalidate AI caches.
func (h *Handler) notifyConfigChange() {
	if h.onConfigChange != nil {
		h.onConfigChange()
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// maskAPIKey returns a masked version of the API key: first 4 + stars + last 4.
// Keys of 8 characters or fewer are fully masked.
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

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
// createConfigRequest is the parsed JSON body for POST /api/admin/configs
// ---------------------------------------------------------------------------

type createConfigRequest struct {
	Name            string         `json:"name"`
	Provider        string         `json:"provider"`
	APIKey          string         `json:"api_key"`
	BaseURL         *string        `json:"base_url"`
	ChatModels      []AIModelInput `json:"chat_models"`
	EmbeddingModels []AIModelInput `json:"embedding_models"`
	ReasoningModels []AIModelInput `json:"reasoning_models"`
	RerankModels    []AIModelInput `json:"rerank_models"`
	TtsModels       []AIModelInput `json:"tts_models"`
	SttModels       []AIModelInput `json:"stt_models"`
}

// updateConfigRequest is the parsed JSON body for PATCH /api/admin/configs/{id}.
// All fields are optional; only provided fields are applied.
type updateConfigRequest struct {
	Name            *string        `json:"name"`
	APIKey          *string        `json:"api_key"`
	BaseURL         *string        `json:"base_url"`
	ChatModels      []AIModelInput `json:"chat_models"`
	EmbeddingModels []AIModelInput `json:"embedding_models"`
	ReasoningModels []AIModelInput `json:"reasoning_models"`
	RerankModels    []AIModelInput `json:"rerank_models"`
	TtsModels       []AIModelInput `json:"tts_models"`
	SttModels       []AIModelInput `json:"stt_models"`
}

// ---------------------------------------------------------------------------
// Endpoint handlers
// ---------------------------------------------------------------------------

// ListConfigs handles GET /api/admin/configs.
func (h *Handler) ListConfigs(w http.ResponseWriter, r *http.Request) {
	configs, err := h.store.ListAIConfigs(r.Context())
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch configs")
		return
	}
	if configs == nil {
		configs = []AIConfigResponse{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, configs)
}

// CreateConfig handles POST /api/admin/configs.
func (h *Handler) CreateConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body createConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(body.Name)
	provider := strings.TrimSpace(body.Provider)
	apiKey := strings.TrimSpace(body.APIKey)

	if name == "" || provider == "" || apiKey == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "name, provider, and api_key are required")
		return
	}

	// Validate embedding model dimensions.
	for _, m := range body.EmbeddingModels {
		if m.Dimensions < 0 {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "embedding model dimensions must be >= 0")
			return
		}
	}

	input := CreateAIConfigInput{
		Name:            name,
		Provider:        provider,
		APIKey:          apiKey,
		BaseURL:         body.BaseURL,
		ChatModels:      body.ChatModels,
		EmbeddingModels: body.EmbeddingModels,
		ReasoningModels: body.ReasoningModels,
		RerankModels:    body.RerankModels,
		TtsModels:       body.TtsModels,
		SttModels:       body.SttModels,
	}

	config, err := h.store.CreateAIConfig(ctx, input)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create config")
		return
	}

	// Audit log (best-effort; do not fail the request on log errors).
	_ = h.store.LogAuditAction(ctx, operatorID(r), "config.create", "config", config.ID, map[string]any{"name": config.Name, "provider": config.Provider})
	h.notifyConfigChange()

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, config)
}

// UpdateConfig handles PATCH /api/admin/configs/{id}.
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing config id")
		return
	}

	var body updateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate embedding model dimensions if provided.
	for _, m := range body.EmbeddingModels {
		if m.Dimensions < 0 {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "embedding model dimensions must be >= 0")
			return
		}
	}

	input := UpdateAIConfigInput{
		Name:            body.Name,
		APIKey:          body.APIKey,
		BaseURL:         body.BaseURL,
		ChatModels:      body.ChatModels,
		EmbeddingModels: body.EmbeddingModels,
		ReasoningModels: body.ReasoningModels,
		RerankModels:    body.RerankModels,
		TtsModels:       body.TtsModels,
		SttModels:       body.SttModels,
	}

	config, err := h.store.UpdateAIConfig(ctx, id, input)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "config not found")
		} else {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update config")
		}
		return
	}
	if config == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "config not found")
		return
	}

	_ = h.store.LogAuditAction(ctx, operatorID(r), "config.update", "config", id, map[string]any{"name": config.Name})
	h.notifyConfigChange()

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, config)
}

// DeleteConfig handles DELETE /api/admin/configs/{id}.
func (h *Handler) DeleteConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing config id")
		return
	}

	if err := h.store.DeleteAIConfig(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "config not found")
		} else {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete config")
		}
		return
	}

	_ = h.store.LogAuditAction(ctx, operatorID(r), "config.delete", "config", id, nil)
	h.notifyConfigChange()

	w.WriteHeader(http.StatusNoContent)
}

// ActivateConfig handles POST /api/admin/configs/{id}/activate.
func (h *Handler) ActivateConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing config id")
		return
	}

	if err := h.store.ActivateAIConfig(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "config not found")
		} else {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to activate config")
		}
		return
	}

	_ = h.store.LogAuditAction(ctx, operatorID(r), "config.activate", "config", id, nil)
	h.notifyConfigChange()

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]bool{"success": true})
}

// TestConfig handles POST /api/admin/configs/{id}/test.
// It looks up the provider by ID (with the real, unmasked API key), creates a
// temporary AI client, and calls the models endpoint to verify connectivity.
func (h *Handler) TestConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing config id")
		return
	}

	provider, err := h.store.GetAIProviderByID(ctx, id)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch config")
		return
	}
	if provider == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "config not found")
		return
	}

	client := ai.NewClient(provider.BaseURL, provider.APIKey)

	start := time.Now()
	listErr := client.ListModels(ctx)
	latencyMs := time.Since(start).Milliseconds()

	if listErr != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
			"status":    "unhealthy",
			"latencyMs": latencyMs,
			"error":     listErr.Error(),
		})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{
		"status":    "healthy",
		"latencyMs": latencyMs,
	})
}
