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
	ID          string  `db:"id"`
	UserID      *string `db:"user_id"`
	IsGlobal    bool    `db:"is_global"`
	IsPublished bool    `db:"is_published"`
}

// GetKBByID returns the knowledge base with the given ID, or nil if not found.
func (s *PGStore) GetKBByID(ctx context.Context, id string) (*KnowledgeBase, error) {
	const sql = `SELECT id, user_id, is_global, is_published FROM knowledge_bases WHERE id = $1`

	rows, err := pgxutil.QueryRows[kbRow](ctx, s.pool, sql, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	r := rows[0]
	return &KnowledgeBase{
		ID:          r.ID,
		UserID:      r.UserID,
		IsGlobal:    r.IsGlobal,
		IsPublished: r.IsPublished,
	}, nil
}

// kbRoleRow is an internal struct with db tags for scanning kb_members.
type kbRoleRow struct {
	Role string `db:"role"`
}

// GetKBRole returns the explicit kb_members role for (kbID, userID), or "" when
// the user has no row. Implicit roles (superadmin, systemadmin on a public KB,
// any user on a published public KB) are resolved in the middleware, not here —
// the store answers only what is stored.
func (s *PGStore) GetKBRole(ctx context.Context, kbID, userID string) (string, error) {
	const sql = `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2 LIMIT 1`

	rows, err := pgxutil.QueryRows[kbRoleRow](ctx, s.pool, sql, kbID, userID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Role, nil
}
