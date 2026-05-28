-- +goose Up
CREATE INDEX "files_rss_feed_id_idx" ON "files" USING btree ("rss_feed_id");

-- +goose Down
DROP INDEX IF EXISTS "files_rss_feed_id_idx";
