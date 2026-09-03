-- +goose Up
-- Night-window sync scheduling. Replaces the per-source interval columns
-- (rss_feeds.poll_interval, confluence_sources.sync_interval) with a three-
-- valued schedule read by internal/syncsched's sweeper. next_sync_at is the
-- next wall-clock instant the sweeper should enqueue this source; NULL means
-- "not yet stamped" and the sweeper stamps it without enqueuing, so a deploy
-- does not trigger a daytime mass sync.
--
-- The old interval columns are deliberately left in place and unread
-- (expand/contract); they are dropped in a later cleanup release.

ALTER TABLE rss_feeds ADD COLUMN IF NOT EXISTS sync_schedule TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE rss_feeds ADD COLUMN IF NOT EXISTS next_sync_at TIMESTAMPTZ;

ALTER TABLE confluence_sources ADD COLUMN IF NOT EXISTS sync_schedule TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE confluence_sources ADD COLUMN IF NOT EXISTS next_sync_at TIMESTAMPTZ;

ALTER TABLE git_repo_sources ADD COLUMN IF NOT EXISTS sync_schedule TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE git_repo_sources ADD COLUMN IF NOT EXISTS next_sync_at TIMESTAMPTZ;

-- CHECK constraints are added separately so a re-run is a no-op (Postgres has
-- no ADD CONSTRAINT IF NOT EXISTS).
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rss_feeds_sync_schedule_chk') THEN
        ALTER TABLE rss_feeds ADD CONSTRAINT rss_feeds_sync_schedule_chk
            CHECK (sync_schedule IN ('manual','daily','weekly'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'confluence_sources_sync_schedule_chk') THEN
        ALTER TABLE confluence_sources ADD CONSTRAINT confluence_sources_sync_schedule_chk
            CHECK (sync_schedule IN ('manual','daily','weekly'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'git_repo_sources_sync_schedule_chk') THEN
        ALTER TABLE git_repo_sources ADD CONSTRAINT git_repo_sources_sync_schedule_chk
            CHECK (sync_schedule IN ('manual','daily','weekly'));
    END IF;
END $$;
-- +goose StatementEnd

-- Backfill. Every active RSS feed was polled automatically before this
-- release, so it becomes daily; a feed the user explicitly paused
-- (status != 'active') must not be flipped onto a nightly schedule by this
-- one-shot, irreversible backfill. Confluence sources with an interval
-- become daily (including the weekly ones — one uniform target beats a
-- per-interval map). Git repositories were manual-only and stay manual.
UPDATE rss_feeds SET sync_schedule = 'daily' WHERE sync_schedule = 'manual' AND status = 'active';
UPDATE confluence_sources SET sync_schedule = 'daily'
 WHERE sync_schedule = 'manual' AND sync_interval IS NOT NULL AND sync_interval > 0;

-- +goose Down
ALTER TABLE rss_feeds DROP CONSTRAINT IF EXISTS rss_feeds_sync_schedule_chk;
ALTER TABLE confluence_sources DROP CONSTRAINT IF EXISTS confluence_sources_sync_schedule_chk;
ALTER TABLE git_repo_sources DROP CONSTRAINT IF EXISTS git_repo_sources_sync_schedule_chk;
ALTER TABLE rss_feeds DROP COLUMN IF EXISTS sync_schedule;
ALTER TABLE rss_feeds DROP COLUMN IF EXISTS next_sync_at;
ALTER TABLE confluence_sources DROP COLUMN IF EXISTS sync_schedule;
ALTER TABLE confluence_sources DROP COLUMN IF EXISTS next_sync_at;
ALTER TABLE git_repo_sources DROP COLUMN IF EXISTS sync_schedule;
ALTER TABLE git_repo_sources DROP COLUMN IF EXISTS next_sync_at;
