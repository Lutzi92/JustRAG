-- +goose Up
-- Fix 1: The original migration (0003) created a btree index on the tsvector
-- column, but the runtime query uses the @@ operator which requires GIN.
-- Recreate as GIN on all document_chunks* tables.
--
-- Fix 2: The metadata column was defined as text but the Go code casts values
-- as ::jsonb on insert and uses @> jsonb containment in filters. Alter to jsonb
-- so operators work natively without implicit casts.

-- +goose StatementBegin
DO $$
DECLARE
    tbl TEXT;
BEGIN
    FOR tbl IN
        SELECT table_name FROM information_schema.tables
        WHERE table_schema = 'public'
          AND table_name LIKE 'document_chunks%'
    LOOP
        -- Fix btree→GIN index on vector_index column.
        IF EXISTS (
            SELECT 1 FROM pg_indexes
            WHERE tablename = tbl
              AND indexname = tbl || '_vector_index_idx'
              AND indexdef LIKE '%btree%'
        ) THEN
            EXECUTE format('DROP INDEX IF EXISTS "%s_vector_index_idx"', tbl);
            EXECUTE format('CREATE INDEX "%s_vector_index_idx" ON "%s" USING gin ("vector_index")', tbl, tbl);
        ELSIF NOT EXISTS (
            SELECT 1 FROM pg_indexes
            WHERE tablename = tbl
              AND indexname = tbl || '_vector_index_idx'
        ) THEN
            -- Index doesn't exist at all (new table created by vectorsetup.go already has GIN).
            EXECUTE format('CREATE INDEX IF NOT EXISTS "%s_vector_index_idx" ON "%s" USING gin ("vector_index")', tbl, tbl);
        END IF;

        -- Fix text→jsonb metadata column.
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_name = tbl
              AND column_name = 'metadata'
              AND data_type = 'text'
        ) THEN
            EXECUTE format('ALTER TABLE "%s" ALTER COLUMN "metadata" TYPE jsonb USING CASE WHEN "metadata" IS NULL THEN NULL WHEN "metadata" = '''' THEN NULL ELSE "metadata"::jsonb END', tbl);
        END IF;
    END LOOP;
END $$;
-- +goose StatementEnd
