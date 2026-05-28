-- +goose Up
CREATE TABLE eval_golden_sets (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name           text NOT NULL,
  description    text,
  content        jsonb NOT NULL,
  content_hash   text NOT NULL,
  question_count int NOT NULL,
  created_at     timestamp NOT NULL DEFAULT now(),
  created_by     uuid REFERENCES users(id)
);
CREATE INDEX eval_golden_sets_created_at_idx ON eval_golden_sets (created_at DESC);
CREATE UNIQUE INDEX eval_golden_sets_name_uniq ON eval_golden_sets (name);

ALTER TABLE eval_runs DROP COLUMN golden_path;
ALTER TABLE eval_runs ADD COLUMN golden_set_id uuid REFERENCES eval_golden_sets(id) ON DELETE SET NULL;
CREATE INDEX eval_runs_golden_set_id_idx ON eval_runs (golden_set_id);

-- +goose Down
DROP INDEX IF EXISTS eval_runs_golden_set_id_idx;
ALTER TABLE eval_runs DROP COLUMN IF EXISTS golden_set_id;
ALTER TABLE eval_runs ADD COLUMN golden_path text NOT NULL DEFAULT '';
ALTER TABLE eval_runs ALTER COLUMN golden_path DROP DEFAULT;
DROP INDEX IF EXISTS eval_golden_sets_name_uniq;
DROP INDEX IF EXISTS eval_golden_sets_created_at_idx;
DROP TABLE IF EXISTS eval_golden_sets;
