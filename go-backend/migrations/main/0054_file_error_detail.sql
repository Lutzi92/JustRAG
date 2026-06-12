-- +goose Up
-- Ingestion error visibility: failing stage + short sanitized user-facing
-- reason. NULL = no recorded reason (legacy error rows, or non-error status).
-- Raw error strings stay in logs (joinable via request_id) — never persisted.
ALTER TABLE files ADD COLUMN IF NOT EXISTS error_stage varchar(50);
ALTER TABLE files ADD COLUMN IF NOT EXISTS error_message text;

-- +goose Down
ALTER TABLE files DROP COLUMN IF EXISTS error_message;
ALTER TABLE files DROP COLUMN IF EXISTS error_stage;
