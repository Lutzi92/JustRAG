-- +goose Up
-- User file library, phase 0: who added this file. Set by every
-- user-initiated ingest path (upload, text, url, crawl, academic import);
-- NULL for source-owned files (rss, confluence, git) and for every
-- row that predates this migration — the uploader was never recorded, so
-- there is nothing to backfill from.
--
-- ON DELETE SET NULL for now: phase 1 deletes a user's files explicitly via
-- internal/cascade before the user row goes, so the FK action is only a
-- safety net and must never cascade-delete files rows behind the deleter's
-- back (chunks / tabular tables / KG rows live outside FK reach).
--
-- No index yet: nothing filters on it in phase 0. Phase 1 adds one
-- (CONCURRENTLY, NO TRANSACTION) together with the first query that needs it.
-- Until then each users DELETE scans files to apply ON DELETE SET NULL
-- (cheap at current sizes).
ALTER TABLE files ADD COLUMN IF NOT EXISTS uploaded_by uuid REFERENCES users(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE files DROP COLUMN IF EXISTS uploaded_by;
