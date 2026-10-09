-- +goose NO TRANSACTION
-- +goose Up
-- Fuzzy matching for GET /api/search. internal/globalsearch matches topic
-- names, file names and chat titles with pg_trgm's word-similarity operator
-- (`q <% col`), next to an escaped substring ILIKE, and ranks
-- prefix > substring > fuzzy.
--
-- Why ONE file, and why NO TRANSACTION:
--   - files and chats are large tables (migrations/README.md), so their
--     indexes must be built CONCURRENTLY, which cannot run inside goose's
--     transaction wrapper. That forces NO TRANSACTION on the whole file.
--     CREATE EXTENSION runs fine outside a transaction.
--   - Every statement is idempotent (IF NOT EXISTS), so a run that fails half
--     way can simply be re-run. The one exception is the README's caveat: a
--     failed CONCURRENTLY build leaves an INVALID index that IF NOT EXISTS
--     then skips. Find and drop it before the re-run:
--       SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
--       WHERE NOT i.indisvalid;
--       DROP INDEX CONCURRENTLY <name>;
--   - No statement_timeout handling here: cmd/migrate connects with
--     statement_timeout=0 (internal/migrate.openSQL), so a long build is not
--     cancelled half way.
--
-- Run a SINGLE cmd/migrate instance for this migration (and 0085). cmd/migrate
-- serialises runs with a session advisory lock, and the lock's holder runs
-- this file — but a CONCURRENTLY build waits for every transaction in the
-- database that holds an older snapshot, and that includes a second
-- cmd/migrate blocked in pg_advisory_lock() waiting for this run. The two
-- wait on each other. Measured on Postgres 18.6: after deadlock_timeout the
-- deadlock detector aborts the WAITING run ("deadlock detected", 40P01), so
-- the second instance exits non-zero while the build completes. Concurrent
-- migrate runs are therefore not harmless here: start one (k8s: the single
-- migrate pod before the rollout; compose already runs one migrate service).
--
-- Privilege: CREATE EXTENSION pg_trgm requires the CREATE privilege on the
-- database (a database owner has it). pg_trgm is a trusted extension, so no
-- superuser is needed. A migration role without CREATE on the database fails
-- here; then a privileged role runs `CREATE EXTENSION pg_trgm;` once in that
-- database and the migration is re-run (IF NOT EXISTS skips it). This belongs
-- in the release's upgrade notes.
--
-- description / header_text are indexed as well, although search never
-- matches them fuzzily (long free text makes trigram similarity noise). The
-- index is for their existing substring ILIKE: the topics query ORs name with
-- description and header_text, and the planner can only turn an OR into a
-- BitmapOr when EVERY arm has an index. Without these two, the name index
-- would never be used. knowledge_bases is small, so the cost is small too.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX CONCURRENTLY IF NOT EXISTS knowledge_bases_name_trgm_idx
    ON knowledge_bases USING gin (name gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS knowledge_bases_description_trgm_idx
    ON knowledge_bases USING gin (description gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS knowledge_bases_header_text_trgm_idx
    ON knowledge_bases USING gin (header_text gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS files_name_trgm_idx
    ON files USING gin (name gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS chats_title_trgm_idx
    ON chats USING gin (title gin_trgm_ops);

-- +goose Down
-- Indexes first: DROP EXTENSION without CASCADE refuses while an index still
-- uses gin_trgm_ops. No CASCADE on purpose, so the Down fails loudly instead
-- of silently dropping something a later migration built on pg_trgm.
DROP INDEX CONCURRENTLY IF EXISTS chats_title_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS files_name_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS knowledge_bases_header_text_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS knowledge_bases_description_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS knowledge_bases_name_trgm_idx;
DROP EXTENSION IF EXISTS pg_trgm;
