package publicconfigs

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the publicconfigs Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// aiProviderRow is an internal struct with db tags for scanning ai_providers.
type aiProviderRow struct {
	ID       string `db:"id"`
	Name     string `db:"name"`
	Provider string `db:"provider"`
	IsActive bool   `db:"is_active"`
}

// aiModelRow is an internal struct with db tags for scanning ai_models.
type aiModelRow struct {
	ID          string `db:"id"`
	ProviderID  string `db:"provider_id"`
	Name        string `db:"name"`
	IsReasoning bool   `db:"is_reasoning"`
	IsEmbedding bool   `db:"is_embedding"`
	IsRerank    bool   `db:"is_rerank"`
	IsTts       bool   `db:"is_tts"`
	IsStt       bool   `db:"is_stt"`
}

// GetAIProviders returns all AI providers.
func (s *PGStore) GetAIProviders(ctx context.Context) ([]AIProvider, error) {
	const sql = `SELECT id, name, provider, is_active FROM ai_providers ORDER BY name`

	rows, err := pgxutil.QueryRows[aiProviderRow](ctx, s.pool, sql)
	if err != nil {
		return nil, err
	}

	result := make([]AIProvider, len(rows))
	for i, r := range rows {
		result[i] = AIProvider{
			ID:       r.ID,
			Name:     r.Name,
			Provider: r.Provider,
			IsActive: r.IsActive,
		}
	}
	return result, nil
}

// GetAIModelsByProviderIDs returns all models belonging to the given provider IDs.
func (s *PGStore) GetAIModelsByProviderIDs(ctx context.Context, ids []string) ([]AIModel, error) {
	if len(ids) == 0 {
		return []AIModel{}, nil
	}

	const sql = `
		SELECT id, provider_id, name, is_reasoning, is_embedding, is_rerank, is_tts, is_stt
		FROM ai_models
		WHERE provider_id = ANY($1)
		ORDER BY name`

	rows, err := pgxutil.QueryRows[aiModelRow](ctx, s.pool, sql, ids)
	if err != nil {
		return nil, err
	}

	result := make([]AIModel, len(rows))
	for i, r := range rows {
		result[i] = AIModel{
			ID:          r.ID,
			ProviderID:  r.ProviderID,
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
