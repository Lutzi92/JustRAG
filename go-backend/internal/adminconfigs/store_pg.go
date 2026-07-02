package adminconfigs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/store"
)

// PGStore is a PostgreSQL-backed implementation of the adminconfigs Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// ---------------------------------------------------------------------------
// Scan structs & helpers
// ---------------------------------------------------------------------------

// aiConfigProviderRow is an internal struct for scanning ai_providers with api_key and base_url.
type aiConfigProviderRow struct {
	ID        string    `db:"id"`
	Name      string    `db:"name"`
	Provider  string    `db:"provider"`
	APIKey    string    `db:"api_key"`
	BaseURL   *string   `db:"base_url"`
	IsActive  bool      `db:"is_active"`
	CreatedAt time.Time `db:"created_at"`
}

// aiConfigModelRow is an internal struct for scanning ai_models with all boolean flags and dimensions.
type aiConfigModelRow struct {
	ID          string `db:"id"`
	ProviderID  string `db:"provider_id"`
	Name        string `db:"name"`
	IsReasoning bool   `db:"is_reasoning"`
	IsEmbedding bool   `db:"is_embedding"`
	IsRerank    bool   `db:"is_rerank"`
	IsTts       bool   `db:"is_tts"`
	IsStt       bool   `db:"is_stt"`
	Dimensions  int    `db:"dimensions"`
}

// buildAIConfigResponse assembles an AIConfigResponse from a provider row and its model rows.
// The models slice must already be filtered to only contain models for this provider.
//
// Reasoning models are conversational chat models with an extra IsReasoning flag,
// matching the contract used by ai/config.go and publicconfigs/handler.go: the
// model appears in chat_models (with IsReasoning=true) AND is duplicated in
// reasoning_models for legacy API consumers that key off the split.
func buildAIConfigResponse(p aiConfigProviderRow, models []aiConfigModelRow) AIConfigResponse {
	chat := []AIModelRow{}
	embedding := []AIModelRow{}
	reasoning := []AIModelRow{}
	rerank := []AIModelRow{}
	tts := []AIModelRow{}
	stt := []AIModelRow{}

	for _, m := range models {
		row := AIModelRow{
			ID:          m.ID,
			Name:        m.Name,
			IsReasoning: m.IsReasoning,
			IsEmbedding: m.IsEmbedding,
			IsRerank:    m.IsRerank,
			IsTts:       m.IsTts,
			IsStt:       m.IsStt,
			Dimensions:  m.Dimensions,
		}
		switch {
		case m.IsEmbedding:
			embedding = append(embedding, row)
		case m.IsRerank:
			rerank = append(rerank, row)
		case m.IsTts:
			tts = append(tts, row)
		case m.IsStt:
			stt = append(stt, row)
		default:
			chat = append(chat, row)
			if m.IsReasoning {
				reasoning = append(reasoning, row)
			}
		}
	}

	return AIConfigResponse{
		ID:              p.ID,
		Name:            p.Name,
		Provider:        p.Provider,
		APIKey:          maskAPIKey(p.APIKey),
		BaseURL:         p.BaseURL,
		ChatModels:      chat,
		EmbeddingModels: embedding,
		ReasoningModels: reasoning,
		RerankModels:    rerank,
		TtsModels:       tts,
		SttModels:       stt,
		IsActive:        p.IsActive,
		CreatedAt:       p.CreatedAt,
	}
}

// fetchModelsForProviders retrieves all ai_models for the given provider IDs.
//
// Accepts a Querier (rather than using s.pool directly) so callers inside a
// transaction can fetch models via the tx — this is what lets Create/Update
// build their response from inside-tx data and avoid the post-commit race
// where a concurrent Delete would surface as (nil, nil) to the caller.
func fetchModelsForProviders(ctx context.Context, q pgxutil.Querier, ids []string) ([]aiConfigModelRow, error) {
	if len(ids) == 0 {
		return []aiConfigModelRow{}, nil
	}
	return pgxutil.QueryRows[aiConfigModelRow](ctx, q,
		`SELECT id, provider_id, name, is_reasoning, is_embedding, is_rerank, is_tts, is_stt, dimensions
		 FROM ai_models WHERE provider_id = ANY($1) ORDER BY name`, ids)
}

// aiModelInsert is the row-level representation used by both Create and
// Update to batch-insert ai_models in a single round-trip. Per-category
// transforms (which boolean flag to set, default dimensions) happen at the
// call site so this type carries no policy.
type aiModelInsert struct {
	name        string
	isReasoning bool
	isEmbedding bool
	isRerank    bool
	isTts       bool
	isStt       bool
	dims        int
}

// batchInsertModels executes a single multi-row INSERT for all models, or a
// no-op when models is empty. One round-trip regardless of model count, so
// neither Create nor Update pays the per-model network round-trip the
// previous Update path did.
func batchInsertModels(ctx context.Context, tx pgx.Tx, providerID string, models []aiModelInsert) error {
	if len(models) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO ai_models (provider_id, name, is_reasoning, is_embedding, is_rerank, is_tts, is_stt, dimensions) VALUES `)
	args := make([]any, 0, len(models)*8)
	for i, m := range models {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * 8
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8)
		args = append(args, providerID, m.name, m.isReasoning, m.isEmbedding, m.isRerank, m.isTts, m.isStt, m.dims)
	}
	_, err := tx.Exec(ctx, sb.String(), args...)
	return err
}

// getAIConfigByID is the shared single-config read used by both the public
// GetAIConfigByID (against s.pool) and the in-transaction response build in
// Create/UpdateAIConfig. Using a Querier means in-tx callers see their own
// uncommitted writes and avoid the post-commit race where a concurrent
// DeleteAIConfig between COMMIT and a fresh SELECT would return (nil, nil).
func getAIConfigByID(ctx context.Context, q pgxutil.Querier, id string) (*AIConfigResponse, error) {
	provider, err := pgxutil.QueryOne[aiConfigProviderRow](ctx, q,
		`SELECT id, name, provider, api_key, base_url, is_active, created_at
		 FROM ai_providers WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("GetAIConfigByID: %w", err)
	}
	if provider == nil {
		return nil, nil
	}
	models, err := fetchModelsForProviders(ctx, q, []string{id})
	if err != nil {
		return nil, fmt.Errorf("GetAIConfigByID models: %w", err)
	}
	resp := buildAIConfigResponse(*provider, models)
	return &resp, nil
}

// ---------------------------------------------------------------------------
// CRUD methods
// ---------------------------------------------------------------------------

// ListAIConfigs returns all AI provider configs with masked API keys.
func (s *PGStore) ListAIConfigs(ctx context.Context) ([]AIConfigResponse, error) {
	providers, err := pgxutil.QueryRows[aiConfigProviderRow](ctx, s.pool,
		`SELECT id, name, provider, api_key, base_url, is_active, created_at
		 FROM ai_providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("ListAIConfigs providers: %w", err)
	}

	ids := make([]string, len(providers))
	for i, p := range providers {
		ids[i] = p.ID
	}

	allModels, err := fetchModelsForProviders(ctx, s.pool, ids)
	if err != nil {
		return nil, fmt.Errorf("ListAIConfigs models: %w", err)
	}

	// Group models by provider ID for O(1) lookup instead of O(N*M) iteration.
	modelsByProvider := make(map[string][]aiConfigModelRow, len(ids))
	for _, m := range allModels {
		modelsByProvider[m.ProviderID] = append(modelsByProvider[m.ProviderID], m)
	}

	result := make([]AIConfigResponse, len(providers))
	for i, p := range providers {
		result[i] = buildAIConfigResponse(p, modelsByProvider[p.ID])
	}
	return result, nil
}

// GetAIConfigByID returns a single AI config with masked API key, or nil if not found.
func (s *PGStore) GetAIConfigByID(ctx context.Context, id string) (*AIConfigResponse, error) {
	return getAIConfigByID(ctx, s.pool, id)
}

// CreateAIConfig inserts a new AI provider and its models transactionally.
func (s *PGStore) CreateAIConfig(ctx context.Context, input CreateAIConfigInput) (*AIConfigResponse, error) {
	var resp *AIConfigResponse
	err := pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var providerID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO ai_providers (name, provider, api_key, base_url)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id`,
			input.Name, input.Provider, input.APIKey, input.BaseURL,
		).Scan(&providerID); err != nil {
			return fmt.Errorf("CreateAIConfig insert provider: %w", err)
		}

		allModels := collectModelInserts(input)
		if err := batchInsertModels(ctx, tx, providerID, allModels); err != nil {
			return fmt.Errorf("CreateAIConfig insert models: %w", err)
		}

		// Build the response from inside the transaction so a concurrent
		// DeleteAIConfig that lands between COMMIT and a fresh SELECT can't
		// surface as (nil, nil) to a caller who just successfully created.
		r, err := getAIConfigByID(ctx, tx, providerID)
		if err != nil {
			return fmt.Errorf("CreateAIConfig load response: %w", err)
		}
		resp = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// collectModelInserts fans the per-category input slices into a single
// flat list ready for batchInsertModels. Empty/whitespace names are
// dropped. Embedding dims persist verbatim with negatives clamped to 0:
// 0 means "auto" (model-native size; the resolver omits the `dimensions`
// request parameter), a positive value requests MRL truncation. Coercing
// unset to a concrete number here would silently activate truncation.
func collectModelInserts(input CreateAIConfigInput) []aiModelInsert {
	var out []aiModelInsert
	for _, m := range input.ChatModels {
		if name := strings.TrimSpace(m.Name); name != "" {
			out = append(out, aiModelInsert{name: name, isReasoning: m.IsReasoning, dims: 0})
		}
	}
	for _, m := range input.ReasoningModels {
		if name := strings.TrimSpace(m.Name); name != "" {
			out = append(out, aiModelInsert{name: name, isReasoning: true, dims: 0})
		}
	}
	for _, m := range input.EmbeddingModels {
		if name := strings.TrimSpace(m.Name); name != "" {
			dims := max(m.Dimensions, 0)
			out = append(out, aiModelInsert{name: name, isEmbedding: true, dims: dims})
		}
	}
	for _, m := range input.RerankModels {
		if name := strings.TrimSpace(m.Name); name != "" {
			out = append(out, aiModelInsert{name: name, isRerank: true, dims: 0})
		}
	}
	for _, m := range input.TtsModels {
		if name := strings.TrimSpace(m.Name); name != "" {
			out = append(out, aiModelInsert{name: name, isTts: true, dims: 0})
		}
	}
	for _, m := range input.SttModels {
		if name := strings.TrimSpace(m.Name); name != "" {
			out = append(out, aiModelInsert{name: name, isStt: true, dims: 0})
		}
	}
	return out
}

// UpdateAIConfig updates an AI provider's fields and replaces provided model categories,
// all within a single transaction.
//
// Uses Serializable isolation because the body is a read-check-write cycle
// (existence check → conditional UPDATE / DELETE+INSERT). At READ COMMITTED a
// concurrent DeleteAIConfig between the EXISTS check and the UPDATE would
// silently no-op the update; Serializable surfaces that race as a
// serialization failure so the caller can retry.
func (s *PGStore) UpdateAIConfig(ctx context.Context, id string, input UpdateAIConfigInput) (*AIConfigResponse, error) {
	var resp *AIConfigResponse
	err := pgxutil.WithSerializableRetry(ctx, s.pool, func(tx pgx.Tx) error {
		// Reset the captured response on every retry so an aborted earlier
		// attempt's partial result cannot leak through if the final retry
		// also errors out before reaching the load step.
		resp = nil
		// Verify the config exists before proceeding with updates.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_providers WHERE id = $1)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("UpdateAIConfig check existence: %w", err)
		}
		if !exists {
			return fmt.Errorf("ai_provider %s: %w", id, store.ErrNotFound)
		}

		// One targeted UPDATE per field. Three round-trips in the same
		// transaction is negligible overhead and removes the dynamic
		// fmt.Sprintf SET-clause builder — any future column added here is
		// just one more block, no shared mutable slice to keep in sync.
		if input.Name != nil {
			if _, err := tx.Exec(ctx, `UPDATE ai_providers SET name = $2 WHERE id = $1`, id, *input.Name); err != nil {
				return fmt.Errorf("UpdateAIConfig update name: %w", err)
			}
		}
		if input.APIKey != nil {
			if _, err := tx.Exec(ctx, `UPDATE ai_providers SET api_key = $2 WHERE id = $1`, id, *input.APIKey); err != nil {
				return fmt.Errorf("UpdateAIConfig update api_key: %w", err)
			}
		}
		if input.BaseURL != nil {
			if _, err := tx.Exec(ctx, `UPDATE ai_providers SET base_url = $2 WHERE id = $1`, id, *input.BaseURL); err != nil {
				return fmt.Errorf("UpdateAIConfig update base_url: %w", err)
			}
		}

		// modelCategory wraps the WHERE clause used to scope a delete-then-insert
		// of one category of models. The unexported field forces every value to be
		// constructed from one of the package-level constants below — a raw string
		// literal cannot reach the SQL builder.
		type modelCategory struct{ whereClause string }
		var (
			// categoryChat covers all conversational chat models, including those
			// flagged as reasoning. is_reasoning is a flag on chat models, not a
			// separate category — see buildAIConfigResponse for the read-side contract.
			categoryChat      = modelCategory{"is_embedding = false AND is_rerank = false AND is_tts = false AND is_stt = false"}
			categoryReasoning = modelCategory{"is_reasoning = true AND is_embedding = false AND is_rerank = false AND is_tts = false AND is_stt = false"}
			categoryEmbedding = modelCategory{"is_embedding = true"}
			categoryRerank    = modelCategory{"is_rerank = true"}
			categoryTts       = modelCategory{"is_tts = true"}
			categoryStt       = modelCategory{"is_stt = true"}
		)

		// replaceModels DELETEs the named category and batch-inserts its
		// replacement in two round-trips total, regardless of the number of
		// models — the previous per-model loop made it N+1 round-trips per
		// category, which was visible on providers with many chat models.
		replaceModels := func(category modelCategory, rows []aiModelInsert) error {
			if _, e := tx.Exec(ctx, `DELETE FROM ai_models WHERE provider_id = $1 AND `+category.whereClause, id); e != nil {
				return e
			}
			return batchInsertModels(ctx, tx, id, rows)
		}

		if input.ChatModels != nil {
			rows := make([]aiModelInsert, 0, len(input.ChatModels))
			for _, m := range input.ChatModels {
				if name := strings.TrimSpace(m.Name); name != "" {
					rows = append(rows, aiModelInsert{name: name, isReasoning: m.IsReasoning, dims: 0})
				}
			}
			if err := replaceModels(categoryChat, rows); err != nil {
				return fmt.Errorf("UpdateAIConfig replace chat models: %w", err)
			}
		}
		// ReasoningModels is the legacy parallel write path. chat_models with
		// isReasoning is the source of truth (buildAIConfigResponse duplicates
		// reasoning rows into both lists on read), so we only honour an explicit
		// ReasoningModels payload when ChatModels was not also provided —
		// otherwise the frontend echoing a stale reasoning_models array would
		// clobber or vanish rows the chat_models block just wrote.
		if input.ReasoningModels != nil && input.ChatModels == nil {
			rows := make([]aiModelInsert, 0, len(input.ReasoningModels))
			for _, m := range input.ReasoningModels {
				if name := strings.TrimSpace(m.Name); name != "" {
					rows = append(rows, aiModelInsert{name: name, isReasoning: true, dims: 0})
				}
			}
			if err := replaceModels(categoryReasoning, rows); err != nil {
				return fmt.Errorf("UpdateAIConfig replace reasoning models: %w", err)
			}
		}
		if input.EmbeddingModels != nil {
			rows := make([]aiModelInsert, 0, len(input.EmbeddingModels))
			for _, m := range input.EmbeddingModels {
				name := strings.TrimSpace(m.Name)
				if name == "" {
					continue
				}
				// 0 = auto (see collectModelInserts); persist verbatim,
				// clamp negatives.
				rows = append(rows, aiModelInsert{name: name, isEmbedding: true, dims: max(m.Dimensions, 0)})
			}
			if err := replaceModels(categoryEmbedding, rows); err != nil {
				return fmt.Errorf("UpdateAIConfig replace embedding models: %w", err)
			}
		}
		if input.RerankModels != nil {
			rows := make([]aiModelInsert, 0, len(input.RerankModels))
			for _, m := range input.RerankModels {
				if name := strings.TrimSpace(m.Name); name != "" {
					rows = append(rows, aiModelInsert{name: name, isRerank: true, dims: 0})
				}
			}
			if err := replaceModels(categoryRerank, rows); err != nil {
				return fmt.Errorf("UpdateAIConfig replace rerank models: %w", err)
			}
		}
		if input.TtsModels != nil {
			rows := make([]aiModelInsert, 0, len(input.TtsModels))
			for _, m := range input.TtsModels {
				if name := strings.TrimSpace(m.Name); name != "" {
					rows = append(rows, aiModelInsert{name: name, isTts: true, dims: 0})
				}
			}
			if err := replaceModels(categoryTts, rows); err != nil {
				return fmt.Errorf("UpdateAIConfig replace tts models: %w", err)
			}
		}
		if input.SttModels != nil {
			rows := make([]aiModelInsert, 0, len(input.SttModels))
			for _, m := range input.SttModels {
				if name := strings.TrimSpace(m.Name); name != "" {
					rows = append(rows, aiModelInsert{name: name, isStt: true, dims: 0})
				}
			}
			if err := replaceModels(categoryStt, rows); err != nil {
				return fmt.Errorf("UpdateAIConfig replace stt models: %w", err)
			}
		}

		// Build the response from inside the transaction (see CreateAIConfig
		// for the same race-avoidance rationale).
		r, err := getAIConfigByID(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("UpdateAIConfig load response: %w", err)
		}
		resp = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// DeleteAIConfig removes the AI provider with the given ID.
// Associated ai_models cascade-delete via the DB FK constraint.
func (s *PGStore) DeleteAIConfig(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM ai_providers WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("ai_provider %s: %w", id, store.ErrNotFound)
	}
	return nil
}

// ActivateAIConfig sets is_active = true for the given provider and false for all others,
// in a single transaction to avoid a split-brain state.
//
// Uses Serializable isolation because the body is a read-check-write cycle
// (existence check → deactivate-all → activate-one). At READ COMMITTED a
// concurrent DeleteAIConfig between the EXISTS check and the second UPDATE
// would silently leave the system with zero active providers; Serializable
// surfaces that race as a serialization failure.
func (s *PGStore) ActivateAIConfig(ctx context.Context, id string) error {
	return pgxutil.WithSerializableRetry(ctx, s.pool, func(tx pgx.Tx) error {
		// Verify the target config exists before deactivating everything.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_providers WHERE id = $1)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("ActivateAIConfig check existence: %w", err)
		}
		if !exists {
			return fmt.Errorf("ai_provider %s: %w", id, store.ErrNotFound)
		}

		if _, err := tx.Exec(ctx, `UPDATE ai_providers SET is_active = false WHERE is_active = true`); err != nil {
			return fmt.Errorf("ActivateAIConfig deactivate all: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE ai_providers SET is_active = true WHERE id = $1`, id); err != nil {
			return fmt.Errorf("ActivateAIConfig activate: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// GetAIProviderByID (required by adminconfigs.Store interface)
// ---------------------------------------------------------------------------

// aiProviderInfoRow is an internal struct for scanning the subset of ai_providers
// columns needed by GetAIProviderByID.
type aiProviderInfoRow struct {
	ID      string  `db:"id"`
	Name    string  `db:"name"`
	APIKey  string  `db:"api_key"`
	BaseURL *string `db:"base_url"`
}

// GetAIProviderByID returns the provider with the given ID.
func (s *PGStore) GetAIProviderByID(ctx context.Context, id string) (*ai.AIProviderInfo, error) {
	r, err := pgxutil.QueryOne[aiProviderInfoRow](ctx, s.pool,
		`SELECT id, name, api_key, base_url FROM ai_providers WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("GetAIProviderByID: %w", err)
	}
	if r == nil {
		return nil, nil
	}
	baseURL := ""
	if r.BaseURL != nil {
		baseURL = *r.BaseURL
	}
	return &ai.AIProviderInfo{
		ID:      r.ID,
		Name:    r.Name,
		APIKey:  r.APIKey,
		BaseURL: baseURL,
	}, nil
}

// ---------------------------------------------------------------------------
// LogAuditAction (required by adminconfigs.Store interface)
// ---------------------------------------------------------------------------

// LogAuditAction inserts a row into admin_audit_logs.
func (s *PGStore) LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error {
	var diffJSON []byte
	if diff != nil {
		var err error
		diffJSON, err = json.Marshal(diff)
		if err != nil {
			return fmt.Errorf("LogAuditAction marshal diff: %w", err)
		}
	}

	const sql = `
		INSERT INTO admin_audit_logs (operator_id, action, target_type, target_id, diff)
		VALUES ($1, $2, $3, $4, $5)`

	if _, err := s.pool.Exec(ctx, sql, operatorID, action, targetType, targetID, diffJSON); err != nil {
		return fmt.Errorf("LogAuditAction exec: %w", err)
	}
	return nil
}
