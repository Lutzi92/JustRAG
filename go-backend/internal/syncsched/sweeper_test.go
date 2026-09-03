package syncsched

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/syncwindow"
)

// daily builds a due row with the daily schedule.
func daily(id string) syncwindow.DueSource {
	return syncwindow.DueSource{ID: id, Schedule: syncwindow.ScheduleDaily}
}

type fakeStore struct {
	kind        string
	due         []syncwindow.DueSource
	unscheduled []syncwindow.DueSource
	marked      map[string]time.Time
	markErr     error
	mu          sync.Mutex
}

func newFakeStore(kind string) *fakeStore {
	return &fakeStore{kind: kind, marked: map[string]time.Time{}}
}

func (f *fakeStore) Kind() string { return f.kind }

func (f *fakeStore) ListDue(context.Context, time.Time) ([]syncwindow.DueSource, error) {
	return f.due, nil
}

func (f *fakeStore) ListUnscheduled(context.Context) ([]syncwindow.DueSource, error) {
	return f.unscheduled, nil
}

func (f *fakeStore) MarkScheduled(_ context.Context, id string, next time.Time) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked[id] = next
	return nil
}

type fakeEnqueuer struct {
	mu    sync.Mutex
	tasks []*asynq.Task
	err   error
}

func (f *fakeEnqueuer) Enqueue(t *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, t)
	return &asynq.TaskInfo{}, nil
}

type fakeConfig struct{}

func (fakeConfig) GetSiteConfigValue(context.Context, string) (*string, error) { return nil, nil }

func TestTick_EnqueuesDueSourcesAndStampsThemForward(t *testing.T) {
	store := newFakeStore("rss")
	store.due = []syncwindow.DueSource{daily("feed-1"), daily("feed-2")}
	enq := &fakeEnqueuer{}
	s := New(enq, fakeConfig{}, store)

	now := time.Date(2026, 9, 3, 1, 30, 0, 0, time.UTC)
	s.Tick(context.Background(), now)

	if len(enq.tasks) != 2 {
		t.Fatalf("expected 2 enqueued tasks, got %d", len(enq.tasks))
	}
	for _, src := range store.due {
		next, ok := store.marked[src.ID]
		if !ok {
			t.Fatalf("%s was not stamped forward", src.ID)
		}
		if !next.After(now) {
			t.Fatalf("%s stamped to %v, not after %v", src.ID, next, now)
		}
	}
}

func TestTick_StampsUnscheduledWithoutEnqueuing(t *testing.T) {
	store := newFakeStore("rss")
	store.unscheduled = []syncwindow.DueSource{daily("feed-new")}
	enq := &fakeEnqueuer{}
	s := New(enq, fakeConfig{}, store)

	s.Tick(context.Background(), time.Date(2026, 9, 3, 14, 0, 0, 0, time.UTC))

	if len(enq.tasks) != 0 {
		t.Fatalf("a newly scheduled source must not sync immediately, got %d tasks", len(enq.tasks))
	}
	if _, ok := store.marked["feed-new"]; !ok {
		t.Fatal("feed-new was not stamped")
	}
}

func TestTick_StampFailureSkipsEnqueue(t *testing.T) {
	store := newFakeStore("rss")
	store.due = []syncwindow.DueSource{daily("feed-1")}
	store.markErr = errors.New("db down")
	enq := &fakeEnqueuer{}
	s := New(enq, fakeConfig{}, store)

	s.Tick(context.Background(), time.Now())

	if len(enq.tasks) != 0 {
		t.Fatal("must not enqueue when the stamp failed — the source would re-fire every tick")
	}
}

func TestTick_EnqueueFailureLeavesStampAdvanced(t *testing.T) {
	store := newFakeStore("rss")
	store.due = []syncwindow.DueSource{daily("feed-1")}
	enq := &fakeEnqueuer{err: errors.New("redis down")}
	s := New(enq, fakeConfig{}, store)

	now := time.Now()
	s.Tick(context.Background(), now)

	next, ok := store.marked["feed-1"]
	if !ok || !next.After(now) {
		t.Fatal("stamp must stay advanced so a failed enqueue costs one night, not an enqueue loop")
	}
}

func TestTick_SweepsAllKinds(t *testing.T) {
	rssStore := newFakeStore("rss")
	rssStore.due = []syncwindow.DueSource{daily("feed-1")}
	confStore := newFakeStore("confluence")
	confStore.due = []syncwindow.DueSource{daily("src-1")}
	gitStore := newFakeStore("git_repo")
	gitStore.due = []syncwindow.DueSource{daily("repo-1")}
	enq := &fakeEnqueuer{}
	s := New(enq, fakeConfig{}, rssStore, confStore, gitStore)

	s.Tick(context.Background(), time.Now())

	if len(enq.tasks) != 3 {
		t.Fatalf("expected one task per kind, got %d", len(enq.tasks))
	}
	types := map[string]bool{}
	for _, task := range enq.tasks {
		types[task.Type()] = true
	}
	for _, want := range []string{jobs.TypeRSSPoll, jobs.TypeConfluenceSync, jobs.TypeGitRepoSync} {
		if !types[want] {
			t.Fatalf("missing task type %q in %v", want, types)
		}
	}
}

func TestTick_UnknownKindIsIgnored(t *testing.T) {
	store := newFakeStore("carrier-pigeon")
	store.due = []syncwindow.DueSource{daily("x-1")}
	enq := &fakeEnqueuer{}
	s := New(enq, fakeConfig{}, store)

	s.Tick(context.Background(), time.Now())

	if len(enq.tasks) != 0 {
		t.Fatal("an unknown kind must not enqueue anything")
	}
}

// TestRun_ReturnsPromptlyOnContextCancellation pins down the property that
// internal/app's runAsLeader relies on to join the sweeper goroutine before
// returning: Run must not outlive its context by more than a trivial
// scheduling delay. Run performs an immediate first Tick before entering its
// ticker select loop, so the stores here return empty due/unscheduled lists —
// that first Tick must be cheap or this test's timeout budget is measuring
// fake-store latency instead of Run's shutdown behavior.
func TestRun_ReturnsPromptlyOnContextCancellation(t *testing.T) {
	store := newFakeStore("rss")
	enq := &fakeEnqueuer{}
	s := New(enq, fakeConfig{}, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Run even starts — Run must still return.

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of its context being cancelled — a caller joining this goroutine (internal/app.runAsLeader) would hang shutdown")
	}
}
