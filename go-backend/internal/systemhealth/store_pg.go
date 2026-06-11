package systemhealth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the MetricsStore interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ MetricsStore = (*PGStore)(nil)

// GetActiveUsersCount returns the number of users seen within the last 15 minutes
// (matching the Node.js ACTIVE_USER_THRESHOLD_MS = 15 * 60 * 1000).
func (s *PGStore) GetActiveUsersCount(ctx context.Context) (int, error) {
	const sql = `SELECT COUNT(*)::int FROM users WHERE last_seen_at >= NOW() - INTERVAL '15 minutes'`
	var count int
	err := s.pool.QueryRow(ctx, sql).Scan(&count)
	return count, err
}

// GetProcessingFilesCount returns the number of files currently being processed.
func (s *PGStore) GetProcessingFilesCount(ctx context.Context) (int, error) {
	const sql = `SELECT COUNT(*)::int FROM files WHERE status = 'processing'`
	var count int
	err := s.pool.QueryRow(ctx, sql).Scan(&count)
	return count, err
}

// GetTotalKBsCount returns the total number of knowledge bases.
func (s *PGStore) GetTotalKBsCount(ctx context.Context) (int, error) {
	const sql = `SELECT COUNT(*)::int FROM knowledge_bases`
	var count int
	err := s.pool.QueryRow(ctx, sql).Scan(&count)
	return count, err
}

// GetTotalFilesCount returns the total number of files across all knowledge bases.
func (s *PGStore) GetTotalFilesCount(ctx context.Context) (int, error) {
	const sql = `SELECT COUNT(*)::int FROM files`
	var count int
	err := s.pool.QueryRow(ctx, sql).Scan(&count)
	return count, err
}

// GetTotalStorageBytes returns the total size of all files in bytes.
func (s *PGStore) GetTotalStorageBytes(ctx context.Context) (int64, error) {
	const sql = `SELECT COALESCE(SUM(size), 0)::bigint FROM files`
	var total int64
	err := s.pool.QueryRow(ctx, sql).Scan(&total)
	return total, err
}

// GetTotalUsersCount returns the total number of registered users.
func (s *PGStore) GetTotalUsersCount(ctx context.Context) (int, error) {
	const sql = `SELECT COUNT(*)::int FROM users`
	var count int
	err := s.pool.QueryRow(ctx, sql).Scan(&count)
	return count, err
}

// historicalPoint is an internal struct with db tags for scanning system_metrics.
type historicalPoint struct {
	Timestamp string  `db:"timestamp"`
	Value     float64 `db:"value"`
}

// GetHistoricalMetrics returns time-series data for the named metric between from and to.
func (s *PGStore) GetHistoricalMetrics(ctx context.Context, metric string, from, to time.Time) ([]HistoricalPoint, error) {
	const sql = `
		SELECT to_char(recorded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS timestamp,
		       metric_value::float8 AS value
		FROM system_metrics
		WHERE metric_name = $1 AND recorded_at >= $2 AND recorded_at <= $3
		ORDER BY recorded_at`

	rows, err := pgxutil.QueryRows[historicalPoint](ctx, s.pool, sql, metric, from, to)
	if err != nil {
		return nil, err
	}

	result := make([]HistoricalPoint, len(rows))
	for i, r := range rows {
		result[i] = HistoricalPoint(r)
	}
	return result, nil
}
