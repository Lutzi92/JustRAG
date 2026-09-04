-- +goose Up
-- Spreadsheet ingest rework, Phase 2 (spec docs/superpowers/specs/2026-09-04-spreadsheet-ingest-rework-design.md §4.4, §4.6, §5.1).
-- tabular_catalog gains the profiler's view of each materialised region; the
-- Phase-1 `columns` JSONB shape stays for readers that only know it.
ALTER TABLE tabular_catalog ADD COLUMN IF NOT EXISTS sheet_index  integer NOT NULL DEFAULT 0;
ALTER TABLE tabular_catalog ADD COLUMN IF NOT EXISTS region_index integer NOT NULL DEFAULT 0;
ALTER TABLE tabular_catalog ADD COLUMN IF NOT EXISTS sheet_kind   text    NOT NULL DEFAULT 'table';
ALTER TABLE tabular_catalog ADD COLUMN IF NOT EXISTS hidden       boolean NOT NULL DEFAULT false;
ALTER TABLE tabular_catalog ADD COLUMN IF NOT EXISTS header_row   integer;
ALTER TABLE tabular_catalog ADD COLUMN IF NOT EXISTS profile      jsonb;
ALTER TABLE tabular_catalog ADD COLUMN IF NOT EXISTS column_stats jsonb;

-- Distinct values of text-typed columns, for question-matched value lookup
-- (router, Phase 3). Bounded per column by tabular_column_values_max_distinct.
CREATE TABLE IF NOT EXISTS tabular_column_values (
    table_name  text    NOT NULL,
    column_name text    NOT NULL,
    value       text    NOT NULL,
    row_count   integer NOT NULL,
    PRIMARY KEY (table_name, column_name, value)
);
CREATE INDEX IF NOT EXISTS tabular_column_values_lower_idx
    ON tabular_column_values (table_name, column_name, lower(value));

-- Router SQL log (Phase 3 writes it; created now so the schema lands with the
-- catalog change and the read-only grants can be issued once).
CREATE TABLE IF NOT EXISTS tabular_query_log (
    id          bigserial PRIMARY KEY,
    kb_id       uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    message_id  uuid,
    question    text NOT NULL,
    sql         text,
    row_count   integer,
    outcome     text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tabular_query_log_kb_idx ON tabular_query_log (kb_id, created_at);

-- Per-file ingest report (spec §4.6) and a free-text progress detail for long
-- stages (spec §6.2). Both nullable, metadata-only ALTERs.
ALTER TABLE files ADD COLUMN IF NOT EXISTS parse_report jsonb;
ALTER TABLE files ADD COLUMN IF NOT EXISTS stage_detail text;

-- Operator note: the read-only role needs SELECT on tabular_column_values
-- (GRANT SELECT ON tabular_column_values TO <readonly_role>); nothing else
-- changes for the tabular-schema grants documented in 0048.

-- +goose Down
ALTER TABLE files DROP COLUMN IF EXISTS stage_detail;
ALTER TABLE files DROP COLUMN IF EXISTS parse_report;
DROP TABLE IF EXISTS tabular_query_log;
DROP TABLE IF EXISTS tabular_column_values;
ALTER TABLE tabular_catalog DROP COLUMN IF EXISTS column_stats;
ALTER TABLE tabular_catalog DROP COLUMN IF EXISTS profile;
ALTER TABLE tabular_catalog DROP COLUMN IF EXISTS header_row;
ALTER TABLE tabular_catalog DROP COLUMN IF EXISTS hidden;
ALTER TABLE tabular_catalog DROP COLUMN IF EXISTS sheet_kind;
ALTER TABLE tabular_catalog DROP COLUMN IF EXISTS region_index;
ALTER TABLE tabular_catalog DROP COLUMN IF EXISTS sheet_index;
