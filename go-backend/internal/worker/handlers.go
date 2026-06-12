package worker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/requestid"
)

// getRetryCount / getMaxRetry are package-level indirections over the asynq
// accessors so MarkErrorOnExhaustion is unit-testable without a live asynq
// server populating the context. Tests override these.
var getRetryCount = asynq.GetRetryCount
var getMaxRetry = asynq.GetMaxRetry

// statusStore is the minimal surface MarkErrorOnExhaustion needs.
type statusStore interface {
	// MarkFileErrorIfUnset transitions to status='error' without clobbering
	// a stage/message the final attempt's handler already recorded.
	MarkFileErrorIfUnset(ctx context.Context, fileID, stage, message string) error
}

// MarkErrorOnExhaustion wraps a file-processing handler so that when it returns
// an error on the FINAL retry attempt, the file is transitioned to 'error'
// immediately instead of waiting for the maintenance stuck-file sweep (~10 min).
// Non-final failures pass through unchanged so asynq still retries.
func MarkErrorOnExhaustion(h asynq.HandlerFunc, store statusStore) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		err := h(ctx, task)
		if err == nil {
			return nil
		}
		retried, _ := getRetryCount(ctx)
		maxRetry, _ := getMaxRetry(ctx)
		if retried >= maxRetry {
			var p jobs.FileProcessingPayload
			if jsonErr := json.Unmarshal(task.Payload(), &p); jsonErr == nil && p.FileID != "" {
				if uErr := store.MarkFileErrorIfUnset(ctx, p.FileID, "processing", "Processing failed after 3 attempts"); uErr != nil {
					logctx.From(ctx).Error("worker: failed to mark file error on retry exhaustion", "fileId", p.FileID, "error", uErr)
				} else {
					logctx.From(ctx).Warn("worker: file marked error after retry exhaustion", "fileId", p.FileID)
				}
			}
		}
		return err
	}
}

// DefaultJobOptions returns standard retry/cleanup options.
func DefaultJobOptions() []asynq.Option {
	return []asynq.Option{
		asynq.MaxRetry(3),
		asynq.Queue(jobs.QueueHeavy),
		asynq.Retention(24 * time.Hour),
	}
}

// Instrument wraps an asynq.HandlerFunc with the standard task lifecycle
// observability: a per-task request_id, a start log, an end log carrying
// status + duration, and a worker_task Prometheus metric pair (counter +
// histogram). Wiring code in internal/app/worker.go is expected to apply
// this once per registration so individual handlers don't repeat the
// boilerplate.
func Instrument(h asynq.HandlerFunc) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		// Worker tasks degrade gracefully on entropy failure: log + continue
		// with the parent context (logs lose request_id correlation for this
		// task, but the task still runs).
		var ridErr error
		ctx, _, ridErr = requestid.EnsureContext(ctx)
		if ridErr != nil {
			logctx.From(ctx).Warn("requestid: entropy unavailable; task runs without correlation id", "error", ridErr)
		}
		taskType := task.Type()
		start := time.Now()
		logctx.From(ctx).Info("worker.task.start", "task_type", taskType)

		err := h(ctx, task)

		duration := time.Since(start)
		status := "success"
		if err != nil {
			status = "error"
		}
		observability.RecordWorkerTask(taskType, status, duration)
		logctx.From(ctx).Info("worker.task.end",
			"task_type", taskType,
			"status", status,
			"duration_ms", duration.Milliseconds(),
		)
		return err
	}
}
