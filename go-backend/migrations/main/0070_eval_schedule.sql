-- +goose Up
-- Scheduled in-app eval runs (RAG SOTA review 2026-09-05, Wave 1 / R1).
-- Mirrors 0068's sync_schedule/next_sync_at: 'manual' = never scheduled;
-- next_run_at IS NULL on a non-manual row = "not yet stamped" (the sweeper
-- stamps without enqueuing, so enabling a schedule never fires a daytime run).
ALTER TABLE eval_golden_sets
  ADD COLUMN IF NOT EXISTS schedule text NOT NULL DEFAULT 'manual'
    CHECK (schedule IN ('manual', 'daily', 'weekly')),
  ADD COLUMN IF NOT EXISTS next_run_at timestamptz;

CREATE INDEX IF NOT EXISTS eval_golden_sets_due_idx
  ON eval_golden_sets (next_run_at) WHERE schedule <> 'manual';

-- Distinguishes sweeper-created runs from operator-triggered ones so the
-- regression delta compares like with like (W1-R4).
ALTER TABLE eval_runs
  ADD COLUMN IF NOT EXISTS scheduled boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS eval_runs_scheduled_completed_idx
  ON eval_runs (golden_set_id, finished_at DESC) WHERE scheduled AND status = 'completed';

-- +goose Down
DROP INDEX IF EXISTS eval_runs_scheduled_completed_idx;
ALTER TABLE eval_runs DROP COLUMN IF EXISTS scheduled;
DROP INDEX IF EXISTS eval_golden_sets_due_idx;
ALTER TABLE eval_golden_sets DROP COLUMN IF EXISTS next_run_at;
ALTER TABLE eval_golden_sets DROP COLUMN IF EXISTS schedule;
