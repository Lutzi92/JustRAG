package ai

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of ai.ConfigStore.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ ConfigStore = (*PGStore)(nil)

// ---------------------------------------------------------------------------
// Internal scan structs
// ---------------------------------------------------------------------------

// aiProviderInfoRow is an internal struct for scanning ai_providers with api_key/base_url.
type aiProviderInfoRow struct {
	ID      string  `db:"id"`
	Name    string  `db:"name"`
	APIKey  string  `db:"api_key"`
	BaseURL *string `db:"base_url"`
}

// aiModelInfoRow is an internal struct for scanning ai_models classification columns.
type aiModelInfoRow struct {
	Name        string `db:"name"`
	IsReasoning bool   `db:"is_reasoning"`
	IsEmbedding bool   `db:"is_embedding"`
	IsRerank    bool   `db:"is_rerank"`
	IsTts       bool   `db:"is_tts"`
	IsStt       bool   `db:"is_stt"`
}

// kbModelOverridesRow is an internal struct for scanning model-override columns on knowledge_bases.
type kbModelOverridesRow struct {
	AIConfigID     *string `db:"ai_config_id"`
	ChatModel      *string `db:"chat_model"`
	EmbeddingModel *string `db:"embedding_model"`
	RerankModel    *string `db:"rerank_model"`
	TtsModel       *string `db:"tts_model"`
	SttModel       *string `db:"stt_model"`
}

// derefBaseURL returns the string value of a *string base_url, or empty string when nil.
func derefBaseURL(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ---------------------------------------------------------------------------
// ConfigStore implementation
// ---------------------------------------------------------------------------

// GetActiveAIProvider returns the provider whose is_active flag is true.
// Returns nil, nil when no active provider exists.
func (s *PGStore) GetActiveAIProvider(ctx context.Context) (*AIProviderInfo, error) {
	r, err := pgxutil.QueryOne[aiProviderInfoRow](ctx, s.pool,
		`SELECT id, name, api_key, base_url FROM ai_providers WHERE is_active = true`)
	if err != nil {
		return nil, fmt.Errorf("GetActiveAIProvider: %w", err)
	}
	if r == nil {
		return nil, nil
	}
	return &AIProviderInfo{
		ID:      r.ID,
		Name:    r.Name,
		APIKey:  r.APIKey,
		BaseURL: derefBaseURL(r.BaseURL),
	}, nil
}

// GetAIProviderByID returns the provider with the given ID.
// Returns nil, nil when the provider does not exist.
func (s *PGStore) GetAIProviderByID(ctx context.Context, id string) (*AIProviderInfo, error) {
	r, err := pgxutil.QueryOne[aiProviderInfoRow](ctx, s.pool,
		`SELECT id, name, api_key, base_url FROM ai_providers WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("GetAIProviderByID: %w", err)
	}
	if r == nil {
		return nil, nil
	}
	return &AIProviderInfo{
		ID:      r.ID,
		Name:    r.Name,
		APIKey:  r.APIKey,
		BaseURL: derefBaseURL(r.BaseURL),
	}, nil
}

// GetAIModelsByProvider returns all models belonging to providerID.
func (s *PGStore) GetAIModelsByProvider(ctx context.Context, providerID string) ([]AIModelInfo, error) {
	rows, err := pgxutil.QueryRows[aiModelInfoRow](ctx, s.pool,
		`SELECT name, is_reasoning, is_embedding, is_rerank, is_tts, is_stt
		 FROM ai_models WHERE provider_id = $1`, providerID)
	if err != nil {
		return nil, fmt.Errorf("GetAIModelsByProvider: %w", err)
	}
	result := make([]AIModelInfo, len(rows))
	for i, r := range rows {
		result[i] = AIModelInfo{
			Name:        r.Name,
			IsReasoning: r.IsReasoning,
			IsEmbedding: r.IsEmbedding,
			IsRerank:    r.IsRerank,
			IsTts:       r.IsTts,
			IsStt:       r.IsStt,
		}
	}
	return result, nil
}

// GetKBModelOverrides returns the AI-related override columns for the KB with kbID.
// Returns nil, nil when the KB does not exist.
func (s *PGStore) GetKBModelOverrides(ctx context.Context, kbID string) (*KBModelOverrides, error) {
	r, err := pgxutil.QueryOne[kbModelOverridesRow](ctx, s.pool,
		`SELECT ai_config_id, chat_model, embedding_model, rerank_model, tts_model, stt_model
		 FROM knowledge_bases WHERE id = $1`, kbID)
	if err != nil {
		return nil, fmt.Errorf("GetKBModelOverrides: %w", err)
	}
	if r == nil {
		return nil, nil
	}
	return &KBModelOverrides{
		AIConfigID:     r.AIConfigID,
		ChatModel:      r.ChatModel,
		EmbeddingModel: r.EmbeddingModel,
		RerankModel:    r.RerankModel,
		TtsModel:       r.TtsModel,
		SttModel:       r.SttModel,
	}, nil
}
