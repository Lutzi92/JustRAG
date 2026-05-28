-- +goose Up
ALTER TABLE "knowledge_bases" ADD COLUMN "chunk_size" integer;
ALTER TABLE "knowledge_bases" ADD COLUMN "chunk_overlap" integer;

-- +goose Down
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "chunk_overlap";
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "chunk_size";
