-- +goose Up
-- Per-file linkage for precise mindmap removal. kg_entities/kg_edges were
-- kb_id-scoped only, so a deleted file's KG contribution lingered forever.
-- file_id stamps each edge with its source file; kg_entity_files records
-- which files contributed each (deduped, cross-file-merged) entity so we
-- can GC an entity once its last contributing file is deleted.
ALTER TABLE kg_edges ADD COLUMN IF NOT EXISTS file_id UUID;

-- Supports DELETE FROM kg_edges WHERE kb_id=$1 AND file_id=$2 on file delete.
CREATE INDEX IF NOT EXISTS kg_edges_file_idx ON kg_edges (kb_id, file_id);

-- file_id is a soft reference (no hard FK to files.id), mirroring the pattern
-- used by kg_edges.chunk_id in 0044_kg_entities.sql. Per-file cleanup is
-- handled by the application file-delete hook (kg.DeleteKGForFile); KB-level
-- cleanup cascades through kg_entities.kb_id → the ON DELETE CASCADE on
-- entity_id above. A hard ON DELETE CASCADE on file_id would remove these link
-- rows before the delete hook runs, breaking the orphan-GC which needs them to
-- compute the affected entity set.
CREATE TABLE IF NOT EXISTS kg_entity_files (
    entity_id BIGINT NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    kb_id     UUID   NOT NULL,
    file_id   UUID   NOT NULL,
    PRIMARY KEY (entity_id, file_id)
);

-- Supports DELETE FROM kg_entity_files WHERE kb_id=$1 AND file_id=$2.
CREATE INDEX IF NOT EXISTS kg_entity_files_file_idx ON kg_entity_files (kb_id, file_id);

-- +goose Down
DROP INDEX IF EXISTS kg_entity_files_file_idx;
DROP TABLE IF EXISTS kg_entity_files;
DROP INDEX IF EXISTS kg_edges_file_idx;
ALTER TABLE kg_edges DROP COLUMN IF EXISTS file_id;
