package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus/testutil"

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
