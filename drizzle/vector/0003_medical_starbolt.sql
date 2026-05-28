ALTER TABLE "document_chunks" ADD COLUMN "vector_index" "tsvector";--> statement-breakpoint
CREATE INDEX "document_chunks_vector_index_idx" ON "document_chunks" USING btree ("vector_index");