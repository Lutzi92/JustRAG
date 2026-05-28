package worker

import (
	"context"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/requestid"
)

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
