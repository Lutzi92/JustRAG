// Package admineval contains the in-app eval runner's asynq worker and HTTP
// handlers for the /api/admin/eval/runs endpoints.
package admineval

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
)

// RunPayload is the asynq task payload for a TypeEvalRun task.
type RunPayload struct {
	RunID uuid.UUID `json:"run_id"`
}

// evalGlobalSlots caps concurrent evals deployment-wide. Implemented as N
// advisory-lock "slots" (asynq's Concurrency is global, with no per-queue cap,
// so the cap lives here). evalSlotKeyBase..+N-1 are the slot lock keys.
const (
	evalGlobalSlots = 2
	evalSlotKeyBase = int64(73847293847000)
)

// Worker executes eval runs dispatched via the asynq queue.
type Worker struct {
	pool  *pgxpool.Pool
	store *eval.Store
	// run performs the actual in-process eval. Typically bound to
	// eval.RunInProcessFromRecord with injected RunDeps.
	run func(ctx context.Context, r eval.Run) (json.RawMessage, error)
}

// NewWorker constructs a Worker. The run function is called with the loaded
// eval.Run row and must return marshaled Report JSON on success.
func NewWorker(
	pool *pgxpool.Pool,
	store *eval.Store,
	run func(ctx context.Context, r eval.Run) (json.RawMessage, error),
) *Worker {
	return &Worker{pool: pool, store: store, run: run}
}

// HandleRun is the asynq task handler for TypeEvalRun tasks.
//
// Advisory lock semantics: at most one eval runs across the deployment at any
// time. If a run is already in flight, pg_try_advisory_lock returns false and
// this handler returns a non-nil error so asynq retries with exponential
// backoff (see EnqueueRun's MaxRetry setting).
//
// Failure handling: once the row is marked running, all subsequent errors are
// recorded via MarkFailed and the handler returns nil so asynq does NOT retry
// the run (a failed DB or runner error is not transient at that point; the
// operator must re-kick the run manually).
func (w *Worker) HandleRun(ctx context.Context, t *asynq.Task) error {
	// 1. Unmarshal payload.
	var p RunPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("eval run: unmarshal payload: %w", err)
	}
	log := logctx.From(ctx).With("run_id", p.RunID)

	// detachedWriteCtx returns a fresh context.Background()-derived context with
	// a short deadline, intended for terminal-status DB writes. Detached so the
	// request ctx (which can be cancelled by asynq's task timeout once the run
	// itself takes too long) cannot prevent us from persisting the outcome.
	// Each call gets its own freshly-started timer — creating one writeCtx at
	// handler entry would expire before the long-running w.run completes.
	detachedWriteCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 15*time.Second)
	}

	// 2. Load the run row first — we need kb_id to choose the per-KB lock.
	run, err := w.store.Get(ctx, p.RunID)
	if err != nil {
		errMsg := fmt.Sprintf("load run: %v", err)
		wctx, wcancel := detachedWriteCtx()
		if mfErr := w.store.MarkFailed(wctx, p.RunID, errMsg); mfErr != nil {
			log.Error("eval.run.mark_failed", "error", mfErr)
		}
		wcancel()
		log.Error("eval.run.load_failed", "error", err)
		return nil // do not re-enqueue
	}

	// 3. Acquire a dedicated connection for the session-scoped advisory locks.
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("eval run: acquire conn: %w", err)
	}
	defer conn.Release()

	// 3a. Per-KB lock: serialize evals within a KB (different KBs run in parallel).
	var gotKB bool
	kbLockArg := "eval:" + run.KBID.String()
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", kbLockArg).Scan(&gotKB); err != nil {
		return fmt.Errorf("eval run: kb advisory lock: %w", err)
	}
	if !gotKB {
		log.Info("eval.run.requeued", "reason", "another run holds this KB's lock", "kb_id", run.KBID)
		return fmt.Errorf("another eval run is in flight for this KB; will retry")
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtextextended($1, 0))", kbLockArg)
	}()

	// 3b. Global cap: grab one of N slots; requeue if all are taken.
	slot := int64(-1)
	for i := int64(0); i < evalGlobalSlots; i++ {
		var got bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", evalSlotKeyBase+i).Scan(&got); err != nil {
			return fmt.Errorf("eval run: slot lock: %w", err)
		}
		if got {
			slot = evalSlotKeyBase + i
			break
		}
	}
	if slot < 0 {
		log.Info("eval.run.requeued", "reason", "global eval concurrency cap reached", "cap", evalGlobalSlots)
		return fmt.Errorf("global eval concurrency cap reached; will retry")
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", slot)
	}()

	// 4. Mark running and proceed. (run is already loaded above.)
	if err := w.store.MarkRunning(ctx, p.RunID); err != nil {
		return fmt.Errorf("eval run: mark running: %w", err)
	}
	observability.EvalRunActive.Set(1)
	defer observability.EvalRunActive.Set(0)
	startedAt := time.Now()

	log.Info("eval.run.started",
		"kb_id", run.KBID,
		"judge", run.JudgeEnabled,
		"top_k", run.TopK,
		"golden_set_id", run.GoldenSetID,
	)

	// 5. Execute the in-process eval runner.
	reportJSON, runErr := w.run(ctx, *run)
	durationSeconds := time.Since(startedAt).Seconds()
	kbLabel := run.KBID.String()

	// 6. Persist outcome and emit metrics.
	if runErr != nil {
		wctx, wcancel := detachedWriteCtx()
		if mfErr := w.store.MarkFailed(wctx, p.RunID, runErr.Error()); mfErr != nil {
			log.Error("eval.run.mark_failed", "error", mfErr)
		}
		wcancel()
		observability.EvalRunsTotal.WithLabelValues("failed", kbLabel).Inc()
		observability.EvalRunDuration.WithLabelValues(strconv.FormatBool(run.JudgeEnabled), "failed").Observe(durationSeconds)
		log.Error("eval.run.failed", "error", runErr, "duration_s", durationSeconds)
		return nil // do not re-enqueue
	}

	wctx, wcancel := detachedWriteCtx()
	completeErr := w.store.MarkCompleted(wctx, p.RunID, reportJSON)
	wcancel()
	if completeErr != nil {
		log.Error("eval.run.mark_completed", "error", completeErr)
		// Rescue the row into a terminal state so it is not permanently stuck
		// in 'running'. The Delete handler refuses to delete running rows, so
		// without this the run would be orphaned until manual intervention.
		rwctx, rwcancel := detachedWriteCtx()
		if mfErr := w.store.MarkFailed(rwctx, p.RunID, fmt.Sprintf("mark_completed failed: %v", completeErr)); mfErr != nil {
			log.Error("eval.run.mark_failed_after_complete", "error", mfErr)
		}
		rwcancel()
		return nil
	}
	observability.EvalRunsTotal.WithLabelValues("completed", kbLabel).Inc()
	observability.EvalRunDuration.WithLabelValues(strconv.FormatBool(run.JudgeEnabled), "completed").Observe(durationSeconds)
	log.Info("eval.run.finished", "duration_s", durationSeconds, "kb_id", run.KBID)
	return nil
}

// EnqueueRun enqueues a TypeEvalRun task for runID. Called by the HTTP handler
// after inserting the eval_runs row with status 'queued'.
//
// Timeout is 2 h (generous ceiling for judge-mode runs at ~50-60 min). MaxRetry
// is 25 — but ONLY pre-execution contention paths (per-KB advisory lock not
// obtained; global slot cap reached; conn acquire failure) return a non-nil
// error from HandleRun, triggering asynq's exponential-backoff retry. Once
// MarkRunning succeeds, all subsequent failures call MarkFailed and return nil,
// so no post-execution retry ever fires. Raising MaxRetry from 0 makes the
// N-slot global cap functional: a run queued while all slots are busy will be
// retried automatically rather than stranded in 'queued' forever.
//
// client must satisfy the enqueuer interface; *asynq.Client does so automatically.
func EnqueueRun(ctx context.Context, client enqueuer, runID uuid.UUID) error {
	payload, err := json.Marshal(RunPayload{RunID: runID})
	if err != nil {
		return fmt.Errorf("enqueue eval run: marshal payload: %w", err)
	}
	_, err = client.EnqueueContext(ctx,
		asynq.NewTask(jobs.TypeEvalRun, payload),
		asynq.Queue(jobs.QueueHeavy),
		// Eval runs can take ~50 min in judge mode. 2h ceiling leaves generous
		// headroom; post-execution failures are recorded via MarkFailed (no retry).
		asynq.Timeout(2*time.Hour),
		// Retries apply only to pre-execution contention (per-KB lock / global
		// slot cap) and transient errors (conn acquire). Post-execution paths
		// always return nil so asynq never retries a started run.
		asynq.MaxRetry(25),
	)
	if err != nil {
		return fmt.Errorf("enqueue eval run: %w", err)
	}
	return nil
}
