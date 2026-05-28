-- +goose Up
CREATE TABLE IF NOT EXISTS "document_chunk_parents" (
    "id"                uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
    "kb_id"             uuid NOT NULL,
    "file_id"           uuid NOT NULL,
    "content"           text NOT NULL,
    "contextual_prefix" text,
    "metadata"          jsonb,
    "created_at"        timestamp DEFAULT now() NOT NULL
);
CREATE INDEX IF NOT EXISTS "document_chunk_parents_kb_id_idx" ON "document_chunk_parents" ("kb_id");
CREATE INDEX IF NOT EXISTS "document_chunk_parents_file_id_idx" ON "document_chunk_parents" ("file_id");

ALTER TABLE "document_chunks" ADD COLUMN IF NOT EXISTS "parent_chunk_id" uuid;
CREATE INDEX IF NOT EXISTS "document_chunks_parent_chunk_id_idx" ON "document_chunks" ("parent_chunk_id");

-- +goose Down
DROP INDEX IF EXISTS "document_chunks_parent_chunk_id_idx";
ALTER TABLE "document_chunks" DROP COLUMN IF EXISTS "parent_chunk_id";
DROP INDEX IF EXISTS "document_chunk_parents_file_id_idx";
DROP INDEX IF EXISTS "document_chunk_parents_kb_id_idx";
DROP TABLE IF EXISTS "document_chunk_parents";
