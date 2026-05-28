-- +goose Up
-- Phase 1: structured tabular store for large spreadsheets (table_query tool).
-- Per-sheet typed tables live in the dedicated `tabular` schema; this catalog
-- maps each materialized sheet to its physical table + column metadata and is
-- the source of truth for tool-side discovery, per-request scoping, and
-- cascade cleanup. Design: docs/superpowers/specs/2026-05-28-tabular-data-qa-design.md
--
-- OPERATOR PREREQUISITE for the read-only role behind JUSTRAG_DB_URL_READONLY
-- (the sql_query / table_query pool). Run once, as the table owner:
--   GRANT SELECT ON tabular_catalog TO <readonly_role>;   -- discovery + allowlist build
--   GRANT USAGE ON SCHEMA tabular TO <readonly_role>;
--   GRANT SELECT ON ALL TABLES IN SCHEMA tabular TO <readonly_role>;
--   ALTER DEFAULT PRIVILEGES FOR ROLE <materializer_role> IN SCHEMA tabular
--       GRANT SELECT ON TABLES TO <readonly_role>;
-- The GRANT on tabular_catalog (public schema) is REQUIRED: the table_query
-- tool reads the catalog through the read-only pool to discover sheets and
-- build the per-KB allowlist; without it every table_query call fails.
-- The ALTER DEFAULT PRIVILEGES line is REQUIRED so per-sheet tables created
-- AFTER this migration are readable by the tool's role. <materializer_role>
-- is the role the Go worker/server connects as (the DB_USER), since that role
-- creates the per-sheet tables.
--
-- SECURITY: do NOT add the `tabular` schema to the read-only role's
-- search_path. Per-KB isolation is enforced by the table_query tool requiring
-- schema-qualified `tabular.<name>` references and allowlisting them per KB;
-- an unqualified name must fail to resolve. Keeping `tabular` out of
-- search_path is what makes a bare-name reference error instead of leaking.

CREATE SCHEMA IF NOT EXISTS tabular;

CREATE TABLE IF NOT EXISTS "tabular_catalog" (
    "id"             BIGSERIAL PRIMARY KEY,
    "kb_id"          UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    "file_id"        UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    "sheet_name"     TEXT NOT NULL,
    "table_name"     TEXT NOT NULL UNIQUE,
    "columns"        JSONB NOT NULL,
    "row_count"      BIGINT NOT NULL DEFAULT 0,
    "coercion_stats" JSONB,
    "created_at"     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS "tabular_catalog_kb_idx"   ON "tabular_catalog" ("kb_id");
CREATE INDEX IF NOT EXISTS "tabular_catalog_file_idx" ON "tabular_catalog" ("file_id");

-- +goose Down
DROP TABLE IF EXISTS "tabular_catalog";
DROP SCHEMA IF EXISTS tabular CASCADE;
