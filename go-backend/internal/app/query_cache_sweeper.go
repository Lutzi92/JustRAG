package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/justrag/go-backend/internal/vector"
)

const queryCacheSweepInterval = 1 * time.Hour

// runQueryCacheSweeper drives a periodic DELETE of expired query_cache
// rows via QueryCache.SweepExpired. Idempotent at the SQL level (each
// run only deletes rows whose expires_at < now()), so safe to run from
// multiple replicas without coordination — overlapping sweeps converge
// on the same end state.
func runQueryCacheSweeper(ctx context.Context, qc *vector.QueryCache) {
	ticker := time.NewTicker(queryCacheSweepInterval)
	defer ticker.Stop()

	// Run once at startup to populate the size gauge promptly.
	sweepOnce(ctx, qc)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepOnce(ctx, qc)
		}
	}
}

func sweepOnce(ctx context.Context, qc *vector.QueryCache) {
	start := time.Now()
	deleted, err := qc.SweepExpired(ctx)
	if err != nil {
		slog.Warn("query_cache: sweep failed", "error", err)
		return
	}
	slog.Info("rag.query_cache.sweep", "deleted_count", deleted, "duration_ms", time.Since(start).Milliseconds())
}
