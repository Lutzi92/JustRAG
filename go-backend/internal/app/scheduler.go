package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-redsync/redsync/v4"
	redsyncredis "github.com/go-redsync/redsync/v4/redis/goredis/v9"

	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/gitrepo"
	"github.com/justrag/go-backend/internal/rss"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/siteconfig"
	"github.com/justrag/go-backend/internal/syncsched"
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

// startSchedulers runs the leader-election loop that drives scheduled syncs.
// Exactly one replica holds the leader role at a time; the leader runs a
// single night-window sweeper (internal/syncsched) that enqueues due RSS,
// Confluence, and git-repo syncs. This replaced the old per-source ticker
// schedulers, which anchored a source's period to process start time and
// only read the DB at leader election.
//
// The lock is acquired with a TTL and refreshed periodically. If the leader
// dies, the TTL expires and another replica acquires the lock on its next
// retry tick. If the lock is lost while held (e.g. Redis hiccup), the
// sweeper is torn down and the loop returns to acquisition mode.
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

		slog.Info("scheduler leader lock acquired — starting sync sweeper")
		runAsLeader(ctx, infra, mutex)
		slog.Info("scheduler leader role released")
	}
}

// runAsLeader starts the night-window sync sweeper on a context tied to the
// leader lock, then refreshes the lock until ctx is canceled or the lock is
// lost. The sweeper runs on leaderCtx (not the outer ctx) so it stops the
// moment this replica steps down — on lock loss or shutdown — rather than
// continuing to enqueue syncs after another replica has taken over.
// On exit, the lock is released.
func runAsLeader(ctx context.Context, infra *serverInfra, mutex *redsync.Mutex) {
	leaderCtx, cancel := context.WithCancel(ctx)

	// Defers run LIFO: release the lock first (declared first, runs last)
	// only after signaling the sweeper to stop (declared last, runs
	// first). This ordering doesn't wait for the sweeper goroutine to
	// actually observe cancellation, but it avoids releasing the lock
	// while it is still definitely running — narrowing, rather than
	// eliminating, the window in which a new leader's sweeper could
	// overlap with this one's in-flight tick.
	defer func() {
		// Best-effort release; ignore errors (TTL will expire anyway).
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer releaseCancel()
		_, _ = mutex.UnlockContext(releaseCtx)
	}()
	defer cancel()

	sweeper := syncsched.New(
		infra.asynqClient,
		siteconfig.NewStore(infra.db.Main),
		rss.NewStore(infra.db.Main),
		confluence.NewStore(infra.db.Main),
		gitrepo.NewStore(infra.db.Main),
	)
	safego.Go(func() { sweeper.Run(leaderCtx) })

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
