package adminusers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the adminusers Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// adminUserListRow is an internal struct with db tags for scanning admin user list rows.
type adminUserListRow struct {
	ID        string    `db:"id"`
	Username  string    `db:"username"`
	FirstName *string   `db:"first_name"`
	LastName  *string   `db:"last_name"`
	Role      string    `db:"role"`
	CreatedAt time.Time `db:"created_at"`
}

// ListAllUsers returns all users ordered by created_at DESC.
func (s *PGStore) ListAllUsers(ctx context.Context) ([]UserListRow, error) {
	const sql = `SELECT id, username, first_name, last_name, role, created_at FROM users ORDER BY created_at DESC`

	rows, err := pgxutil.QueryRows[adminUserListRow](ctx, s.pool, sql)
	if err != nil {
		return nil, err
	}

	result := make([]UserListRow, len(rows))
	for i, r := range rows {
		result[i] = UserListRow{
			ID:        r.ID,
			Username:  r.Username,
			FirstName: r.FirstName,
			LastName:  r.LastName,
			Role:      r.Role,
			CreatedAt: r.CreatedAt,
		}
	}
	return result, nil
}

// GetUserByID returns the user with the given ID, or nil when no user matches.
func (s *PGStore) GetUserByID(ctx context.Context, id string) (*UserListRow, error) {
	const sql = `SELECT id, username, first_name, last_name, role, created_at FROM users WHERE id = $1`

	row, err := pgxutil.QueryOne[adminUserListRow](ctx, s.pool, sql, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return &UserListRow{
		ID:        row.ID,
		Username:  row.Username,
		FirstName: row.FirstName,
		LastName:  row.LastName,
		Role:      row.Role,
		CreatedAt: row.CreatedAt,
	}, nil
}

// UpdateUserRole sets the role of the user with the given ID and returns the updated row.
// Returns nil if no user with that ID exists.
func (s *PGStore) UpdateUserRole(ctx context.Context, id, role string) (*UserListRow, error) {
	const sql = `
		UPDATE users SET role = $2 WHERE id = $1
		RETURNING id, username, first_name, last_name, role, created_at`

	rows, err := pgxutil.QueryRows[adminUserListRow](ctx, s.pool, sql, id, role)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	return &UserListRow{
		ID:        r.ID,
		Username:  r.Username,
		FirstName: r.FirstName,
		LastName:  r.LastName,
		Role:      r.Role,
		CreatedAt: r.CreatedAt,
	}, nil
}

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

	_, err := s.pool.Exec(ctx, sql, operatorID, action, targetType, targetID, diffJSON)
	return err
}
