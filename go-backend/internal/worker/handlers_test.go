package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/observability"
)

// TestInstrument_RecordsSuccessAndError verifies that the Instrument wrapper
// records the correct status label for both success and error returns and
// always propagates the inner handler's error verbatim.
func TestInstrument_RecordsSuccessAndError(t *testing.T) {
	const taskType = "test:instrument"
	totals := observability.WorkerTaskTotalForTest()

	beforeOK := testutil.ToFloat64(totals.WithLabelValues(taskType, "success"))
	beforeErr := testutil.ToFloat64(totals.WithLabelValues(taskType, "error"))

	wantErr := errors.New("boom")
	successHandler := Instrument(func(ctx context.Context, t *asynq.Task) error { return nil })
	errorHandler := Instrument(func(ctx context.Context, t *asynq.Task) error { return wantErr })

	if err := successHandler(context.Background(), asynq.NewTask(taskType, nil)); err != nil {
		t.Fatalf("success handler returned error: %v", err)
	}
	if err := errorHandler(context.Background(), asynq.NewTask(taskType, nil)); !errors.Is(err, wantErr) {
		t.Fatalf("error handler should propagate inner error verbatim, got %v", err)
	}

	if got := testutil.ToFloat64(totals.WithLabelValues(taskType, "success")) - beforeOK; got != 1 {
		t.Errorf("expected success +1, got %v", got)
	}
	if got := testutil.ToFloat64(totals.WithLabelValues(taskType, "error")) - beforeErr; got != 1 {
		t.Errorf("expected error +1, got %v", got)
	}
}

// fakeStatusStore records UpdateFileStatus calls for MarkErrorOnExhaustion tests.
type fakeStatusStore struct {
	lastStatus map[string]string
}

func newFakeStatusStore() *fakeStatusStore {
	return &fakeStatusStore{lastStatus: make(map[string]string)}
}

func (f *fakeStatusStore) UpdateFileStatus(_ context.Context, fileID, status string) error {
	f.lastStatus[fileID] = status
	return nil
}

// mustMarshal is a helper to create a task payload from a FileProcessingPayload.
func mustMarshalFilePayload(t *testing.T, p jobs.FileProcessingPayload) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	return b
}

// TestMarkErrorOnExhaustion_FinalAttemptMarksError verifies that when the inner
// handler errors on the final retry attempt (retried == maxRetry), the file
// status is set to "error" and the original error is still returned.
func TestMarkErrorOnExhaustion_FinalAttemptMarksError(t *testing.T) {
	// Override package-level indirections so we don't need a live asynq server.
	origRetryCount := getRetryCount
	origMaxRetry := getMaxRetry
	t.Cleanup(func() {
		getRetryCount = origRetryCount
		getMaxRetry = origMaxRetry
	})
	getRetryCount = func(_ context.Context) (int, bool) { return 3, true }
	getMaxRetry = func(_ context.Context) (int, bool) { return 3, true }

	wantErr := errors.New("processing failed")
	store := newFakeStatusStore()
	payload := mustMarshalFilePayload(t, jobs.FileProcessingPayload{FileID: "file-123"})

	wrapped := MarkErrorOnExhaustion(
		func(_ context.Context, _ *asynq.Task) error { return wantErr },
		store,
	)

	task := asynq.NewTask(jobs.TypeFileProcessing, payload)
	err := wrapped(context.Background(), task)

	if !errors.Is(err, wantErr) {
		t.Errorf("expected original error to be returned, got %v", err)
	}
	if got := store.lastStatus["file-123"]; got != "error" {
		t.Errorf("expected file-123 status=error, got %q", got)
	}
}

// TestMarkErrorOnExhaustion_NonFinalAttemptDoesNotMark verifies that on a
// non-final retry, no status update is recorded even though the inner handler errors.
func TestMarkErrorOnExhaustion_NonFinalAttemptDoesNotMark(t *testing.T) {
	origRetryCount := getRetryCount
	origMaxRetry := getMaxRetry
	t.Cleanup(func() {
		getRetryCount = origRetryCount
		getMaxRetry = origMaxRetry
	})
	getRetryCount = func(_ context.Context) (int, bool) { return 1, true }
	getMaxRetry = func(_ context.Context) (int, bool) { return 3, true }

	wantErr := errors.New("transient failure")
	store := newFakeStatusStore()
	payload := mustMarshalFilePayload(t, jobs.FileProcessingPayload{FileID: "file-456"})

	wrapped := MarkErrorOnExhaustion(
		func(_ context.Context, _ *asynq.Task) error { return wantErr },
		store,
	)

	task := asynq.NewTask(jobs.TypeFileProcessing, payload)
	err := wrapped(context.Background(), task)

	if !errors.Is(err, wantErr) {
		t.Errorf("expected original error to be returned, got %v", err)
	}
	if len(store.lastStatus) != 0 {
		t.Errorf("expected no status update on non-final attempt, got %v", store.lastStatus)
	}
}

// TestMarkErrorOnExhaustion_URLPayloadMarksError verifies that MarkErrorOnExhaustion
// correctly extracts the FileID from a URLProcessingPayload (cross-payload coverage).
// URLProcessingPayload and FileProcessingPayload share the same json:"fileId" field,
// so the wrapper's FileProcessingPayload unmarshal picks up the correct ID.
func TestMarkErrorOnExhaustion_URLPayloadMarksError(t *testing.T) {
	origRetryCount := getRetryCount
	origMaxRetry := getMaxRetry
	t.Cleanup(func() {
		getRetryCount = origRetryCount
		getMaxRetry = origMaxRetry
	})
	getRetryCount = func(_ context.Context) (int, bool) { return 3, true }
	getMaxRetry = func(_ context.Context) (int, bool) { return 3, true }

	wantErr := errors.New("url processing failed")
	store := newFakeStatusStore()

	b, err := json.Marshal(jobs.URLProcessingPayload{FileID: "url-file-1", KbID: "kb-1", FilePath: "/tmp/x.md"})
	if err != nil {
		t.Fatalf("failed to marshal URLProcessingPayload: %v", err)
	}

	wrapped := MarkErrorOnExhaustion(
		func(_ context.Context, _ *asynq.Task) error { return wantErr },
		store,
	)

	task := asynq.NewTask(jobs.TypeURLProcessing, b)
	gotErr := wrapped(context.Background(), task)

	if !errors.Is(gotErr, wantErr) {
		t.Errorf("expected original error to be returned, got %v", gotErr)
	}
	if got := store.lastStatus["url-file-1"]; got != "error" {
		t.Errorf("expected url-file-1 status=error, got %q", got)
	}
}

// TestMarkErrorOnExhaustion_SuccessNoMark verifies that a successful handler
// causes no status update and returns nil.
func TestMarkErrorOnExhaustion_SuccessNoMark(t *testing.T) {
	origRetryCount := getRetryCount
	origMaxRetry := getMaxRetry
	t.Cleanup(func() {
		getRetryCount = origRetryCount
		getMaxRetry = origMaxRetry
	})
	// Even on the "final" attempt, success → no status update.
	getRetryCount = func(_ context.Context) (int, bool) { return 3, true }
	getMaxRetry = func(_ context.Context) (int, bool) { return 3, true }

	store := newFakeStatusStore()
	payload := mustMarshalFilePayload(t, jobs.FileProcessingPayload{FileID: "file-789"})

	wrapped := MarkErrorOnExhaustion(
		func(_ context.Context, _ *asynq.Task) error { return nil },
		store,
	)

	task := asynq.NewTask(jobs.TypeFileProcessing, payload)
	err := wrapped(context.Background(), task)

	if err != nil {
		t.Errorf("expected nil error from successful handler, got %v", err)
	}
	if len(store.lastStatus) != 0 {
		t.Errorf("expected no status update on success, got %v", store.lastStatus)
	}
}
