package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-redsync/redsync/v4"
	redsyncredis "github.com/go-redsync/redsync/v4/redis/goredis/v9"

	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/rss"
)

// schedulerLockKey is the Redis key used to elect a single leader across
// go-server replicas. The TTL must be longer than refreshInterval so a brief
// pause doesn't drop the lock; refresh runs well before expiry.
const (
	schedulerLockKey   = "justrag:scheduler:leader"
	schedulerLockTTL   = 30 * time.Second
	refreshInterval    = 10 * time.Second
	acquireRetryPeriod = 15 * time.Second
)

// startSchedulers runs the RSS and Confluence schedulers under a Redis-based
// leader lock so that exactly one go-server replica drives scheduled syncs.
// It blocks until ctx is canceled.
//
// The lock is acquired with a TTL and refreshed periodically. If the leader
// dies, the TTL expires and another replica acquires the lock on its next
// retry tick. If the lock is lost while held (e.g. Redis hiccup), running
// schedulers are torn down and the loop returns to acquisition mode.
func startSchedulers(ctx context.Context, infra *serverInfra) {
	pool := redsyncredis.NewPool(infra.rdb.Client)
	rs := redsync.New(pool)

	for {
		if ctx.Err() != nil {
			return
		}

		mutex := rs.NewMutex(
			schedulerLockKey,
			redsync.WithExpiry(schedulerLockTTL),
			redsync.WithTries(1),
		)

		if err := mutex.LockContext(ctx); err != nil {
			// Another replica holds the lock — sleep and retry.
			slog.Debug("scheduler leader lock not acquired, will retry", "error", err)
			if !sleepOrDone(ctx, acquireRetryPeriod) {
				return
			}
			continue
		}

		slog.Info("scheduler leader lock acquired — starting RSS + Confluence schedulers")
		runAsLeader(ctx, infra, mutex)
		slog.Info("scheduler leader role released")
	}
}

// runAsLeader starts the schedulers and refreshes the lock until ctx is
// canceled or the lock is lost. On exit, schedulers are stopped and the lock
// is released.
func runAsLeader(ctx context.Context, infra *serverInfra, mutex *redsync.Mutex) {
	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	rssScheduler := rss.NewScheduler(leaderCtx, infra.asynqClient, rss.NewStore(infra.db.Main))
	if err := rssScheduler.InitializeAll(leaderCtx); err != nil {
		slog.Warn("failed to initialize RSS schedules", "error", err)
	}
	defer rssScheduler.StopAll()

	confScheduler := confluence.NewConfluenceScheduler(leaderCtx, infra.asynqClient, confluence.NewStore(infra.db.Main))
	if err := confScheduler.InitializeAll(leaderCtx); err != nil {
		slog.Warn("failed to initialize Confluence schedules", "error", err)
	}
	defer confScheduler.StopAll()

	defer func() {
		// Best-effort release; ignore errors (TTL will expire anyway).
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer releaseCancel()
		_, _ = mutex.UnlockContext(releaseCtx)
	}()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := mutex.ExtendContext(ctx)
			if err != nil || !ok {
				slog.Warn("scheduler leader lock lost — stepping down", "error", err)
				return
			}
		}
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
