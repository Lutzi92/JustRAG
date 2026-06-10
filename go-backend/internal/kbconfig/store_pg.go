// Package kbconfig persists and serves per-KB site_config overrides
// (kb_site_configs). The set of overridable keys is governed by
// internal/siteconfig's registry; this package does not re-validate membership
// on read, only on write (via the HTTP handler).
package kbconfig

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// Store is the Postgres-backed kb_site_configs accessor.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type kvRow struct {
	Key   string  `db:"key"`
	Value *string `db:"value"`
}

// ListKBOverrides returns the KB's overrides as key → value (value may be nil).
// An empty map (KB has no overrides) is the common case and returns fast.
func (s *Store) ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error) {
	const sql = `SELECT key, value FROM kb_site_configs WHERE kb_id = $1`
	rows, err := pgxutil.QueryRows[kvRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return nil, fmt.Errorf("ListKBOverrides: %w", err)
	}
	out := make(map[string]*string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

// UpsertBatch writes the given key/value pairs for kbID in a single statement.
// A nil value stores SQL NULL. No-op for an empty map.
func (s *Store) UpsertBatch(ctx context.Context, kbID string, kv map[string]*string) error {
	if len(kv) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO kb_site_configs (kb_id, key, value, updated_at) VALUES `)
	args := make([]any, 0, len(kv)*2+1) // 2 placeholders (key,value) per row + $1 kb_id
	args = append(args, kbID)           // $1 reused for kb_id in every tuple
	i := 0
	for k, v := range kv {
		if i > 0 {
			sb.WriteString(", ")
		}
		// $1 = kb_id; key/value get fresh placeholders.
		fmt.Fprintf(&sb, "($1, $%d, $%d, NOW())", len(args)+1, len(args)+2)
		args = append(args, k, v)
		i++
	}
	sb.WriteString(` ON CONFLICT (kb_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`)
	if _, err := s.pool.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("UpsertBatch: %w", err)
	}
	return nil
}

// DeleteKey removes one override, resetting the key to its global value.
// Returns true when a row was deleted.
func (s *Store) DeleteKey(ctx context.Context, kbID, key string) (bool, error) {
	const sql = `DELETE FROM kb_site_configs WHERE kb_id = $1 AND key = $2`
	tag, err := s.pool.Exec(ctx, sql, kbID, key)
	if err != nil {
		return false, fmt.Errorf("DeleteKey: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
