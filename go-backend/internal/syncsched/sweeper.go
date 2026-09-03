// Package syncsched enqueues scheduled source syncs inside the configured
// night window.
//
// It replaces the per-source ticker goroutines that internal/rss and
// internal/confluence used to run. Those anchored a source's period to the
// process start time — so "daily" meant "24h after this replica booted" — and
// only ever read the DB at leader election, which made a schedule change in
// the UI a no-op until the next restart. A single DB-driven sweeper fixes
// both and covers git repositories, which had no scheduler at all.
package syncsched

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/syncwindow"
)

// TickInterval is how often the sweeper looks for due sources. It bounds how
// late a source can start relative to its slot.
const TickInterval = 5 * time.Minute

// SourceStore is the per-kind contract implemented by the rss, confluence and
// gitrepo stores.
type SourceStore interface {
	// Kind is one of "rss", "confluence", "git_repo".
	Kind() string
	// ListDue returns non-manual, active sources whose stamped slot is at or
	// before now, each carrying its own schedule.
	ListDue(ctx context.Context, now time.Time) ([]syncwindow.DueSource, error)
	// ListUnscheduled returns non-manual, active sources with no stamped
	// slot yet.
	ListUnscheduled(ctx context.Context) ([]syncwindow.DueSource, error)
	// MarkScheduled stores the next slot for a source.
	MarkScheduled(ctx context.Context, id string, next time.Time) error
}

// Enqueuer is the subset of *asynq.Client the sweeper uses.
type Enqueuer interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// Sweeper enqueues due syncs for every registered source kind.
type Sweeper struct {
	enq    Enqueuer
	cfg    syncwindow.SiteConfigReader
	stores []SourceStore
}

// New builds a Sweeper over the given stores.
func New(enq Enqueuer, cfg syncwindow.SiteConfigReader, stores ...SourceStore) *Sweeper {
	return &Sweeper{enq: enq, cfg: cfg, stores: stores}
}

// Run ticks until ctx is cancelled. Call it from the scheduler leader.
func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(TickInterval)
	defer ticker.Stop()
	s.Tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			s.Tick(ctx, t)
		}
	}
}

// Tick performs one sweep. The window is re-read every tick so an admin
// change takes effect within TickInterval; already-stamped slots keep their
// old time.
func (s *Sweeper) Tick(ctx context.Context, now time.Time) {
	window := syncwindow.WindowFrom(ctx, s.cfg)

	for _, store := range s.stores {
		kind := store.Kind()

		// Newly scheduled sources: stamp only. Enqueuing here would fire a
		// full sync in the middle of the working day, which is the whole
		// thing the night window exists to prevent.
		unscheduled, err := store.ListUnscheduled(ctx)
		if err != nil {
			slog.Warn("sync sweeper: list unscheduled failed", "kind", kind, "error", err)
		}
		for _, src := range unscheduled {
			s.stamp(ctx, store, src, now, window)
		}

		due, err := store.ListDue(ctx, now)
		if err != nil {
			slog.Warn("sync sweeper: list due failed", "kind", kind, "error", err)
			continue
		}
		for _, src := range due {
			// Stamp BEFORE enqueuing: if the process dies between the two,
			// the source misses one night. The other order would re-enqueue
			// a full re-sync on every subsequent tick.
			if !s.stamp(ctx, store, src, now, window) {
				continue
			}
			s.enqueue(kind, src.ID)
		}
	}
}

// stamp writes the next slot; it reports false when nothing was written, in
// which case the caller must not enqueue.
func (s *Sweeper) stamp(ctx context.Context, store SourceStore, src syncwindow.DueSource, now time.Time, w syncwindow.Window) bool {
	// The store's queries already exclude manual sources, so every row that
	// reaches here carries a real schedule.
	next, ok := syncwindow.NextSlot(src.ID, src.Schedule, now, w)
	if !ok {
		slog.Warn("sync sweeper: unschedulable row", "kind", store.Kind(), "id", src.ID, "schedule", src.Schedule)
		return false
	}
	if err := store.MarkScheduled(ctx, src.ID, next); err != nil {
		slog.Warn("sync sweeper: stamp failed", "kind", store.Kind(), "id", src.ID, "error", err)
		return false
	}
	return true
}

func (s *Sweeper) enqueue(kind, id string) {
	var task *asynq.Task
	switch kind {
	case "rss":
		payload, _ := json.Marshal(jobs.RSSPollPayload{FeedID: id})
		task = asynq.NewTask(jobs.TypeRSSPoll, payload)
	case "confluence":
		payload, _ := json.Marshal(jobs.ConfluenceSyncPayload{SourceID: id})
		task = asynq.NewTask(jobs.TypeConfluenceSync, payload)
	case "git_repo":
		payload, _ := json.Marshal(jobs.GitRepoSyncPayload{SourceID: id})
		task = asynq.NewTask(jobs.TypeGitRepoSync, payload)
	default:
		slog.Warn("sync sweeper: unknown source kind", "kind", kind, "id", id)
		return
	}

	if _, err := s.enq.Enqueue(
		task,
		asynq.Queue(jobs.QueueHeavy),
		asynq.MaxRetry(3),
		asynq.Timeout(jobs.TimeoutFor(task.Type())),
	); err != nil {
		// The stamp already moved forward, so this costs one night rather
		// than looping.
		slog.Error("sync sweeper: enqueue failed", "kind", kind, "id", id, "error", err)
	}
}
