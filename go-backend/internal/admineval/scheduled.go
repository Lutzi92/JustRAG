package admineval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/siteconfig"
	"github.com/justrag/go-backend/internal/store"
)

// scheduledRunTopK / scheduledRunLabel are fixed for sweeper-created runs
// (W1-R3): the regression gate is on retrieval metrics, judge mode is a
// nightly LLM bill nobody asked for.
const (
	scheduledRunTopK  = 10
	scheduledRunLabel = "scheduled"
)

// ScheduledWorker turns a due golden set into a normal eval_runs row and
// hands it to the existing TypeEvalRun executor. It exists so HandleRun
// stays the single place that runs evals (advisory-lock slots, timeouts).
type ScheduledWorker struct {
	runs      runStore
	sets      goldenSetStore
	cfg       siteConfigReader
	overrides kbOverrideLister // may be nil
	enq       enqueuer
}

// NewScheduledWorker constructs a ScheduledWorker. overrides may be nil (no
// per-KB overlay in the snapshot — same fallback as the KB-scoped handler).
func NewScheduledWorker(runs runStore, sets goldenSetStore, cfg siteConfigReader, overrides kbOverrideLister, enq enqueuer) *ScheduledWorker {
	return &ScheduledWorker{runs: runs, sets: sets, cfg: cfg, overrides: overrides, enq: enq}
}

// HandleScheduled is the asynq handler for TypeEvalScheduled. Everything
// that is not a transient DB error is terminal (SkipRetry): a bad payload,
// an unknown or manual set, or a KB that already has an active run — the
// sweeper stamped the next slot before enqueuing, so the next night retries
// naturally.
func (w *ScheduledWorker) HandleScheduled(ctx context.Context, t *asynq.Task) error {
	var p jobs.EvalScheduledPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("eval scheduled: unmarshal payload: %w: %w", err, asynq.SkipRetry)
	}
	gsID, err := uuid.Parse(p.GoldenSetID)
	if err != nil {
		return fmt.Errorf("eval scheduled: bad golden_set_id %q: %w", p.GoldenSetID, asynq.SkipRetry)
	}
	log := logctx.From(ctx).With("golden_set_id", gsID)

	gs, err := w.sets.Get(ctx, gsID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			log.Info("eval.scheduled.skipped", "reason", "golden_set_deleted")
			return nil
		}
		return fmt.Errorf("eval scheduled: load golden set: %w", err)
	}
	if gs.Schedule == "" || gs.Schedule == "manual" {
		log.Info("eval.scheduled.skipped", "reason", "schedule_manual")
		return nil
	}
	if gs.KBID == uuid.Nil {
		log.Warn("eval.scheduled.skipped", "reason", "golden_set_has_no_kb")
		return nil
	}
	active, err := w.runs.HasActiveRun(ctx, gs.KBID)
	if err != nil {
		return fmt.Errorf("eval scheduled: has active run: %w", err)
	}
	if active {
		log.Info("eval.scheduled.skipped", "reason", "kb_has_active_run", "kb_id", gs.KBID)
		return nil
	}

	// Snapshot the KB's effective config exactly like the KB-scoped
	// CreateRun handler does, so a scheduled run measures what users get.
	var snapReader siteConfigReader = w.cfg
	if w.overrides != nil {
		if ov, oerr := w.overrides.ListKBOverrides(ctx, gs.KBID.String()); oerr != nil {
			log.Warn("eval.scheduled.override_load_failed", "error", oerr)
		} else if len(ov) > 0 {
			snapReader = siteconfig.NewKBOverlay(w.cfg, ov)
		}
	}
	snap, err := captureConfigSnapshot(ctx, snapReader)
	if err != nil {
		return fmt.Errorf("eval scheduled: snapshot: %w", err)
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("eval scheduled: marshal snapshot: %w: %w", err, asynq.SkipRetry)
	}

	runID, err := w.runs.Insert(ctx, eval.Run{
		KBID:           gs.KBID,
		GoldenSetID:    &gs.ID,
		FixtureHash:    gs.ContentHash,
		ConfigSnapshot: json.RawMessage(snapJSON),
		JudgeEnabled:   false,
		TopK:           scheduledRunTopK,
		Label:          scheduledRunLabel,
		Scheduled:      true,
	})
	if err != nil {
		return fmt.Errorf("eval scheduled: insert run: %w", err)
	}
	if err := EnqueueRun(ctx, w.enq, runID); err != nil {
		if mfErr := w.runs.MarkFailed(ctx, runID, "enqueue failed: "+err.Error()); mfErr != nil {
			log.Error("eval.scheduled.mark_failed", "run_id", runID, "error", mfErr)
		}
		return fmt.Errorf("eval scheduled: enqueue run: %w", err)
	}
	log.Info("eval.scheduled.enqueued", "run_id", runID, "kb_id", gs.KBID)
	return nil
}
