package adminproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/store"
)

// PGStore is a PostgreSQL-backed implementation of the adminproviders Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// authProviderDBRow is an internal struct with db tags for scanning auth_providers rows.
type authProviderDBRow struct {
	ID        string          `db:"id"`
	Type      string          `db:"type"`
	Name      string          `db:"name"`
	Config    json.RawMessage `db:"config"`
	IsActive  bool            `db:"is_active"`
	CreatedAt time.Time       `db:"created_at"`
}

func toAuthProviderRow(r authProviderDBRow) AuthProviderRow {
	return AuthProviderRow(r)
}

const authProviderSelectCols = `id, type, name, config, is_active, created_at`

// ListAuthProviders returns all auth providers ordered by created_at DESC.
func (s *PGStore) ListAuthProviders(ctx context.Context) ([]AuthProviderRow, error) {
	sql := `SELECT ` + authProviderSelectCols + ` FROM auth_providers ORDER BY created_at DESC`

	rows, err := pgxutil.QueryRows[authProviderDBRow](ctx, s.pool, sql)
	if err != nil {
		return nil, err
	}

	result := make([]AuthProviderRow, len(rows))
	for i, r := range rows {
		result[i] = toAuthProviderRow(r)
	}
	return result, nil
}

// GetAuthProvider returns the auth provider with the given ID, or nil if none
// exists. The config is returned verbatim (sensitive fields still encrypted) —
// callers that hand it to clients must mask it first.
func (s *PGStore) GetAuthProvider(ctx context.Context, id string) (*AuthProviderRow, error) {
	rows, err := pgxutil.QueryRows[authProviderDBRow](ctx, s.pool,
		`SELECT `+authProviderSelectCols+` FROM auth_providers WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toAuthProviderRow(rows[0])
	return &r, nil
}

// CreateAuthProvider inserts a new auth provider and returns the stored row.
// If data.IsActive is nil, the DB default (true) is used.
func (s *PGStore) CreateAuthProvider(ctx context.Context, data AuthProviderCreate) (*AuthProviderRow, error) {
	isActive := true
	if data.IsActive != nil {
		isActive = *data.IsActive
	}

	const sql = `
		INSERT INTO auth_providers (type, name, config, is_active)
		VALUES ($1, $2, $3, $4)
		RETURNING ` + authProviderSelectCols

	rows, err := pgxutil.QueryRows[authProviderDBRow](ctx, s.pool, sql,
		data.Type, data.Name, data.Config, isActive)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("create auth provider: no row returned")
	}
	r := toAuthProviderRow(rows[0])
	return &r, nil
}

// UpdateAuthProvider applies the non-nil fields in data to the auth provider with the given ID
// and returns the updated row. Returns nil if no row with that ID exists.
func (s *PGStore) UpdateAuthProvider(ctx context.Context, id string, data AuthProviderUpdate) (*AuthProviderRow, error) {
	// base=1: $1 is reserved for the WHERE id clause, so the first SET
	// assignment binds $2.
	b := pgxutil.NewClauseBuilder(1)

	if data.Type != nil {
		b.Add("type = $%d", *data.Type)
	}
	if data.Name != nil {
		b.Add("name = $%d", *data.Name)
	}
	if data.Config != nil {
		b.Add("config = $%d", *data.Config)
	}
	if data.IsActive != nil {
		b.Add("is_active = $%d", *data.IsActive)
	}

	if b.Len() == 0 {
		// Nothing to update — return the current row unchanged.
		rows, err := pgxutil.QueryRows[authProviderDBRow](ctx, s.pool,
			`SELECT `+authProviderSelectCols+` FROM auth_providers WHERE id = $1`, id)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		r := toAuthProviderRow(rows[0])
		return &r, nil
	}

	updateSQL := fmt.Sprintf(
		`UPDATE auth_providers SET %s WHERE id = $1 RETURNING `+authProviderSelectCols,
		strings.Join(b.Clauses(), ", "),
	)
	// Prepend id as $1
	allArgs := append([]any{id}, b.Args()...)

	rows, err := pgxutil.QueryRows[authProviderDBRow](ctx, s.pool, updateSQL, allArgs...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toAuthProviderRow(rows[0])
	return &r, nil
}

// HasActiveOIDCProviderOtherThan reports whether any other auth_providers row
// with type='oidc' and is_active=true exists, excluding the row with id =
// excludeID. Passing an empty excludeID checks for *any* active OIDC row.
//
// Used by the handler to enforce the single-active OIDC invariant before a
// create/update lands.
func (s *PGStore) HasActiveOIDCProviderOtherThan(ctx context.Context, excludeID string) (bool, error) {
	const sql = `SELECT 1 FROM auth_providers
	             WHERE type = 'oidc' AND is_active = true AND ($1 = '' OR id::text <> $1)
	             LIMIT 1`
	rows, err := pgxutil.QueryRows[struct {
		Exists int `db:"?column?"`
	}](ctx, s.pool, sql, excludeID)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// DeleteAuthProvider removes the auth provider with the given ID.
// Returns an error wrapping "not found" if no row was deleted.
func (s *PGStore) DeleteAuthProvider(ctx context.Context, id string) error {
	rows, err := pgxutil.QueryRows[struct {
		ID string `db:"id"`
	}](ctx, s.pool, `DELETE FROM auth_providers WHERE id = $1 RETURNING id`, id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("auth_provider %s: %w", id, store.ErrNotFound)
	}
	return nil
}

// LogAuditAction inserts a row into admin_audit_logs. Mirrors the
// adminconfigs / adminusers implementations.
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
