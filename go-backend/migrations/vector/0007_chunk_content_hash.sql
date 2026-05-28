-- Pre-embed chunk deduplication. Adds a content_hash column on the default
-- 1536-dim chunk table and an index keyed by (kb_id, content_hash) so the
-- ingestion path can short-circuit duplicate chunks without re-embedding.
--
-- Idempotent via IF NOT EXISTS so re-runs after a partial apply, or an
-- out-of-band ALTER via internal/migrate/vectorsetup.go, succeed as no-ops.
--
-- Non-default-dimension chunk tables (e.g. document_chunks_4096) are handled
-- automatically by EnsureVectorTables on every worker/migrate boot, which
-- applies the equivalent ALTER + CREATE INDEX idempotently.

-- +goose Up
ALTER TABLE "document_chunks" ADD COLUMN IF NOT EXISTS "content_hash" text;
CREATE INDEX IF NOT EXISTS "document_chunks_kb_content_hash_idx" ON "document_chunks" ("kb_id", "content_hash");

-- +goose Down
DROP INDEX IF EXISTS "document_chunks_kb_content_hash_idx";
ALTER TABLE "document_chunks" DROP COLUMN IF EXISTS "content_hash";
