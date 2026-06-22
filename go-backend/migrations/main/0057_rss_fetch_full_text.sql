-- +goose Up
ALTER TABLE rss_feeds ADD COLUMN IF NOT EXISTS fetch_full_text boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE rss_feeds DROP COLUMN IF EXISTS fetch_full_text;
