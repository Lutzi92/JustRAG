package rss

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/safego"
)

// Scheduler manages periodic RSS feed polling via Asynq.
type Scheduler struct {
	client    *asynq.Client
	store     RSSStore
	parentCtx context.Context
	cancels   map[string]context.CancelFunc
	mu        sync.Mutex
	// wg tracks running ticker goroutines so StopAll can join before
	// returning. Without it, a goroutine mid-enqueuePoll could race the
	// Redis client close that runAsLeader performs immediately after.
	wg sync.WaitGroup
}

// NewScheduler creates a Scheduler backed by the given Asynq client and store.
// parentCtx controls the lifetime of all scheduler goroutines — when it is
// cancelled (e.g. worker shutdown), all schedules stop automatically. It MUST
// be non-nil; pass ctx from the server/worker lifetime.
func NewScheduler(parentCtx context.Context, client *asynq.Client, store RSSStore) *Scheduler {
	return &Scheduler{
		client:    client,
		store:     store,
		parentCtx: parentCtx,
		cancels:   make(map[string]context.CancelFunc),
	}
}

// StartFeedSchedule starts a goroutine that enqueues an RSS poll task every
// intervalMinutes. Any existing schedule for the same feedID is stopped first.
//
// The stop-create-store sequence runs under a single lock to avoid a TOCTOU
// window where two concurrent callers (or a caller racing with StopAll)
// would each create a goroutine and the second writer's cancel would
// overwrite the first in the map — leaking the first goroutine until the
// parent context cancels.
func (s *Scheduler) StartFeedSchedule(feedID string, intervalMinutes int) {
	s.mu.Lock()
	if existing, ok := s.cancels[feedID]; ok {
		existing()
		delete(s.cancels, feedID)
	}
	ctx, cancel := context.WithCancel(s.parentCtx)
	s.cancels[feedID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	safego.Go(func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.enqueuePoll(feedID)
			}
		}
	})

	slog.Info("RSS schedule started", "feedId", feedID, "intervalMinutes", intervalMinutes)
}

// StopFeedSchedule cancels the goroutine for the given feed.
func (s *Scheduler) StopFeedSchedule(feedID string) {
	s.mu.Lock()
	if cancel, ok := s.cancels[feedID]; ok {
		cancel()
		delete(s.cancels, feedID)
	}
	s.mu.Unlock()
}

// InitializeAll loads active feeds from the DB and starts their schedules.
func (s *Scheduler) InitializeAll(ctx context.Context) error {
	feeds, err := s.store.ListActiveRSSFeeds(ctx)
	if err != nil {
		return err
	}
	for _, feed := range feeds {
		s.StartFeedSchedule(feed.ID, feed.PollInterval)
	}
	slog.Info("RSS schedules initialized", "count", len(feeds))
	return nil
}

// EnqueuePollNow immediately enqueues a poll task for the given feed.
func (s *Scheduler) EnqueuePollNow(feedID string) error {
	return s.enqueuePoll(feedID)
}

func (s *Scheduler) enqueuePoll(feedID string) error {
	payload, _ := json.Marshal(jobs.RSSPollPayload{FeedID: feedID})
	_, err := s.client.Enqueue(
		asynq.NewTask(jobs.TypeRSSPoll, payload),
		asynq.Queue(jobs.QueueHeavy),
		asynq.MaxRetry(3),
		asynq.Timeout(jobs.TimeoutFor(jobs.TypeRSSPoll)),
	)
	if err != nil {
		slog.Error("failed to enqueue RSS poll", "feedId", feedID, "error", err)
	}
	return err
}

// StopAll cancels all active feed schedules and blocks until every ticker
// goroutine has exited. Joining matters because shutdown closes the Redis
// client used by enqueuePoll immediately after this returns — without the
// wait, a goroutine still in flight would race the close.
func (s *Scheduler) StopAll() {
	s.mu.Lock()
	for id, cancel := range s.cancels {
		cancel()
		delete(s.cancels, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}
