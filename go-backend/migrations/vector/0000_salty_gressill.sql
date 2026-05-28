-- +goose Up
CREATE TABLE IF NOT EXISTS "document_chunks" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"file_id" uuid NOT NULL,
	"content" text NOT NULL,
	"embedding" vector(1536),
	"metadata" text,
	"created_at" timestamp DEFAULT now() NOT NULL
);

-- Safely convert existing jsonb embeddings to vector if needed
-- +goose StatementBegin
DO $$
BEGIN
    -- Check if column exists and is of type jsonb
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'document_chunks'
        AND column_name = 'embedding'
        AND data_type = 'jsonb'
    ) THEN
        -- Convert jsonb -> text -> vector
        ALTER TABLE "document_chunks"
        ALTER COLUMN "embedding"
        TYPE vector(1536)
        USING (embedding::text::vector(1536));
    END IF;
END $$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS "document_chunks_kb_id_idx" ON "document_chunks" USING btree ("kb_id");
CREATE INDEX IF NOT EXISTS "document_chunks_file_id_idx" ON "document_chunks" USING btree ("file_id");
CREATE INDEX IF NOT EXISTS "document_chunks_embedding_idx" ON "document_chunks" USING hnsw ("embedding" vector_cosine_ops);
