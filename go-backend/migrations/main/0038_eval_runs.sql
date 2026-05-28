-- +goose Up
CREATE TABLE eval_runs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at      timestamp NOT NULL DEFAULT now(),
  started_at      timestamp,
  finished_at     timestamp,
  status          text NOT NULL CHECK (status IN ('queued','running','completed','failed')),
  triggered_by    uuid REFERENCES users(id),
  kb_id           uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
  golden_path     text NOT NULL,
  fixture_hash    text NOT NULL,
  config_snapshot jsonb NOT NULL,
  judge_enabled   bool NOT NULL DEFAULT true,
  top_k           int  NOT NULL DEFAULT 10,
  report          jsonb,
  error_message   text,
  label           text
);
CREATE INDEX eval_runs_created_at_idx ON eval_runs (created_at DESC);
CREATE INDEX eval_runs_status_active_idx ON eval_runs (status) WHERE status IN ('queued','running');

-- +goose Down
DROP TABLE IF EXISTS eval_runs;
