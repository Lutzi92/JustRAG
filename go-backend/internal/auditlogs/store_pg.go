package auditlogs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the auditlogs Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// auditLogRow is an internal struct with db tags for scanning admin_audit_logs.
type auditLogRow struct {
	ID         string    `db:"id"`
	OperatorID *string   `db:"operator_id"`
	Action     string    `db:"action"`
	TargetType string    `db:"target_type"`
	TargetID   *string   `db:"target_id"`
	Diff       any       `db:"diff"`
	CreatedAt  time.Time `db:"created_at"`
}

// GetAuditLogs returns audit log entries from admin_audit_logs filtered by the
// provided AuditLogFilters, ordered by created_at DESC.
func (s *PGStore) GetAuditLogs(ctx context.Context, f AuditLogFilters) ([]AuditLogRow, error) {
	base := `SELECT id, operator_id, action, target_type, target_id, diff, created_at
FROM admin_audit_logs
WHERE 1=1`

	b := pgxutil.NewClauseBuilder(0)

	if f.OperatorID != "" {
		b.Add("operator_id = $%d", f.OperatorID)
	}
	if f.Action != "" {
		b.Add("action = $%d", f.Action)
	}
	if f.TargetType != "" {
		b.Add("target_type = $%d", f.TargetType)
	}
	if f.From != nil {
		b.Add("created_at >= $%d", *f.From)
	}
	if f.To != nil {
		b.Add("created_at <= $%d", *f.To)
	}
	if b.Len() > 0 {
		base += " AND " + strings.Join(b.Clauses(), " AND ")
	}

	limitParam := b.Bind(f.Limit)
	offsetParam := b.Bind(f.Offset)
	base += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", limitParam, offsetParam)

	rows, err := pgxutil.QueryRows[auditLogRow](ctx, s.pool, base, b.Args()...)
	if err != nil {
		return nil, err
	}

	result := make([]AuditLogRow, len(rows))
	for i, r := range rows {
		result[i] = AuditLogRow(r)
	}
	return result, nil
}
