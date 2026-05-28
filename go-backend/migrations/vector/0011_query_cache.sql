-- +goose Up
CREATE TABLE IF NOT EXISTS "query_cache" (
    "id"              uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
    "kb_id"           uuid NOT NULL,
    "shape_hash"      bytea NOT NULL,
    "query_embedding" vector(2560) NOT NULL,
    "query_text"      text NOT NULL,
    "result_json"     jsonb NOT NULL,
    "hit_count"       integer NOT NULL DEFAULT 0,
    "created_at"      timestamptz NOT NULL DEFAULT now(),
    "expires_at"      timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS "query_cache_kb_shape_idx" ON "query_cache" ("kb_id", "shape_hash");
CREATE INDEX IF NOT EXISTS "query_cache_expires_idx" ON "query_cache" ("expires_at");
-- pgvector HNSW caps `vector` at 2000 dims; the 2560-dim Qwen3-Embedding-4B
-- query vectors don't fit. The established workaround in this codebase
-- (see internal/vector/schema.go:180) is to cast to halfvec(2560) for the
-- index — halfvec supports up to 4000 dims. Storage stays full-precision
-- vector(2560); only the index uses the half-precision cast.
CREATE INDEX IF NOT EXISTS "query_cache_embedding_idx" ON "query_cache"
    USING hnsw ((query_embedding::halfvec(2560)) halfvec_cosine_ops);

-- +goose Down
DROP INDEX IF EXISTS "query_cache_embedding_idx";
DROP INDEX IF EXISTS "query_cache_expires_idx";
DROP INDEX IF EXISTS "query_cache_kb_shape_idx";
DROP TABLE IF EXISTS "query_cache";
