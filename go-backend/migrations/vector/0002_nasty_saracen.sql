-- +goose Up
DROP INDEX "document_chunks_embedding_idx";
ALTER TABLE "document_chunks" ALTER COLUMN "embedding" SET DATA TYPE vector;
