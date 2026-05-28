-- +goose Up
-- Duplicate migration cleaned up (content was already in 0014 and 0015)

-- +goose Down
-- No-op: Up was already a no-op.
SELECT 1;
