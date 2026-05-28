package publicconfigs

import (
	"context"
	"net/http"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
)

// AIProvider represents an AI provider configuration (no secrets).
type AIProvider struct {
	ID       string
	Name     string
	Provider string
	IsActive bool
}

// AIModel represents an AI model configuration.
type AIModel struct {
	ID          string
	ProviderID  string
	Name        string
	IsReasoning bool
	IsEmbedding bool
	IsRerank    bool
	IsTts       bool
	IsStt       bool
}

// Store is the data access interface required by this handler.
type Store interface {
	GetAIProviders(ctx context.Context) ([]AIProvider, error)
	GetAIModelsByProviderIDs(ctx context.Context, ids []string) ([]AIModel, error)
}

// providerResponse is the JSON shape returned to the frontend.
type providerResponse struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Provider        string   `json:"provider"`
	IsActive        bool     `json:"is_active"`
	ChatModels      []string `json:"chat_models"`
	EmbeddingModels []string `json:"embedding_models"`
	RerankModels    []string `json:"rerank_models"`
	TtsModels       []string `json:"tts_models"`
	SttModels       []string `json:"stt_models"`
	ReasoningModels []string `json:"reasoning_models"`
}

// Handler handles public config requests.
type Handler struct {
	store Store
}

// NewHandler constructs a Handler with the given store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// GetConfigs handles GET /api/public/configs.
// Requires authentication. Returns AI provider configs with model names only.
func (h *Handler) GetConfigs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "Authentication required")
		return
	}

	providers, err := h.store.GetAIProviders(ctx)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "Failed to fetch providers")
		return
	}

	// Collect provider IDs to fetch models.
	providerIDs := make([]string, len(providers))
	for i, p := range providers {
		providerIDs[i] = p.ID
	}

	var models []AIModel
	if len(providerIDs) > 0 {
		models, err = h.store.GetAIModelsByProviderIDs(ctx, providerIDs)
		if err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "Failed to fetch models")
			return
		}
	}

	// Build a lookup: providerID -> *providerResponse.
	responseMap := make(map[string]*providerResponse, len(providers))
	result := make([]*providerResponse, 0, len(providers))
	for _, p := range providers {
		pr := &providerResponse{
			ID:              p.ID,
			Name:            p.Name,
			Provider:        p.Provider,
			IsActive:        p.IsActive,
			ChatModels:      []string{},
			EmbeddingModels: []string{},
			RerankModels:    []string{},
			TtsModels:       []string{},
			SttModels:       []string{},
			ReasoningModels: []string{},
		}
		responseMap[p.ID] = pr
		result = append(result, pr)
	}

	// Classify models into the appropriate buckets.
	for _, m := range models {
		pr, ok := responseMap[m.ProviderID]
		if !ok {
			continue
		}

		switch {
		case m.IsEmbedding:
			pr.EmbeddingModels = append(pr.EmbeddingModels, m.Name)
		case m.IsRerank:
			pr.RerankModels = append(pr.RerankModels, m.Name)
		case m.IsTts:
			pr.TtsModels = append(pr.TtsModels, m.Name)
		case m.IsStt:
			pr.SttModels = append(pr.SttModels, m.Name)
		default:
			// Includes reasoning models and plain chat models.
			pr.ChatModels = append(pr.ChatModels, m.Name)
			if m.IsReasoning {
				pr.ReasoningModels = append(pr.ReasoningModels, m.Name)
			}
		}
	}

	// Return empty JSON array instead of null when there are no providers.
	if result == nil {
		result = []*providerResponse{}
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, result)
}
