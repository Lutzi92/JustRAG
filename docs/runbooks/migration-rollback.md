# Runbook: rolling back a database migration

`cmd/migrate` is **up-only** by design (plus `--status` / `--seed-only`); there is
deliberately no `--down` flag, so a rollback is always an explicit operator
action with the goose CLI, never something a mistyped deploy command can do.

Every migration in `go-backend/migrations/main/` and
`go-backend/migrations/vector/` ships a `-- +goose Down` section. Both trees
use goose's default `goose_db_version` bookkeeping table — they don't clash
because main and vector live in separate databases (on single-DB dev setups
the vector tree is skipped entirely, see `cmd/migrate/main.go`).

## Before you roll back

1. **Stop the app first.** `go-server` and `go-worker` assume the schema of the
   latest applied migration; running them against a rolled-back schema fails in
   undefined ways. The whole-run advisory lock (`internal/migrate/migrate.go`)
   only serializes *up* runs through `cmd/migrate` — the goose CLI does **not**
   take it, so nothing protects a `down` racing a concurrently starting pod.

   ```bash
   docker compose stop go-server go-worker
   ```

2. **Down migrations are destructive.** Most `+goose Down` sections are
   `DROP TABLE` / `DROP COLUMN` — e.g. rolling back 0052 drops `message_chunks`
   and all collected feedback signal with it. Take a backup if the data
   matters:

   ```bash
   pg_dump -h "$DB_HOST" -U "$DB_USER" -Fc "$DB_NAME" > pre-rollback.dump
   ```

3. **Check what is applied** before and after:

   ```bash
   cd go-backend && go run ./cmd/migrate --status
   ```

## Rolling back

Install the CLI once: `go install github.com/pressly/goose/v3/cmd/goose@latest`

Roll back the **main** tree one version:

```bash
goose -dir go-backend/migrations/main postgres \
  "postgres://$DB_USER:$DB_PASSWORD@$DB_HOST:$DB_PORT/$DB_NAME" down
```

Roll back the **vector** tree one version (separate-DB setups only):

```bash
goose -dir go-backend/migrations/vector postgres \
  "postgres://$VECTOR_DB_USER:$VECTOR_DB_PASSWORD@$VECTOR_DB_HOST:$VECTOR_DB_PORT/$VECTOR_DB_NAME" down
```

`down` reverts exactly one version per invocation. To revert several, use
`down-to <version>`, e.g. back to (and keeping) 0049:

```bash
goose -dir go-backend/migrations/main postgres "<dsn>" down-to 0049
```

## After rolling back

- Deploy the matching older application build **before** restarting anything —
  the current build expects the newer schema.
- Re-applying later is just the normal path: `go run ./cmd/migrate` (or the
  one-shot `migrate` compose service). Migrations are idempotent and the
  advisory lock makes concurrent re-application safe.
- If the rolled-back migration touched vector tables or embeddings
  (dimension-keyed `document_chunks_*`), check whether a re-ingest is needed —
  see `docs/runbooks/hnsw-reindex.md` for the index-rebuild side of that.
