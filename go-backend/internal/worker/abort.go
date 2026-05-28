package worker

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// watchAbortKey polls a Redis key every 2 seconds and calls cancel() when the
// key is set. This wires client disconnect (which sets the abort key via the
// SSE relay) into context cancellation, so all downstream operations
// (GenerateCompletion, HTTP requests, DB queries) stop promptly.
func watchAbortKey(ctx context.Context, cancel context.CancelFunc, rdb *redis.Client, abortKey string) {
	if abortKey == "" || rdb == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			val, err := rdb.Get(ctx, abortKey).Result()
			if err == nil && val != "" {
				cancel()
				return
			}
		}
	}
}
