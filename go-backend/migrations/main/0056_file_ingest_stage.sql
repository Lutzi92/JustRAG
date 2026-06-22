-- +goose Up
-- Per-file ingestion stage for the upload-section spinner + n/x indicator.
-- current_stage spans the WHOLE pipeline, including the post-`completed`
-- KG/HyPE/RAPTOR tail, and is NULL when the file is not actively ingesting.
-- stage_index is 1-based; stage_total is the static `x` (enabled stages for
-- this file). NULL on all three = idle / done / legacy rows.
ALTER TABLE files ADD COLUMN IF NOT EXISTS current_stage text;
ALTER TABLE files ADD COLUMN IF NOT EXISTS stage_index integer;
ALTER TABLE files ADD COLUMN IF NOT EXISTS stage_total integer;

-- +goose Down
ALTER TABLE files DROP COLUMN IF EXISTS stage_total;
ALTER TABLE files DROP COLUMN IF EXISTS stage_index;
ALTER TABLE files DROP COLUMN IF EXISTS current_stage;
