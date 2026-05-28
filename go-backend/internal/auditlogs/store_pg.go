package auditlogs

import (
	"context"
	"fmt"
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

	var args []any
	param := 1

	if f.OperatorID != "" {
		base += fmt.Sprintf(" AND operator_id = $%d", param)
		args = append(args, f.OperatorID)
		param++
	}
	if f.Action != "" {
		base += fmt.Sprintf(" AND action = $%d", param)
		args = append(args, f.Action)
		param++
	}
	if f.TargetType != "" {
		base += fmt.Sprintf(" AND target_type = $%d", param)
		args = append(args, f.TargetType)
		param++
	}
	if f.From != nil {
		base += fmt.Sprintf(" AND created_at >= $%d", param)
		args = append(args, *f.From)
		param++
	}
	if f.To != nil {
		base += fmt.Sprintf(" AND created_at <= $%d", param)
		args = append(args, *f.To)
		param++
	}

	base += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", param, param+1)
	args = append(args, f.Limit, f.Offset)

	rows, err := pgxutil.QueryRows[auditLogRow](ctx, s.pool, base, args...)
	if err != nil {
		return nil, err
	}

	result := make([]AuditLogRow, len(rows))
	for i, r := range rows {
		result[i] = AuditLogRow{
			ID:         r.ID,
			OperatorID: r.OperatorID,
			Action:     r.Action,
			TargetType: r.TargetType,
			TargetID:   r.TargetID,
			Diff:       r.Diff,
			CreatedAt:  r.CreatedAt,
		}
	}
	return result, nil
}
