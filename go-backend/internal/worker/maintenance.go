package worker

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// maintenanceRestartBackoff is the wait between a panic-driven exit of a
// maintenance loop and its restart. Long enough that a deterministic crash
// (e.g. nil-deref in a DB helper) doesn't spin in a tight loop spamming the
// log, short enough that transient bugs self-heal before operators notice.
const maintenanceRestartBackoff = 30 * time.Second

// MaintenanceConfig holds configuration for periodic maintenance tasks.
type MaintenanceConfig struct {
	MainDB   *pgxpool.Pool
	VectorDB *pgxpool.Pool

	// StuckFileTimeout is how long a file can be in "processing" before being
	// marked as error. Default: 10 minutes.
	StuckFileTimeout time.Duration

	// StuckCheckInterval is how often to check for stuck files. Default: 2 minutes.
	StuckCheckInterval time.Duration

	// OrphanCleanupInterval is how often to clean up orphaned vectors.
	// Default: 30 minutes.
	OrphanCleanupInterval time.Duration

	// MetricsInterval is how often to record system metrics to the DB.
	// Default: 5 minutes.
	MetricsInterval time.Duration

	// MetricsRetention is how long to keep metrics rows before pruning.
	// Default: 90 days.
	MetricsRetention time.Duration
}

// StartMaintenance starts periodic background maintenance tasks (stuck file
// detection and orphaned vector cleanup). Returns a Stop function that
// cancels all loops AND blocks until each goroutine has fully returned —
// callers can therefore close the DB pools immediately after Stop returns
// without racing in-flight queries. Compare with the parallel pattern in
// app/server.go where startSchedulers is wrapped in a sync.WaitGroup for
// the same reason.
func StartMaintenance(ctx context.Context, cfg MaintenanceConfig) (stop func()) {
	if cfg.StuckFileTimeout == 0 {
		cfg.StuckFileTimeout = 10 * time.Minute
	}
	if cfg.StuckCheckInterval == 0 {
		cfg.StuckCheckInterval = 2 * time.Minute
	}
	if cfg.OrphanCleanupInterval == 0 {
		cfg.OrphanCleanupInterval = 30 * time.Minute
	}
	if cfg.MetricsInterval == 0 {
		cfg.MetricsInterval = 5 * time.Minute
	}
	if cfg.MetricsRetention == 0 {
		cfg.MetricsRetention = 90 * 24 * time.Hour // 90 days
	}

	ctx, cancel := context.WithCancel(ctx)

	var wg sync.WaitGroup
	// launch wraps fn in a supervised restart loop: if fn panics, the
	// inner func()'s recover logs the stack and lets the outer loop
	// re-enter fn after a back-off. safego.Go's outer recover catches
	// any panic in the loop machinery itself, but the operational
	// concern (a panic in fn permanently silencing this maintenance
	// task until process restart) is solved here. ctx cancellation
	// terminates the outer loop cleanly without sleeping.
	launch := func(name string, fn func()) {
		wg.Add(1)
		safego.Go(func() {
			defer wg.Done()
			for ctx.Err() == nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							slog.Error("maintenance goroutine panic; will restart",
								"task", name,
								"error", r,
								"stack", string(debug.Stack()),
							)
						}
					}()
					fn()
				}()
				if ctx.Err() != nil {
					return
				}
				// Back-off before restart so a deterministic panic
				// can't spin. Respect context cancellation during
				// the wait so Stop() doesn't have to block on it.
				// Use NewTimer + Stop for consistency with the rest
				// of the codebase (ai/completion.go, database/database.go)
				// and explicit intent over the implicit `time.After` path.
				t := time.NewTimer(maintenanceRestartBackoff)
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
			}
		})
	}

	// Stuck file checker.
	launch("stuck_files", func() {
		// Run once at startup.
		checkStuckFiles(ctx, cfg.MainDB, cfg.StuckFileTimeout)

		ticker := time.NewTicker(cfg.StuckCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkStuckFiles(ctx, cfg.MainDB, cfg.StuckFileTimeout)
			}
		}
	})

	// Orphaned vector cleanup.
	launch("orphan_cleanup", func() {
		// Run once after a short startup delay. NewTimer + Stop is used here
		// for explicit timer cleanup. (Historical note: pre-Go 1.23, time.After
		// leaked because the underlying Timer couldn't be GC'd until natural
		// expiry; 1.23+ fixed that, so time.After is also safe today. The
		// pattern below is kept for clarity and consistency.)
		startupDelay := time.NewTimer(30 * time.Second)
		defer startupDelay.Stop()
		select {
		case <-ctx.Done():
			return
		case <-startupDelay.C:
			cleanupOrphanedVectors(ctx, cfg.MainDB, cfg.VectorDB)
		}

		ticker := time.NewTicker(cfg.OrphanCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupOrphanedVectors(ctx, cfg.MainDB, cfg.VectorDB)
			}
		}
	})

	// Metrics collection (every 5 min by default).
	launch("metrics_snapshot", func() {
		// Initial delay to let the system stabilize.
		startupDelay := time.NewTimer(1 * time.Minute)
		defer startupDelay.Stop()
		select {
		case <-ctx.Done():
			return
		case <-startupDelay.C:
			recordMetricsSnapshot(ctx, cfg.MainDB)
		}

		ticker := time.NewTicker(cfg.MetricsInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recordMetricsSnapshot(ctx, cfg.MainDB)
			}
		}
	})

	// Metrics cleanup (once per day, prune old rows).
	launch("metrics_prune", func() {
		// Run once after startup.
		startupDelay := time.NewTimer(2 * time.Minute)
		defer startupDelay.Stop()
		select {
		case <-ctx.Done():
			return
		case <-startupDelay.C:
			pruneOldMetrics(ctx, cfg.MainDB, cfg.MetricsRetention)
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pruneOldMetrics(ctx, cfg.MainDB, cfg.MetricsRetention)
			}
		}
	})

	slog.Info("maintenance tasks started",
		"stuckCheckInterval", cfg.StuckCheckInterval,
		"stuckFileTimeout", cfg.StuckFileTimeout,
		"orphanCleanupInterval", cfg.OrphanCleanupInterval,
		"metricsInterval", cfg.MetricsInterval,
		"metricsRetention", cfg.MetricsRetention,
	)

	return func() {
		cancel()
		wg.Wait()
	}
}

// checkStuckFiles marks files stuck in "processing" status as "error".
func checkStuckFiles(ctx context.Context, mainDB *pgxpool.Pool, timeout time.Duration) {
	if mainDB == nil {
		return
	}
	cutoff := time.Now().Add(-timeout)

	const sql = `
		UPDATE files SET status = 'error',
		       error_stage   = COALESCE(error_stage, 'timeout'),
		       error_message = COALESCE(error_message, 'Processing timed out')
		WHERE status = 'processing'
		  AND (
		    (progress_updated_at IS NOT NULL AND progress_updated_at < $1)
		    OR
		    (progress_updated_at IS NULL AND created_at < $1)
		  )`

	tag, err := mainDB.Exec(ctx, sql, cutoff)
	if err != nil {
		slog.Error("stuck file check failed", "error", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Warn("marked stuck files as error",
			"count", tag.RowsAffected(),
			"timeoutMinutes", int(timeout.Minutes()),
		)
	}
}

// ---------------------------------------------------------------------------
// Metrics collection & cleanup
// ---------------------------------------------------------------------------

// recordMetricsSnapshot captures live system metrics and stores them in the
// system_metrics table for the historical dashboard.
func recordMetricsSnapshot(ctx context.Context, mainDB *pgxpool.Pool) {
	if mainDB == nil {
		return
	}

	type metricDef struct {
		name string
		sql  string
	}

	metrics := []metricDef{
		{name: "active_users", sql: `SELECT COUNT(*)::float8 FROM users WHERE last_seen_at >= NOW() - INTERVAL '15 minutes'`},
		{name: "total_users", sql: `SELECT COUNT(*)::float8 FROM users`},
		{name: "total_kbs", sql: `SELECT COUNT(*)::float8 FROM knowledge_bases`},
		{name: "total_files", sql: `SELECT COUNT(*)::float8 FROM files`},
		{name: "total_storage_bytes", sql: `SELECT COALESCE(SUM(size), 0)::float8 FROM files`},
		{name: "processing_files", sql: `SELECT COUNT(*)::float8 FROM files WHERE status = 'processing'`},
	}

	const insertSQL = `INSERT INTO system_metrics (metric_name, metric_value, recorded_at) VALUES ($1, $2, NOW())`

	for _, m := range metrics {
		var value float64
		if err := mainDB.QueryRow(ctx, m.sql).Scan(&value); err != nil {
			slog.Error("metrics snapshot: query failed", "metric", m.name, "error", err)
			continue
		}
		if _, err := mainDB.Exec(ctx, insertSQL, m.name, value); err != nil {
			slog.Error("metrics snapshot: insert failed", "metric", m.name, "error", err)
		}
	}

	slog.Debug("metrics snapshot recorded")
}

// pruneOldMetrics deletes system_metrics rows older than the retention period.
func pruneOldMetrics(ctx context.Context, mainDB *pgxpool.Pool, retention time.Duration) {
	if mainDB == nil {
		return
	}

	cutoff := time.Now().Add(-retention)
	const sql = `DELETE FROM system_metrics WHERE recorded_at < $1`

	tag, err := mainDB.Exec(ctx, sql, cutoff)
	if err != nil {
		slog.Error("metrics cleanup failed", "error", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("pruned old metrics", "deleted", tag.RowsAffected(), "olderThan", cutoff.Format(time.RFC3339))
	}
}

// cleanupOrphanedVectors deletes vector chunks whose file_id no longer exists
// in the files table. This can happen if file deletion partially fails.
func cleanupOrphanedVectors(ctx context.Context, mainDB, vectorDB *pgxpool.Pool) {
	if mainDB == nil || vectorDB == nil {
		return
	}
	// Find all document_chunks_* tables.
	const listTablesSQL = `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name LIKE 'document_chunks%'`

	rows, err := vectorDB.Query(ctx, listTablesSQL)
	if err != nil {
		slog.Error("orphan cleanup: failed to list vector tables", "error", err)
		return
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			slog.Error("orphan cleanup: scan table name", "error", err)
			return
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		slog.Error("orphan cleanup: table listing iteration error", "error", err)
		return
	}

	if len(tables) == 0 {
		return
	}

	totalOrphaned := 0
	totalDeleted := int64(0)

	for _, tableName := range tables {
		// Validate table name format before interpolating into SQL.
		if !vector.IsValidVectorTableName(tableName) {
			slog.Error("orphan cleanup: unexpected table name, skipping", "table", tableName)
			continue
		}

		vectorFileIDs, err := collectVectorFileIDs(ctx, vectorDB, tableName)
		if err != nil {
			slog.Error("orphan cleanup: failed to list file_ids", "table", tableName, "error", err)
			continue
		}

		if len(vectorFileIDs) == 0 {
			continue
		}

		existing, err := collectExistingFileIDs(ctx, mainDB, vectorFileIDs)
		if err != nil {
			slog.Error("orphan cleanup: failed to check existing files", "table", tableName, "error", err)
			continue
		}

		// Find orphaned file_ids.
		var orphaned []string
		for _, id := range vectorFileIDs {
			if !existing[id] {
				orphaned = append(orphaned, id)
			}
		}

		if len(orphaned) == 0 {
			continue
		}

		totalOrphaned += len(orphaned)

		// Batch-delete orphaned vectors in a single query. tableName comes
		// from information_schema with a LIKE filter and is also validated by
		// vector.IsValidVectorTableName above — never user input. Do NOT copy
		// this Sprintf pattern with any value that could originate from a request.
		deleteSQL := fmt.Sprintf(`DELETE FROM "%s" WHERE file_id = ANY($1::uuid[])`, tableName)
		tag, err := vectorDB.Exec(ctx, deleteSQL, orphaned)
		if err != nil {
			slog.Error("orphan cleanup: failed to delete chunks",
				"table", tableName, "orphanedFileIds", len(orphaned), "error", err)
			continue
		}
		totalDeleted += tag.RowsAffected()
		slog.Info("cleaned up orphaned vectors",
			"table", tableName, "orphanedFileIds", len(orphaned), "deletedChunks", tag.RowsAffected())
	}

	if totalOrphaned > 0 {
		slog.Info("orphaned vector cleanup completed",
			"tablesChecked", len(tables),
			"orphanedFileIds", totalOrphaned,
			"deletedChunks", totalDeleted,
		)
	}
}

// collectVectorFileIDs returns the distinct file_id values stored in the
// given vector chunks table. The caller is expected to have validated the
// table name with vector.IsValidVectorTableName.
func collectVectorFileIDs(ctx context.Context, vectorDB *pgxpool.Pool, tableName string) ([]string, error) {
	sql := fmt.Sprintf(`SELECT DISTINCT file_id FROM "%s"`, tableName)
	rows, err := vectorDB.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// collectExistingFileIDs returns the set of file_ids from fileIDs that still
// exist in the main `files` table.
func collectExistingFileIDs(ctx context.Context, mainDB *pgxpool.Pool, fileIDs []string) (map[string]bool, error) {
	const sql = `SELECT id FROM files WHERE id = ANY($1::uuid[])`
	rows, err := mainDB.Query(ctx, sql, fileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	existing := make(map[string]bool, len(fileIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		existing[id] = true
	}
	return existing, rows.Err()
}
