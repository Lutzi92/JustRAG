# Migrations

Two independent goose trees:

- `main/` — the application database (users, KBs, files, messages, chats, KG, agents, …)
- `vector/` — the dim-keyed chunk tables (`document_chunks_2560`, `document_chunks_4096`, …)

`cmd/migrate` applies both; on a single-DB deployment it deliberately skips the
vector tree. Runs are serialized across replicas by a `pg_advisory_lock`, and
migrations are **up-only by design** — see `docs/runbooks/migration-rollback.md`
for the goose-CLI escape hatch when a rollback is genuinely required.

## Conventions

- **Numbered sequentially**, never renumbered once merged.
- **Idempotent**: `CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`,
  `CREATE INDEX IF NOT EXISTS`. A re-run of an applied migration must be a no-op.
- **Comment the intent**, not the syntax — say what the column is for and which
  code path reads it.

## Adding an index to an already-large table

Goose wraps each migration in a transaction, and a plain `CREATE INDEX` takes a
`SHARE` lock that **blocks every write to the table for the duration of the
build**. On a small or brand-new table that is invisible. On a table that is
already large in production it is a write stall in the middle of a deploy.

Treat these as large and always index them concurrently:

| Table | Why it grows |
|---|---|
| `document_chunks_*` (vector tree) | one row per chunk per ingested file |
| `files` | one row per ingested document; RSS/Confluence/git sources add continuously |
| `messages` | one row per chat turn |
| `kg_entities` / `kg_edges` | knowledge-graph extraction fans out per chunk |
| `message_chunks` | answer→chunk links, one row per cited chunk per answer |
| `agent_decisions` | one row per orchestrator decision |

`CREATE INDEX CONCURRENTLY` cannot run inside a transaction, so the migration
must opt out of goose's wrapper with the `NO TRANSACTION` directive on the very
first line (`vector/0009_mrl_backfill.sql` is an existing example of the
directive, used there for batched `COMMIT`s):

```sql
-- +goose NO TRANSACTION
-- +goose Up

-- Concurrent build: `files` is large in every production deployment, and a
-- plain CREATE INDEX would hold a SHARE lock — blocking ingest writes — for
-- the whole build. CONCURRENTLY trades a second table scan for no write lock.
CREATE INDEX CONCURRENTLY IF NOT EXISTS files_some_column_idx
    ON files (some_column);
```

Two consequences of `NO TRANSACTION` worth knowing before you use it:

1. **No automatic rollback.** If the migration contains several statements and
   one fails, the earlier ones stay applied. Keep such migrations to a single
   index build where possible.
2. **A failed concurrent build leaves an `INVALID` index behind.** It is not
   used by the planner but still costs write overhead. Detect and clean up:

   ```sql
   SELECT c.relname
   FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
   WHERE NOT i.indisvalid;

   DROP INDEX CONCURRENTLY <name>;   -- then re-run the migration
   ```

   The `IF NOT EXISTS` in the migration makes the re-run safe.

For a new table created in the same migration, keep the plain in-transaction
`CREATE INDEX` — the table is empty and nothing else can be writing to it yet.
