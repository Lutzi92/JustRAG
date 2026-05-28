-- +goose Up
-- Phase D / AP-D1: long-term per-user memory.
--
-- The chat handler writes facts the LLM extracts at end-of-turn
-- (ai.ExtractSalientFacts) and reads them back at the start of
-- the next turn (longmem.Recall). Recall in this v1 cut is
-- recency- + salience-weighted; embedding-based ANN recall is a
-- v2 follow-up that needs its own dim-keyed-table dance.
--
-- Three kinds, mirroring the Mem0 taxonomy:
--   episodic — single-event facts ("user uploaded contract X today")
--   semantic — durable preferences / identity ("user prefers German answers")
--   procedural — workflow patterns ("user always asks about X before Y")
--
-- The CHECK constraint enforces the closed enum; a misbehaving
-- extractor that hallucinates a fresh kind will fail the insert
-- (logged + dropped — write path is fire-and-forget).
--
-- kb_id is nullable: NULL means "global to the user across all
-- KBs" (e.g. user identity facts). When non-null it scopes the
-- memory to one KB so a user's preferences for KB A don't bleed
-- into KB B.
--
-- The embedding column is NOT NULL by convention but the v1 cut
-- writes a zero-vector placeholder. v2's ANN-recall path will
-- backfill via the per-user re-embedding sweep.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS "user_memory" (
    "id"               BIGSERIAL PRIMARY KEY,
    "user_id"          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    "kb_id"            UUID REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    "kind"             TEXT NOT NULL CHECK (kind IN ('episodic', 'semantic', 'procedural')),
    "content"          TEXT NOT NULL,
    "embedding"        VECTOR(1536) NOT NULL,
    "salience"         REAL NOT NULL DEFAULT 1.0 CHECK (salience >= 0.0 AND salience <= 1.0),
    "created_at"       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    "last_accessed_at" TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    "access_count"     INTEGER NOT NULL DEFAULT 0,
    "superseded_by"    BIGINT REFERENCES user_memory(id) ON DELETE SET NULL
);

-- Per-user btree: the v1 recency-based recall fetches "all rows
-- for this user" so a user_id-prefix index is the right shape.
-- The optional kb_id filter at recall time uses a residual scan
-- on a tiny per-user row set — no separate composite index needed
-- until per-user memory counts grow above ~10k.
CREATE INDEX IF NOT EXISTS "user_memory_user_idx"
    ON "user_memory" ("user_id", "created_at" DESC);

-- HNSW index on the embedding column. The v1 recency-based recall
-- doesn't use it, but pre-creating the index means v2's ANN-recall
-- patch needs zero migration work. Only indexes non-superseded
-- rows so the supersession chain doesn't dilute search quality
-- (a partial index — pgvector >= 0.5 supports this).
CREATE INDEX IF NOT EXISTS "user_memory_embedding_idx"
    ON "user_memory" USING hnsw ("embedding" vector_cosine_ops)
    WHERE "superseded_by" IS NULL;

-- +goose Down
DROP INDEX IF EXISTS "user_memory_embedding_idx";
DROP INDEX IF EXISTS "user_memory_user_idx";
DROP TABLE IF EXISTS "user_memory";
