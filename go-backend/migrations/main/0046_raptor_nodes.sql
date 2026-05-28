-- +goose Up
-- Phase F: RAPTOR (Recursive Abstractive Processing for Tree-Organized
-- Retrieval) — per-file summary trees built at ingest. Leaves cluster
-- via K-means; each cluster gets a fast-tier LLM summary that is
-- embedded and stored as a regular chunk row tagged node_kind='summary'.
-- Leaves and summaries share one table so the unchanged BM25 + vector +
-- reranker pipeline scores them identically (collapsed-tree retrieval).
--
-- node_kind defaults to 'leaf' so every pre-existing row is treated as
-- a leaf without backfill. tree_level=0 for leaves, 1..N for summaries.
-- raptor_parent_id is the in-table FK to the parent summary; NULL on
-- roots and on leaves until the builder back-patches them after
-- inserting the parent summary.
--
-- This migration only ALTERs the legacy 1536-dim "document_chunks"
-- table. internal/vector/schema.go mirrors the same three ALTERs onto
-- every dim-keyed document_chunks_<dim> table at worker boot
-- (idempotent ADD COLUMN IF NOT EXISTS) — same pattern used by
-- migrations 0007 (content_hash), 0008 (contextual_prefix), 0010
-- (parent_chunk_id), and 0012 (vector_index_simple).
--
-- On Go-only fresh deployments the legacy 1536-dim "document_chunks"
-- table is never created (the worker bootstraps dim-keyed tables like
-- "document_chunks_4096" at boot). Guard every statement with a
-- to_regclass check so the migration is a no-op when the legacy table
-- is absent.

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.document_chunks') IS NOT NULL THEN
        ALTER TABLE document_chunks ADD COLUMN IF NOT EXISTS node_kind        text     NOT NULL DEFAULT 'leaf';
        ALTER TABLE document_chunks ADD COLUMN IF NOT EXISTS tree_level       smallint NOT NULL DEFAULT 0;
        ALTER TABLE document_chunks ADD COLUMN IF NOT EXISTS raptor_parent_id uuid;

        -- Partial index — the leaf majority has raptor_parent_id NULL and we
        -- never need to look those up by parent. The descendant-fetch recursive
        -- CTE (chat post-response citation validation) walks down the tree
        -- starting from a known summary id, so the index serves the JOIN.
        CREATE INDEX IF NOT EXISTS document_chunks_raptor_parent_idx
            ON document_chunks (raptor_parent_id) WHERE raptor_parent_id IS NOT NULL;

        -- Compound covers the eval-only NodeKindFilter ablation and the
        -- recursive CTE's entry-point filter.
        CREATE INDEX IF NOT EXISTS document_chunks_kind_kb_file_idx
            ON document_chunks (kb_id, file_id, node_kind);
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.document_chunks') IS NOT NULL THEN
        DROP INDEX IF EXISTS document_chunks_kind_kb_file_idx;
        DROP INDEX IF EXISTS document_chunks_raptor_parent_idx;
        ALTER TABLE document_chunks DROP COLUMN IF EXISTS raptor_parent_id;
        ALTER TABLE document_chunks DROP COLUMN IF EXISTS tree_level;
        ALTER TABLE document_chunks DROP COLUMN IF EXISTS node_kind;
    END IF;
END $$;
-- +goose StatementEnd
