-- +goose Up
-- Git repository source tracking: per-KB git repos synced into the KB.
-- Files ingested from a repo carry git_repo_source_id, git_file_path, and
-- git_blob_sha so incremental re-sync can skip unchanged blobs.
CREATE TABLE IF NOT EXISTS git_repo_sources (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id                  UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    repo_url               TEXT NOT NULL,
    is_private             BOOLEAN NOT NULL DEFAULT false,
    access_token_encrypted TEXT,
    branch                 TEXT,
    status                 VARCHAR(50) NOT NULL DEFAULT 'active',
    error_message          TEXT,
    consecutive_failures   INTEGER NOT NULL DEFAULT 0,
    last_synced_at         TIMESTAMP,
    last_commit_sha        VARCHAR(40),
    file_count             INTEGER NOT NULL DEFAULT 0,
    sync_progress          INTEGER NOT NULL DEFAULT 0,
    sync_total             INTEGER NOT NULL DEFAULT 0,
    created_at             TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS git_repo_sources_kb_id_idx ON git_repo_sources(kb_id);

ALTER TABLE files ADD COLUMN IF NOT EXISTS git_repo_source_id UUID REFERENCES git_repo_sources(id) ON DELETE CASCADE;
ALTER TABLE files ADD COLUMN IF NOT EXISTS git_file_path TEXT;
ALTER TABLE files ADD COLUMN IF NOT EXISTS git_blob_sha VARCHAR(40);

CREATE INDEX IF NOT EXISTS files_git_repo_source_id_idx ON files(git_repo_source_id);

-- +goose Down
DROP INDEX IF EXISTS files_git_repo_source_id_idx;
ALTER TABLE files DROP COLUMN IF EXISTS git_blob_sha;
ALTER TABLE files DROP COLUMN IF EXISTS git_file_path;
ALTER TABLE files DROP COLUMN IF EXISTS git_repo_source_id;
DROP TABLE IF EXISTS git_repo_sources;
