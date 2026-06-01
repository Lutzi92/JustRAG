-- +goose Up
CREATE TABLE eval_golden_set_jobs (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kb_id         uuid NOT NULL,
  status        text NOT NULL DEFAULT 'queued',
  params        jsonb NOT NULL,
  golden_set_id uuid REFERENCES eval_golden_sets(id) ON DELETE SET NULL,
  error         text,
  created_at    timestamp NOT NULL DEFAULT now(),
  created_by    uuid REFERENCES users(id)
);
CREATE INDEX eval_golden_set_jobs_status_idx ON eval_golden_set_jobs (status, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS eval_golden_set_jobs;
