-- +goose NO TRANSACTION
-- +goose Up
-- Promised by 0075: deleting a users row applies ON DELETE SET NULL to
-- files.uploaded_by, which scanned all of files without this index.
CREATE INDEX CONCURRENTLY IF NOT EXISTS files_uploaded_by_idx
    ON files (uploaded_by) WHERE uploaded_by IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS files_uploaded_by_idx;
