-- +goose Up
-- User file library, phase 1: one canonical, user-owned copy of each
-- uploaded file. A KB's indexed copy stays a files row (unchanged meaning)
-- and points here via files.user_file_id, sharing this row's blob.
--
-- ON DELETE RESTRICT on both foreign keys, deliberately: chunks, tabular
-- tables and KG rows live outside FK reach, so a cascading delete would
-- orphan them silently. internal/cascade deletes KB copies (with their
-- index) before the user_files row, and user_files before the users row;
-- a deleter that forgets a step fails loudly on RESTRICT instead.
CREATE TABLE IF NOT EXISTS user_files (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name          varchar(255) NOT NULL,
    mime          varchar(255) NOT NULL,
    size          bigint NOT NULL CHECK (size >= 0),
    sha256        char(64) NOT NULL,
    storage_path  text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- Per-user dedup only: cross-user dedup would let one user confirm that
    -- another already holds a given file (hash-confirmation side channel).
    CONSTRAINT user_files_owner_sha256_key UNIQUE (owner_user_id, sha256)
);
CREATE INDEX IF NOT EXISTS user_files_owner_created_idx ON user_files (owner_user_id, created_at DESC);

-- Per-user quota override; NULL = use site_config user_file_quota_bytes,
-- 0 = unlimited (same as the global default).
ALTER TABLE users ADD COLUMN IF NOT EXISTS file_quota_bytes bigint
    CHECK (file_quota_bytes IS NULL OR file_quota_bytes >= 0);

-- The KB copy's link back to the library file. NULL for every source-owned
-- file (rss, confluence, git) and for every non-upload ingest (text, url,
-- crawl, academic import) and for uploads that predate this migration.
-- The index lives in 0077 (CONCURRENTLY, files is a large table).
ALTER TABLE files ADD COLUMN IF NOT EXISTS user_file_id uuid REFERENCES user_files(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE files DROP COLUMN IF EXISTS user_file_id;
ALTER TABLE users DROP COLUMN IF EXISTS file_quota_bytes;
DROP TABLE IF EXISTS user_files;
