package confluence

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

// ConfluenceScheduler manages periodic Confluence source syncing via Asynq.
type ConfluenceScheduler struct {
	client    *asynq.Client
	store     ConfluenceStore
	parentCtx context.Context
	cancels   map[string]context.CancelFunc
	mu        sync.Mutex
	// wg tracks running ticker goroutines so StopAll can join before
	// returning. Without it, a goroutine mid-enqueueSync could race the
	// Redis client close that runAsLeader performs immediately after.
	wg sync.WaitGroup
}

// NewConfluenceScheduler creates a ConfluenceScheduler backed by the given
// Asynq client and store. parentCtx controls the lifetime of all scheduler
// goroutines — when cancelled, all schedules stop automatically. It MUST
// be non-nil; pass ctx from the server/worker lifetime.
func NewConfluenceScheduler(parentCtx context.Context, client *asynq.Client, store ConfluenceStore) *ConfluenceScheduler {
	return &ConfluenceScheduler{
		client:    client,
		store:     store,
		parentCtx: parentCtx,
		cancels:   make(map[string]context.CancelFunc),
	}
}

// StartSyncSchedule starts a goroutine that enqueues a Confluence sync task
// every intervalMinutes. Any existing schedule for the same sourceID is stopped
// first.
//
// The stop-create-store sequence runs under a single lock to avoid a TOCTOU
// window where two concurrent callers (or a caller racing with StopAll)
// would each create a goroutine and the second writer's cancel would
// overwrite the first in the map — leaking the first goroutine until the
// parent context cancels.
func (s *ConfluenceScheduler) StartSyncSchedule(sourceID string, intervalMinutes int) {
	s.mu.Lock()
	if existing, ok := s.cancels[sourceID]; ok {
		existing()
		delete(s.cancels, sourceID)
	}
	ctx, cancel := context.WithCancel(s.parentCtx)
	s.cancels[sourceID] = cancel
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
				s.enqueueSync(sourceID)
			}
		}
	})

	slog.Info("Confluence sync schedule started", "sourceId", sourceID, "intervalMinutes", intervalMinutes)
}

// StopSyncSchedule cancels the goroutine for the given source.
func (s *ConfluenceScheduler) StopSyncSchedule(sourceID string) {
	s.mu.Lock()
	if cancel, ok := s.cancels[sourceID]; ok {
		cancel()
		delete(s.cancels, sourceID)
	}
	s.mu.Unlock()
}

// InitializeAll loads active Confluence sources with a sync interval from the
// DB and starts their schedules.
func (s *ConfluenceScheduler) InitializeAll(ctx context.Context) error {
	sources, err := s.store.ListActiveConfluenceSources(ctx)
	if err != nil {
		return err
	}
	for _, src := range sources {
		if src.SyncInterval != nil {
			s.StartSyncSchedule(src.ID, *src.SyncInterval)
		}
	}
	slog.Info("Confluence sync schedules initialized", "count", len(sources))
	return nil
}

// EnqueueSyncNow immediately enqueues a sync task for the given source.
func (s *ConfluenceScheduler) EnqueueSyncNow(sourceID string) error {
	return s.enqueueSync(sourceID)
}

func (s *ConfluenceScheduler) enqueueSync(sourceID string) error {
	payload, _ := json.Marshal(jobs.ConfluenceSyncPayload{SourceID: sourceID})
	_, err := s.client.Enqueue(
		asynq.NewTask(jobs.TypeConfluenceSync, payload),
		asynq.Queue(jobs.QueueHeavy),
		asynq.MaxRetry(3),
	)
	if err != nil {
		slog.Error("failed to enqueue Confluence sync", "sourceId", sourceID, "error", err)
	}
	return err
}

// StopAll cancels all active sync schedules and blocks until every ticker
// goroutine has exited. Joining matters because shutdown closes the Redis
// client used by enqueueSync immediately after this returns — without the
// wait, a goroutine still in flight would race the close.
func (s *ConfluenceScheduler) StopAll() {
	s.mu.Lock()
	for id, cancel := range s.cancels {
		cancel()
		delete(s.cancels, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}
