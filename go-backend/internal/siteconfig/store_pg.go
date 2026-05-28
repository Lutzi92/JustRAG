package siteconfig

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// siteConfigValueRow is a single-column projection used by GetSiteConfigValue.
type siteConfigValueRow struct {
	Value *string `db:"value"`
}

// GetSiteConfigValue returns the value for a single site_configs key, or nil
// if the key does not exist.
func (s *PGStore) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	const sql = `SELECT value FROM site_configs WHERE key = $1`

	row, err := pgxutil.QueryOne[siteConfigValueRow](ctx, s.pool, sql, key)
	if err != nil {
		return nil, fmt.Errorf("GetSiteConfigValue: %w", err)
	}
	if row == nil {
		return nil, nil
	}
	return row.Value, nil
}

// PGStore is a PostgreSQL-backed implementation of the siteconfig Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// siteConfigRow is an internal struct with db tags used for pgx.RowToStructByName.
type siteConfigRow struct {
	Key   string  `db:"key"`
	Value *string `db:"value"`
}

// GetAllSiteConfigs returns all rows from the site_configs table.
func (s *PGStore) GetAllSiteConfigs(ctx context.Context) ([]SiteConfigRow, error) {
	const sql = `SELECT key, value FROM site_configs ORDER BY key`

	rows, err := pgxutil.QueryRows[siteConfigRow](ctx, s.pool, sql)
	if err != nil {
		return nil, err
	}

	result := make([]SiteConfigRow, len(rows))
	for i, r := range rows {
		result[i] = SiteConfigRow{Key: r.Key, Value: r.Value}
	}
	return result, nil
}

// UpdateSiteConfigsBatch upserts the given KeyValue pairs in a single round-trip
// using a multi-row VALUES upsert. Atomicity is provided by the implicit
// statement-level transaction; no explicit Begin/Commit is needed.
func (s *PGStore) UpdateSiteConfigsBatch(ctx context.Context, configs []KeyValue) ([]SiteConfigRow, error) {
	if len(configs) == 0 {
		return []SiteConfigRow{}, nil
	}

	var sb strings.Builder
	sb.WriteString(`INSERT INTO site_configs (key, value, updated_at) VALUES `)
	args := make([]any, 0, len(configs)*2)
	for i, kv := range configs {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "($%d, $%d, NOW())", i*2+1, i*2+2)
		args = append(args, kv.Key, kv.Value)
	}
	sb.WriteString(`
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
		RETURNING key, value`)

	rows, err := pgxutil.QueryRows[siteConfigRow](ctx, s.pool, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("UpdateSiteConfigsBatch: %w", err)
	}
	result := make([]SiteConfigRow, len(rows))
	for i, r := range rows {
		result[i] = SiteConfigRow{Key: r.Key, Value: r.Value}
	}
	return result, nil
}

// UpdateSiteConfig upserts a single key/value pair into site_configs.
func (s *PGStore) UpdateSiteConfig(ctx context.Context, key, value string) error {
	const upsertSQL = `
		INSERT INTO site_configs (key, value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`

	_, err := s.pool.Exec(ctx, upsertSQL, key, value)
	return err
}
