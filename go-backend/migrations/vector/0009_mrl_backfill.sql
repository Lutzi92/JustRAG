-- +goose NO TRANSACTION
-- +goose Up

-- Backfill the embedding_low column on every existing chunk table.
--
-- The column itself is provisioned via EnsureChunkTable on worker boot,
-- but a fresh DB that runs migrations before the worker starts won't have
-- it yet. The ALTER inside the DO block is defensive (IF NOT EXISTS).
--
-- Backfill is batched (5000 rows per UPDATE) and committed per batch so a
-- production-sized table can't trip the connection's statement_timeout
-- (default 120s in this project, see internal/database/database.go). The
-- batch loop is bounded by `WHERE embedding_low IS NULL`, so the migration
-- is idempotent and resumable: a previous partial run just continues from
-- where it left off.
--
-- Goose runs this migration with NO TRANSACTION (directive at top) so the
-- DO block can use COMMIT between batches. PG 11+ allows COMMIT inside a
-- DO block only when invoked at the top level (not inside an outer tx),
-- and only when no query-result cursor is currently open — hence the
-- table list is materialised into an array first and iterated with
-- FOREACH (which is in-memory) rather than a `FOR ... IN SELECT` cursor.

-- +goose StatementBegin
SET statement_timeout = 0;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    table_names TEXT[];
    table_name  TEXT;
    affected    BIGINT;
    total       BIGINT;
    batch_size  CONSTANT INT := 5000;
BEGIN
    SELECT array_agg(tablename ORDER BY tablename)
      INTO table_names
      FROM pg_tables
     WHERE schemaname = 'public'
       AND tablename ~ '^document_chunks(_[0-9]+)?$';

    IF table_names IS NULL THEN
        RAISE NOTICE 'No chunk tables found; skipping backfill';
        RETURN;
    END IF;

    FOREACH table_name IN ARRAY table_names LOOP
        -- Defensive: ensure the column exists before attempting the UPDATE.
        EXECUTE format(
            'ALTER TABLE %I ADD COLUMN IF NOT EXISTS embedding_low vector(256)',
            table_name
        );

        total := 0;
        LOOP
            EXECUTE format(
                'WITH batch AS (
                     SELECT ctid
                       FROM %I
                      WHERE embedding_low IS NULL AND embedding IS NOT NULL
                      LIMIT %s
                 )
                 UPDATE %I AS t
                    SET embedding_low = l2_normalize(subvector(t.embedding, 1, 256)::vector(256))
                   FROM batch
                  WHERE t.ctid = batch.ctid',
                table_name, batch_size, table_name
            );
            GET DIAGNOSTICS affected = ROW_COUNT;
            total := total + affected;
            EXIT WHEN affected = 0;
            COMMIT;
        END LOOP;

        RAISE NOTICE 'Backfilled % rows in %', total, table_name;
    END LOOP;
END$$;
-- +goose StatementEnd

-- +goose Down

-- Revert: NULL out the embedding_low column on every chunk table. The
-- column itself is left in place because it's part of the table schema
-- managed by EnsureChunkTable (which would re-add it on next worker boot
-- anyway). The HNSW index on embedding_low is preserved — it just has no
-- non-NULL values to index after this Down runs.
--
-- Same batching + NO TRANSACTION rationale as the Up.

-- +goose StatementBegin
SET statement_timeout = 0;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    table_names TEXT[];
    table_name  TEXT;
    affected    BIGINT;
    total       BIGINT;
    batch_size  CONSTANT INT := 5000;
BEGIN
    SELECT array_agg(tablename ORDER BY tablename)
      INTO table_names
      FROM pg_tables
     WHERE schemaname = 'public'
       AND tablename ~ '^document_chunks(_[0-9]+)?$';

    IF table_names IS NULL THEN
        RETURN;
    END IF;

    FOREACH table_name IN ARRAY table_names LOOP
        total := 0;
        LOOP
            EXECUTE format(
                'WITH batch AS (
                     SELECT ctid
                       FROM %I
                      WHERE embedding_low IS NOT NULL
                      LIMIT %s
                 )
                 UPDATE %I AS t
                    SET embedding_low = NULL
                   FROM batch
                  WHERE t.ctid = batch.ctid',
                table_name, batch_size, table_name
            );
            GET DIAGNOSTICS affected = ROW_COUNT;
            total := total + affected;
            EXIT WHEN affected = 0;
            COMMIT;
        END LOOP;
        RAISE NOTICE 'Cleared embedding_low on % rows in %', total, table_name;
    END LOOP;
END$$;
-- +goose StatementEnd
