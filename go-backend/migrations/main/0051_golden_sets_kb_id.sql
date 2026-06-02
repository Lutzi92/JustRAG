-- +goose Up
ALTER TABLE eval_golden_sets ADD COLUMN IF NOT EXISTS kb_id uuid;

-- +goose Down
ALTER TABLE eval_golden_sets DROP COLUMN IF EXISTS kb_id;
