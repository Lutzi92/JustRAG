package admineval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/jobs"
	storepkg "github.com/justrag/go-backend/internal/store"
)

// ---------------------------------------------------------------------------
// Fakes dedicated to ScheduledWorker tests.
//
// handler_test.go's mockRunStore/mockGoldenSetStore and kb_handler_test.go's
// fakeRunStore/fakeGSStore are all shaped for single-KB, single-run
// HTTP-handler tests (one fixed HasActiveRun bool, one fixed Get result).
// The scheduled worker needs per-KB active-run state and an ID-keyed golden
// set lookup, so these are separate, test-only types rather than retrofits
// of the widely-reused HTTP-handler fakes.
// ---------------------------------------------------------------------------

var (
	_ runStore       = (*schedRunStore)(nil)
	_ goldenSetStore = (*schedGoldenSetStore)(nil)
	_ enqueuer       = (*fakeEnqueuer)(nil)
)

type schedRunStore struct {
	inserted        []eval.Run
	insertErr       error
	active          map[uuid.UUID]bool
	activeErr       error
	markFailedCalls int
	markFailedErr   error
}

func newFakeRunStore() *schedRunStore {
	return &schedRunStore{active: map[uuid.UUID]bool{}}
}

func (s *schedRunStore) Insert(_ context.Context, r eval.Run) (uuid.UUID, error) {
	if s.insertErr != nil {
		return uuid.Nil, s.insertErr
	}
	r.ID = uuid.New()
	s.inserted = append(s.inserted, r)
	return r.ID, nil
}

func (s *schedRunStore) Get(_ context.Context, _ uuid.UUID) (*eval.Run, error) { return nil, nil }

func (s *schedRunStore) List(_ context.Context, _ eval.ListOpts) ([]eval.Run, int, error) {
	return nil, 0, nil
}

func (s *schedRunStore) Delete(_ context.Context, _ uuid.UUID) (bool, bool, error) {
	return false, false, nil
}

func (s *schedRunStore) MarkRunning(_ context.Context, _ uuid.UUID) error { return nil }

func (s *schedRunStore) MarkCompleted(_ context.Context, _ uuid.UUID, _ json.RawMessage) error {
	return nil
}

func (s *schedRunStore) MarkFailed(_ context.Context, _ uuid.UUID, _ string) error {
	s.markFailedCalls++
	return s.markFailedErr
}

func (s *schedRunStore) HasActiveRun(_ context.Context, kbID uuid.UUID) (bool, error) {
	if s.activeErr != nil {
		return false, s.activeErr
	}
	return s.active[kbID], nil
}

type schedGoldenSetStore struct {
	sets map[uuid.UUID]eval.GoldenSet
}

func newFakeGoldenSetStore(sets ...eval.GoldenSet) *schedGoldenSetStore {
	m := make(map[uuid.UUID]eval.GoldenSet, len(sets))
	for _, s := range sets {
		m[s.ID] = s
	}
	return &schedGoldenSetStore{sets: m}
}

func (s *schedGoldenSetStore) Create(_ context.Context, _ eval.GoldenSet) (uuid.UUID, time.Time, error) {
	return uuid.Nil, time.Time{}, nil
}

func (s *schedGoldenSetStore) Get(_ context.Context, id uuid.UUID) (*eval.GoldenSet, error) {
	gs, ok := s.sets[id]
	if !ok {
		return nil, storepkg.ErrNotFound
	}
	return &gs, nil
}

func (s *schedGoldenSetStore) List(_ context.Context) ([]eval.GoldenSet, error) { return nil, nil }

func (s *schedGoldenSetStore) Delete(_ context.Context, _ uuid.UUID) (bool, error) {
	return false, nil
}

func (s *schedGoldenSetStore) ListByKB(_ context.Context, _ uuid.UUID) ([]eval.GoldenSet, error) {
	return nil, nil
}

// fakeEnqueuer records every task passed to EnqueueContext, so tests can
// assert on task type and payload rather than just a call count.
type fakeEnqueuer struct {
	tasks []*asynq.Task
	err   error
}

func (f *fakeEnqueuer) EnqueueContext(_ context.Context, t *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.tasks = append(f.tasks, t)
	return &asynq.TaskInfo{}, nil
}

// staticCfg is a tiny test-only siteConfigReader backed by a plain map.
type staticCfg map[string]string

func (c staticCfg) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if c == nil {
		return nil, nil
	}
	v, ok := c[key]
	if !ok {
		return nil, nil
	}
	return &v, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHandleScheduled_CreatesScheduledRunAndEnqueues(t *testing.T) {
	gsID, kbID := uuid.New(), uuid.New()
	sets := newFakeGoldenSetStore(eval.GoldenSet{ID: gsID, KBID: kbID, ContentHash: "h", Schedule: "daily"})
	runs := newFakeRunStore()
	enq := &fakeEnqueuer{}
	w := NewScheduledWorker(runs, sets, staticCfg(map[string]string{}), nil, enq)

	payload, _ := json.Marshal(jobs.EvalScheduledPayload{GoldenSetID: gsID.String()})
	if err := w.HandleScheduled(context.Background(), asynq.NewTask(jobs.TypeEvalScheduled, payload)); err != nil {
		t.Fatal(err)
	}
	if len(runs.inserted) != 1 {
		t.Fatalf("want 1 run inserted, got %d", len(runs.inserted))
	}
	r := runs.inserted[0]
	if !r.Scheduled || r.KBID != kbID || r.GoldenSetID == nil || *r.GoldenSetID != gsID || r.JudgeEnabled || r.TopK != 10 || r.TriggeredBy != nil || r.Label != "scheduled" {
		t.Fatalf("run row shape wrong: %+v", r)
	}
	if len(enq.tasks) != 1 || enq.tasks[0].Type() != jobs.TypeEvalRun {
		t.Fatalf("want one TypeEvalRun enqueued, got %+v", enq.tasks)
	}
}

func TestHandleScheduled_SkipsWhenKBHasActiveRun(t *testing.T) {
	gsID, kbID := uuid.New(), uuid.New()
	sets := newFakeGoldenSetStore(eval.GoldenSet{ID: gsID, KBID: kbID, ContentHash: "h", Schedule: "daily"})
	runs := newFakeRunStore()
	runs.active[kbID] = true
	enq := &fakeEnqueuer{}
	w := NewScheduledWorker(runs, sets, staticCfg(nil), nil, enq)
	payload, _ := json.Marshal(jobs.EvalScheduledPayload{GoldenSetID: gsID.String()})
	if err := w.HandleScheduled(context.Background(), asynq.NewTask(jobs.TypeEvalScheduled, payload)); err != nil {
		t.Fatalf("active run must be a silent skip, not an error: %v", err)
	}
	if len(runs.inserted) != 0 || len(enq.tasks) != 0 {
		t.Fatal("must not insert or enqueue while the KB has an active run")
	}
}

func TestHandleScheduled_SkipsManualSet(t *testing.T) {
	// A set switched back to manual between stamp and tick must not run.
	gsID := uuid.New()
	sets := newFakeGoldenSetStore(eval.GoldenSet{ID: gsID, KBID: uuid.New(), ContentHash: "h", Schedule: "manual"})
	runs := newFakeRunStore()
	enq := &fakeEnqueuer{}
	w := NewScheduledWorker(runs, sets, staticCfg(nil), nil, enq)
	payload, _ := json.Marshal(jobs.EvalScheduledPayload{GoldenSetID: gsID.String()})
	if err := w.HandleScheduled(context.Background(), asynq.NewTask(jobs.TypeEvalScheduled, payload)); err != nil {
		t.Fatal(err)
	}
	if len(runs.inserted) != 0 {
		t.Fatal("manual set must not run")
	}
}

func TestHandleScheduled_BadPayloadIsTerminal(t *testing.T) {
	w := NewScheduledWorker(newFakeRunStore(), newFakeGoldenSetStore(), staticCfg(nil), nil, &fakeEnqueuer{})
	err := w.HandleScheduled(context.Background(), asynq.NewTask(jobs.TypeEvalScheduled, []byte("nope")))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("want SkipRetry-wrapped error, got %v", err)
	}
}
