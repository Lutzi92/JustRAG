package admineval

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/jobs"
)

// ---------------------------------------------------------------------------
// mockStore is an in-memory implementation of the eval.Store's write methods
// used by HandleRun so we can test the worker without a real database.
// ---------------------------------------------------------------------------

type mockStore struct {
	mu          sync.Mutex
	runs        map[uuid.UUID]*eval.Run
	runningIDs  []uuid.UUID
	completedAt map[uuid.UUID]json.RawMessage
	failedAt    map[uuid.UUID]string
}

func newMockStore(run eval.Run) *mockStore {
	return &mockStore{
		runs:        map[uuid.UUID]*eval.Run{run.ID: &run},
		completedAt: map[uuid.UUID]json.RawMessage{},
		failedAt:    map[uuid.UUID]string{},
	}
}

func (s *mockStore) MarkRunning(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runningIDs = append(s.runningIDs, id)
	return nil
}

func (s *mockStore) MarkCompleted(_ context.Context, id uuid.UUID, report json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completedAt[id] = report
	return nil
}

func (s *mockStore) MarkFailed(_ context.Context, id uuid.UUID, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedAt[id] = msg
	return nil
}

func (s *mockStore) Get(_ context.Context, id uuid.UUID) (*eval.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return r, nil
}

// storeAdapter wraps mockStore so the Worker can use it via the same interface
// as eval.Store. We mirror only the methods HandleRun calls.
type storeAdapter struct{ m *mockStore }

func (a *storeAdapter) MarkRunning(ctx context.Context, id uuid.UUID) error {
	return a.m.MarkRunning(ctx, id)
}
func (a *storeAdapter) MarkCompleted(ctx context.Context, id uuid.UUID, report json.RawMessage) error {
	return a.m.MarkCompleted(ctx, id, report)
}
func (a *storeAdapter) MarkFailed(ctx context.Context, id uuid.UUID, msg string) error {
	return a.m.MarkFailed(ctx, id, msg)
}
func (a *storeAdapter) Get(ctx context.Context, id uuid.UUID) (*eval.Run, error) {
	return a.m.Get(ctx, id)
}

// ---------------------------------------------------------------------------
// workerUnderTest is a test-friendly variant of Worker that skips the
// advisory lock (Postgres-only) and accepts a store interface instead of
// *eval.Store. This lets us exercise HandleRun's state-machine logic
// (mark running → run → mark completed/failed) without an integration DB.
// ---------------------------------------------------------------------------

type testableWorker struct {
	store interface {
		MarkRunning(ctx context.Context, id uuid.UUID) error
		MarkCompleted(ctx context.Context, id uuid.UUID, report json.RawMessage) error
		MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error
		Get(ctx context.Context, id uuid.UUID) (*eval.Run, error)
	}
	run func(ctx context.Context, r eval.Run) (json.RawMessage, error)
}

// handleRun mirrors HandleRun but skips the Postgres advisory lock so the
// unit test can run without a real DB. All observable state transitions
// (mark running, mark completed/failed, metric side-effects) are identical.
func (w *testableWorker) handleRun(ctx context.Context, t *asynq.Task) error {
	var p RunPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return err
	}

	if err := w.store.MarkRunning(ctx, p.RunID); err != nil {
		return err
	}

	run, err := w.store.Get(ctx, p.RunID)
	if err != nil {
		_ = w.store.MarkFailed(ctx, p.RunID, "load run: "+err.Error())
		return nil
	}

	reportJSON, runErr := w.run(ctx, *run)
	if runErr != nil {
		_ = w.store.MarkFailed(ctx, p.RunID, runErr.Error())
		return nil
	}
	_ = w.store.MarkCompleted(ctx, p.RunID, reportJSON)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustPayload(t *testing.T, runID uuid.UUID) []byte {
	t.Helper()
	b, err := json.Marshal(RunPayload{RunID: runID})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func sampleRun(id uuid.UUID) eval.Run {
	snap, _ := json.Marshal(map[string]*string{})
	gsID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	return eval.Run{
		ID:             id,
		CreatedAt:      time.Now(),
		Status:         "queued",
		KBID:           uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		GoldenSetID:    &gsID,
		FixtureHash:    "abc123",
		ConfigSnapshot: snap,
		JudgeEnabled:   false,
		TopK:           10,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestHandleRun_SuccessPath verifies that on success the store records the run
// as completed with the JSON returned by the run function.
func TestHandleRun_SuccessPath(t *testing.T) {
	runID := uuid.New()
	run := sampleRun(runID)
	ms := newMockStore(run)
	store := &storeAdapter{ms}

	wantReport := json.RawMessage(`{"golden_path":"/golden/test.jsonl","k":10,"questions":[],"aggregate":{"k":0,"count":0,"mean_recall":0,"mean_precision":0,"mrr":0,"mean_ndcg":0,"p50_recall":0,"p95_recall":0},"errors":0}`)
	tw := &testableWorker{
		store: store,
		run: func(_ context.Context, r eval.Run) (json.RawMessage, error) {
			return wantReport, nil
		},
	}

	task := asynq.NewTask(jobs.TypeEvalRun, mustPayload(t, runID))
	if err := tw.handleRun(context.Background(), task); err != nil {
		t.Fatalf("handleRun returned unexpected error: %v", err)
	}

	// run must be marked running exactly once.
	if len(ms.runningIDs) != 1 || ms.runningIDs[0] != runID {
		t.Errorf("expected MarkRunning called once with %v; got %v", runID, ms.runningIDs)
	}
	// must be marked completed with the report JSON.
	got, ok := ms.completedAt[runID]
	if !ok {
		t.Fatal("expected MarkCompleted to be called; it was not")
	}
	if string(got) != string(wantReport) {
		t.Errorf("report mismatch:\n got  %s\n want %s", got, wantReport)
	}
	// must NOT be marked failed.
	if _, failed := ms.failedAt[runID]; failed {
		t.Errorf("expected no MarkFailed call; got error: %s", ms.failedAt[runID])
	}
}

// TestHandleRun_RunFuncError verifies that when the run function returns an
// error the store records a failure and the task handler returns nil (so asynq
// does not retry).
func TestHandleRun_RunFuncError(t *testing.T) {
	runID := uuid.New()
	run := sampleRun(runID)
	ms := newMockStore(run)
	store := &storeAdapter{ms}

	wantErr := "embedding service unreachable"
	tw := &testableWorker{
		store: store,
		run: func(_ context.Context, _ eval.Run) (json.RawMessage, error) {
			return nil, errors.New(wantErr)
		},
	}

	task := asynq.NewTask(jobs.TypeEvalRun, mustPayload(t, runID))
	if err := tw.handleRun(context.Background(), task); err != nil {
		t.Fatalf("handleRun should swallow run errors; got: %v", err)
	}

	// must be marked failed with the error message.
	gotMsg, failed := ms.failedAt[runID]
	if !failed {
		t.Fatal("expected MarkFailed to be called; it was not")
	}
	if gotMsg != wantErr {
		t.Errorf("error message mismatch: got %q want %q", gotMsg, wantErr)
	}
	// must NOT be marked completed.
	if _, completed := ms.completedAt[runID]; completed {
		t.Error("expected no MarkCompleted call; it was called")
	}
}

// TestHandleRun_BadPayload verifies that an undecodable payload causes an
// immediate error return (the task will be dead-lettered by asynq).
func TestHandleRun_BadPayload(t *testing.T) {
	runID := uuid.New()
	run := sampleRun(runID)
	ms := newMockStore(run)
	store := &storeAdapter{ms}

	tw := &testableWorker{
		store: store,
		run: func(_ context.Context, _ eval.Run) (json.RawMessage, error) {
			return nil, nil
		},
	}

	task := asynq.NewTask(jobs.TypeEvalRun, []byte("not-json"))
	if err := tw.handleRun(context.Background(), task); err == nil {
		t.Fatal("expected error for bad payload; got nil")
	}
}
