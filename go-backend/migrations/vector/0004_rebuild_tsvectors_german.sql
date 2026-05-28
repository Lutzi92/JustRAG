-- +goose Up
-- Rebuild tsvector indexes using German text search configuration.
-- This updates all existing document_chunks* tables so that keyword search
-- uses German stemming instead of the previously hardcoded English config.
-- +goose StatementBegin
DO $$
DECLARE
    tbl TEXT;
BEGIN
    FOR tbl IN
        SELECT table_name
        FROM information_schema.tables
        WHERE table_schema = 'public'
          AND table_name LIKE 'document_chunks%'
    LOOP
        EXECUTE format(
            'UPDATE %I SET vector_index = to_tsvector(''german'', content) WHERE content IS NOT NULL',
            tbl
        );
        RAISE NOTICE 'Rebuilt tsvectors for table %', tbl;
    END LOOP;
END $$;
-- +goose StatementEnd
