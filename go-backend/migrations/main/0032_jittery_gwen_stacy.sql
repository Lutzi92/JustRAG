-- +goose Up
ALTER TABLE "confluence_sources" ADD COLUMN "sync_progress" integer DEFAULT 0 NOT NULL;
ALTER TABLE "confluence_sources" ADD COLUMN "sync_total" integer DEFAULT 0 NOT NULL;

-- +goose Down
ALTER TABLE "confluence_sources" DROP COLUMN IF EXISTS "sync_total";
ALTER TABLE "confluence_sources" DROP COLUMN IF EXISTS "sync_progress";
