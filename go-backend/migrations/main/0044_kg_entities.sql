-- +goose Up
-- Phase C / AP-C1: Knowledge-Graph storage. Per-chunk LLM extraction
-- (kg_extraction_enabled, default off) populates two tables:
--
--   kg_entities — canonical entity rows, dedupe-keyed on
--                 (kb_id, canonical_name, type). The aliases column
--                 captures variant surface forms the LLM saw across
--                 chunks ("PPM", "PPM-Team") so graph_search can
--                 match a query token against any alias before
--                 traversing.
--
--   kg_edges    — relations between entities, one row per evidence
--                 occurrence (multiple edges between the same
--                 (src, dst) are allowed because evidence is per-
--                 chunk). chunk_id is FK-soft to document_chunks*
--                 (those tables are dim-keyed, so we can't FK them
--                 directly — the soft reference is documented and
--                 cleanup happens via the per-KB delete cascade
--                 from kg_entities → kg_edges).
--
-- The embedding column on kg_entities is forward-compat for the
-- "0.92 cosine threshold disambiguation" the plan describes; the
-- C1 cut leaves it NULL and dedupe runs purely on (canonical_name,
-- type). Promotion to embedding-based merge is a follow-up that
-- needs its own dim-keyed-table dance (entities embed in whatever
-- dim the KB's embedding model uses).
--
-- pgvector is required for the embedding column type. The same
-- extension already enables document_chunks_*; we reuse it here.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS "kg_entities" (
    "id"             BIGSERIAL PRIMARY KEY,
    "kb_id"          UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    "canonical_name" TEXT NOT NULL,
    "type"           TEXT NOT NULL,
    "aliases"        TEXT[] NOT NULL DEFAULT '{}',
    "embedding"      VECTOR(1536),  -- forward-compat; NULL in the C1 cut
    "created_at"     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    "updated_at"     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (kb_id, canonical_name, type)
);

CREATE INDEX IF NOT EXISTS "kg_entities_kb_idx"
    ON "kg_entities" ("kb_id");

-- GIN index on aliases for the AP-C4 router heuristic: given a
-- query token, find every entity that has it as an alias. Without
-- this the router would do a sequential scan per chat turn.
CREATE INDEX IF NOT EXISTS "kg_entities_aliases_gin"
    ON "kg_entities" USING GIN ("aliases");

CREATE TABLE IF NOT EXISTS "kg_edges" (
    "id"             BIGSERIAL PRIMARY KEY,
    "kb_id"          UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    "src_entity_id"  BIGINT NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    "dst_entity_id"  BIGINT NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    "rel"            TEXT NOT NULL,
    "evidence"       TEXT,
    -- chunk_id intentionally NOT a hard FK — document_chunks_NNNN
    -- tables are dim-keyed so a single FK constraint can't address
    -- the right table. Cleanup is via cascading from kg_entities
    -- on KB delete; orphan edges from chunk re-ingest are tolerated
    -- (they still carry valid evidence text) until the next
    -- per-KB sweep.
    "chunk_id"       UUID,
    "weight"         REAL NOT NULL DEFAULT 1.0,
    "created_at"     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS "kg_edges_src_idx"
    ON "kg_edges" ("kb_id", "src_entity_id");
CREATE INDEX IF NOT EXISTS "kg_edges_dst_idx"
    ON "kg_edges" ("kb_id", "dst_entity_id");

-- +goose Down
DROP INDEX IF EXISTS "kg_edges_dst_idx";
DROP INDEX IF EXISTS "kg_edges_src_idx";
DROP TABLE IF EXISTS "kg_edges";
DROP INDEX IF EXISTS "kg_entities_aliases_gin";
DROP INDEX IF EXISTS "kg_entities_kb_idx";
DROP TABLE IF EXISTS "kg_entities";
