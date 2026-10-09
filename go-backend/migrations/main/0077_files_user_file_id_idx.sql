-- +goose NO TRANSACTION
-- +goose Up
-- A library file is in a given KB at most once, and "which KB copies does
-- this library file have" (delete fan-out, usage listing) is a lookup on
-- the leading column. One index serves both.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS files_user_file_kb_uidx
    ON files (user_file_id, kb_id) WHERE user_file_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS files_user_file_kb_uidx;
