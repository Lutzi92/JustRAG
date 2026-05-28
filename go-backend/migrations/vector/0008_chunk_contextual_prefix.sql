-- Contextual Retrieval (Anthropic-style): each chunk carries an LLM-generated
-- prefix (~50–100 tokens) describing where in the document it sits and what
-- it is about. Stored separately so the original chunk can still be returned
-- to the user verbatim, while the prefix is fed to both the embedding model
-- and the BM25 (tsvector) index.
--
-- The embedding text is `prefix + "\n\n" + content`; the tsvector is
-- `to_tsvector(<lang>, COALESCE(contextual_prefix, '') || ' ' || content)`,
-- computed at INSERT time by the Go processor (see chunks.go).
--
-- Idempotent via IF NOT EXISTS so re-runs after a partial apply, or an
-- out-of-band ALTER via EnsureChunkTable in schema.go, succeed as no-ops.
--
-- Non-default-dimension chunk tables (e.g. document_chunks_4096) get the
-- equivalent ALTER from EnsureChunkTable on every worker/migrate boot.

-- +goose Up
ALTER TABLE "document_chunks" ADD COLUMN IF NOT EXISTS "contextual_prefix" text;

-- +goose Down
ALTER TABLE "document_chunks" DROP COLUMN IF EXISTS "contextual_prefix";
