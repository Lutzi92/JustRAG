-- Add a second BM25 tsvector built with the `simple` text-search config
-- (lowercase + tokenize, no stemming, no language-specific stop words).
--
-- Why: the German stemmer mangles many surnames and entity-anchored tokens
-- (Krüger → krug, Vorlage → vorlag, Möglichkeit → moglich) so chunks that
-- name an entity in a different morphological context score the same as
-- unrelated chunks. The simple arm preserves token surface form and lets
-- the keyword-search SQL OR-fuse against both indexes.
--
-- Idempotent: ALTER + index are IF NOT EXISTS, the backfill is a single
-- UPDATE that re-computes the column from existing content. The same
-- ALTER runs for every dim-keyed `document_chunks*` table via
-- EnsureChunkTable on worker/server boot — this migration only covers
-- the default 1536-dim table to keep the migration small and idempotent.

-- +goose Up
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
            'ALTER TABLE %I ADD COLUMN IF NOT EXISTS vector_index_simple tsvector',
            tbl
        );
        EXECUTE format(
            'CREATE INDEX IF NOT EXISTS %I ON %I USING gin (vector_index_simple)',
            tbl || '_vector_index_simple_idx', tbl
        );
        EXECUTE format(
            'UPDATE %I SET vector_index_simple = to_tsvector(''simple'', COALESCE(contextual_prefix, '''') || '' '' || content) WHERE content IS NOT NULL AND vector_index_simple IS NULL',
            tbl
        );
        RAISE NOTICE 'simple-tsvector ready on table %', tbl;
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
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
            'DROP INDEX IF EXISTS %I',
            tbl || '_vector_index_simple_idx'
        );
        EXECUTE format(
            'ALTER TABLE %I DROP COLUMN IF EXISTS vector_index_simple',
            tbl
        );
    END LOOP;
END $$;
-- +goose StatementEnd
