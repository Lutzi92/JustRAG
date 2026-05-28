-- +goose Up
ALTER TABLE "files" ADD COLUMN "progress_updated_at" timestamp;

-- +goose Down
ALTER TABLE "files" DROP COLUMN IF EXISTS "progress_updated_at";
