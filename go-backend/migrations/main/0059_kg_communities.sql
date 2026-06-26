-- +goose Up
-- KG community detection (GraphRAG-style): Leiden/Louvain communities over the
-- kg_edges relationship graph, each summarised. Membership lives here; the
-- summary text+embedding lives as a document_chunks row (node_kind=
-- 'community_summary') referenced by summary_chunk_id (soft ref — chunk tables
-- are dim-keyed in the vector DB, no cross-DB FK). Rebuilt per-KB (clear-then-
-- rebuild), so no per-file linkage is needed.
CREATE TABLE IF NOT EXISTS kg_communities (
    id                BIGSERIAL PRIMARY KEY,
    kb_id             UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    level             SMALLINT NOT NULL DEFAULT 0,
    member_entity_ids BIGINT[] NOT NULL,
    summary_chunk_id  UUID,
    size              INT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS kg_communities_kb_idx ON kg_communities (kb_id);

-- +goose Down
DROP TABLE IF EXISTS kg_communities;
