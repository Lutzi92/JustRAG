package kbaccess

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the kbaccess KBStore interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ KBStore = (*PGStore)(nil)

// kbRow is an internal struct with db tags for scanning knowledge_bases.
type kbRow struct {
	ID       string  `db:"id"`
	UserID   *string `db:"user_id"`
	IsGlobal bool    `db:"is_global"`
}

// GetKBByID returns the knowledge base with the given ID, or nil if not found.
func (s *PGStore) GetKBByID(ctx context.Context, id string) (*KnowledgeBase, error) {
	const sql = `SELECT id, user_id, is_global FROM knowledge_bases WHERE id = $1`

	rows, err := pgxutil.QueryRows[kbRow](ctx, s.pool, sql, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	r := rows[0]
	return &KnowledgeBase{
		ID:       r.ID,
		UserID:   r.UserID,
		IsGlobal: r.IsGlobal,
	}, nil
}

// kbShareRow is an internal struct with db tags for scanning knowledge_base_shares.
type kbShareRow struct {
	Permission string `db:"permission"`
}

// GetKBShare returns the share record for (kbID, userID), or nil if not found.
func (s *PGStore) GetKBShare(ctx context.Context, kbID, userID string) (*KBShare, error) {
	const sql = `
		SELECT permission
		FROM knowledge_base_shares
		WHERE kb_id = $1 AND user_id = $2
		LIMIT 1`

	rows, err := pgxutil.QueryRows[kbShareRow](ctx, s.pool, sql, kbID, userID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &KBShare{Permission: rows[0].Permission}, nil
}

// IsGlobalKBEditor returns true when (kbID, userID) appears in global_kb_editors.
func (s *PGStore) IsGlobalKBEditor(ctx context.Context, kbID, userID string) (bool, error) {
	const sql = `
		SELECT COUNT(*)::int
		FROM global_kb_editors
		WHERE kb_id = $1 AND user_id = $2`

	var count int
	err := s.pool.QueryRow(ctx, sql, kbID, userID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
