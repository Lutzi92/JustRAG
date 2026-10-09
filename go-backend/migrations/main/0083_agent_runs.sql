-- +goose Up
-- One row per AG-UI run (agentic chat turn or workflow run). Tracks pending
-- interrupts so a resume can be validated (owner, expiry, exact interrupt
-- set) and claimed exactly once. Plan §6a.
CREATE TABLE IF NOT EXISTS agent_runs (
    id              uuid        PRIMARY KEY,
    thread_id       text        NOT NULL,
    app_name        text        NOT NULL,
    user_id         uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kb_id           uuid        NULL REFERENCES knowledge_bases(id) ON DELETE SET NULL,
    status          text        NOT NULL CHECK (status IN ('running','interrupted','completed','failed','cancelled','abandoned')),
    open_interrupts jsonb       NOT NULL DEFAULT '[]',
    expires_at      timestamptz NULL,
    error           text        NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_runs_thread_idx ON agent_runs (app_name, user_id, thread_id, created_at DESC);
-- At most one paused run per (user, thread) (plan §6a: one open interrupt set per thread).
CREATE UNIQUE INDEX IF NOT EXISTS agent_runs_one_open_per_thread ON agent_runs (app_name, user_id, thread_id) WHERE status = 'interrupted';
CREATE INDEX IF NOT EXISTS agent_runs_expiry_idx ON agent_runs (expires_at) WHERE status = 'interrupted';

-- +goose Down
DROP TABLE IF EXISTS agent_runs;
