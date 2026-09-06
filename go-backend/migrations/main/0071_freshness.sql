-- +goose Up
-- Freshness surface (RAG SOTA review 2026-09-05, Wave 3 / Task 5).
--
-- files.published_at is the document's OWN publication date, as opposed to
-- created_at, which is the ingest timestamp. Only the RSS poller fills it
-- today (gofeed's PublishedParsed, W3-R9); every other origin leaves it NULL,
-- which is why every read site uses COALESCE(published_at, created_at) — the
-- single "effective date" expression shared by the recency boost, the
-- date-window filters (internal/vector/recency_boost.go), the recent_documents
-- MCP tool, the admin KB overview and the Home KB cards.
--
-- No index: the date-window queries filter by kb_id first and files is
-- per-KB small, so the existing kb_id index already bounds the scan. Adding
-- one would need CREATE INDEX CONCURRENTLY (files is a large table) for no
-- measured gain.
ALTER TABLE files ADD COLUMN IF NOT EXISTS published_at TIMESTAMPTZ;

-- last_success_at is stamped ONLY on a successful sync, unlike the existing
-- last_polled_at / last_synced_at columns, which every attempt overwrites —
-- including a failing one. That distinction is the whole point: the admin
-- overview's "letzte erfolgreiche Synchronisation" and the
-- rag_source_sync_age_seconds gauge must not go green because a broken feed
-- was retried a minute ago (W3-R10). Consumers fall back to the last-attempt
-- column while this is still NULL (no backfill — a source that has not
-- succeeded since the deploy has no verifiable success timestamp).
ALTER TABLE rss_feeds ADD COLUMN IF NOT EXISTS last_success_at TIMESTAMPTZ;
ALTER TABLE confluence_sources ADD COLUMN IF NOT EXISTS last_success_at TIMESTAMPTZ;
ALTER TABLE git_repo_sources ADD COLUMN IF NOT EXISTS last_success_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE git_repo_sources DROP COLUMN IF EXISTS last_success_at;
ALTER TABLE confluence_sources DROP COLUMN IF EXISTS last_success_at;
ALTER TABLE rss_feeds DROP COLUMN IF EXISTS last_success_at;
ALTER TABLE files DROP COLUMN IF EXISTS published_at;
