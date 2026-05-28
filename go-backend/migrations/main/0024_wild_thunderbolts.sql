-- +goose Up
CREATE UNIQUE INDEX "rss_feeds_kb_url_uniq" ON "rss_feeds" USING btree ("kb_id","url");

-- +goose Down
DROP INDEX IF EXISTS "rss_feeds_kb_url_uniq";
