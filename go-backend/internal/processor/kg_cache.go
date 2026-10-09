package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/prompts"
)

// kgCache stores per-chunk KG extractions of a library file (P2-R5), keyed by
// the chunk's content hash, the effective extraction model, the prompt
// version and the KB language. It lets a copied index rebuild its KB's graph
// without LLM calls.
type kgCache interface {
	Get(ctx context.Context, userFileID, contentHash, model, lang string) (*ai.KGExtraction, bool, error)
	Put(ctx context.Context, userFileID, contentHash, model, lang string, ext ai.KGExtraction) error
}

type pgKGCache struct{ pool *pgxpool.Pool }

func newPGKGCache(main *pgxpool.Pool) kgCache { return &pgKGCache{pool: main} }

func (c *pgKGCache) Get(ctx context.Context, userFileID, contentHash, model, lang string) (*ai.KGExtraction, bool, error) {
	var raw []byte
	err := c.pool.QueryRow(ctx, `
		SELECT extraction FROM kg_extraction_cache
		WHERE user_file_id = $1::uuid AND content_hash = $2 AND model = $3
		  AND prompt_version = $4 AND lang = $5`,
		userFileID, contentHash, model, prompts.KGPromptVersion, lang).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("kg cache get: %w", err)
	}
	var ext ai.KGExtraction
	if err := json.Unmarshal(raw, &ext); err != nil {
		return nil, false, fmt.Errorf("kg cache decode: %w", err)
	}
	return &ext, true, nil
}

func (c *pgKGCache) Put(ctx context.Context, userFileID, contentHash, model, lang string, ext ai.KGExtraction) error {
	raw, err := json.Marshal(ext)
	if err != nil {
		return fmt.Errorf("kg cache encode: %w", err)
	}
	if _, err := c.pool.Exec(ctx, `
		INSERT INTO kg_extraction_cache (user_file_id, content_hash, model, prompt_version, lang, extraction)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::jsonb)
		ON CONFLICT (user_file_id, content_hash, model, prompt_version, lang)
		DO UPDATE SET extraction = EXCLUDED.extraction`,
		userFileID, contentHash, model, prompts.KGPromptVersion, lang, raw); err != nil {
		return fmt.Errorf("kg cache put: %w", err)
	}
	return nil
}
