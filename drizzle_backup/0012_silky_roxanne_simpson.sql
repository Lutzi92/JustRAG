CREATE EXTENSION IF NOT EXISTS vector;--> statement-breakpoint
ALTER TABLE "document_chunks" ALTER COLUMN "embedding" SET DATA TYPE vector(1536);--> statement-breakpoint
CREATE INDEX "document_chunks_embedding_idx" ON "document_chunks" USING hnsw ("embedding" vector_cosine_ops);