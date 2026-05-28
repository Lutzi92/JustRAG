-- +goose Up
ALTER TABLE "knowledge_bases" ADD COLUMN "is_published" boolean DEFAULT true NOT NULL;

-- +goose Down
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "is_published";
