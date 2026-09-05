# Runbook — operating the spreadsheet ingest pipeline

**When to use:** sizing `worker-heavy` for spreadsheet ingest, diagnosing an oversize-upload 413, a spreadsheet stuck in "processing", an orphaned `tabular.sheet_*` table, or running the Phase 4 acceptance procedure before flipping `chat_tabular_query_enabled`/`chat_tabular_router_enabled` in production.

Mechanism reference: `docs/retrieval.md`'s "Spreadsheets" section (the router's nine steps, metrics, known limits) and `docs/feature-recipes.md`'s "Structured spreadsheet Q&A (table_query)" recipe (the full enablement block and operator grants). This runbook is ops-only — sizing, alerting, cleanup, and the acceptance run.

---

## 1. Sizing `worker-heavy`

`k8s/worker-heavy.yml` requests 512Mi / limits 2Gi for the pod that runs spreadsheet ingest (`WORKER_QUEUES` includes `rag-heavy`, where `TypeFileProcessing` for a spreadsheet lands). The arithmetic behind the 2Gi limit (from the manifest's own comment):

- **Streaming reader baseline:** ~100 MB RSS for a 125 MB workbook (`sheetsource` streams, it does not buffer the whole file).
- **Large-file gate:** at most `tabular_large_file_concurrency` (default **1**) spreadsheet(s) above `tabular_large_file_bytes` (default 20 MiB) materialise concurrently per worker process — this is what keeps the streaming-reader cost from multiplying under a burst of large uploads.
- **Hybrid-text render window:** `tabular/render.RenderSheet` buffers one window of up to `tabular_embed_max_rows` rows (max 100 000, Ruling R23) per table region in memory before writing a line.
- **Tesseract OCR** (unrelated to spreadsheets, but shares the pod): CPU-bound, 2 threads × 2 concurrent.
- With `WORKER_CONCURRENCY=4` and one large-file slot, worst case stays well under 2Gi.

**If you raise `tabular_large_file_concurrency`** (up to 8): each additional concurrent large-file slot adds roughly one streaming-reader baseline's worth of RSS (scale with your actual large-file size distribution, not the 125 MB reference workbook) — raise the pod's memory limit before raising the concurrency knob, not after. The concurrency value is read **once at worker process startup** into a `*processor.LargeFileGate` semaphore (`internal/processor/large_file_gate.go`) — an admin who changes it in the site-config UI needs a worker restart (rolling restart is fine) before it takes effect; the size threshold (`tabular_large_file_bytes`) itself is read fresh per file, so *that* knob is safe to retune live.

**If you raise `tabular_embed_max_rows`** toward its 100 000 ceiling: that many rows sit in the render window per table region, per concurrently-ingesting spreadsheet. This is why the ceiling exists at all — see `docs/retrieval.md`'s Spreadsheets section for the full rationale (incremental rendering is a future-phase fix, not yet built).

---

## 2. Upload limits and the 413

Two independent size gates apply to a spreadsheet upload, and they do not agree by default:

1. **Transport-wide cap** — `http.MaxBytesReader` in `internal/files/http.go`, a package `var` (not a `const` — `maxUploadSize = 500 << 20`, i.e. exactly 500 MiB; it is a `var` only so a test can shrink it via `SetMaxUploadSizeForTest`, never mutated in production). It applies to **every** upload of **every** file type and is not a `site_config` key — there is no admin-UI knob for it. Exceeding it answers 413 with `"File too large: the upload limit is 500 MB"` (`humanBytes` strips the trailing `.0`; the message tracks whatever `maxUploadSize` currently is).
2. **Spreadsheet-specific cap** — `tabular_max_file_bytes` (default 524,288,000 bytes = 500 MiB exactly, range 1 MiB – ~2 GiB), checked only for a file the spreadsheet parser recognises (`.xlsx`/`.xls`/`.ods`/`.csv`/`.tsv` — never `.xlsm`). Exceeding it answers 413 with `"Spreadsheet too large (<size>): the limit is <limit> (tabular_max_file_bytes)"`.

**Operator gotcha:** the two defaults happen to be numerically identical (500 MiB), which can read as "the spreadsheet knob controls the ceiling." It does not, above that point. Raising `tabular_max_file_bytes` past 500 MiB has **no effect** — the transport-wide `MaxBytesReader` still rejects the body at 500 MiB before the spreadsheet-specific check ever runs. To actually allow spreadsheets larger than 500 MiB, the `maxUploadSize` var in `internal/files/http.go` must also be raised and the binary rebuilt/redeployed; there is no runtime override. Lowering `tabular_max_file_bytes` below 500 MiB works as expected (it is the tighter of the two gates in that direction).

A non-spreadsheet file is governed by the transport cap alone, plus the pre-existing per-KB file-count/total-size limits (unrelated to this feature).

---

## 3. The large-file gate and its stage-detail signal

A spreadsheet whose on-disk size exceeds `tabular_large_file_bytes` (default 20 MiB) must acquire one of `tabular_large_file_concurrency` (default 1) slots on its worker process before ingest proceeds; small spreadsheets bypass the gate entirely. While a file waits on a slot, `files.stage_detail` reports:

- German KB: `Wartet auf einen Slot für große Dateien`
- otherwise: `Waiting for a large-file slot`

— visible in the sidebar's sources panel and via `GET /api/kb/{id}/files`. A stat failure on the on-disk file is treated as "small" (fails open, does not block ingest). Log line on slot acquisition: `tabular.largefile.acquired` (fields `file_id`, `bytes`).

**Diagnosing a stuck-looking large upload:** if several large spreadsheets land in the same KB/worker at once and `tabular_large_file_concurrency=1`, later ones queue behind the first — this is expected serialization, not a hang. Confirm via the `stage_detail` message above; if it persists far longer than the file's expected ingest time, check the worker pod's logs for the `tabular.ingest.done` line (or its absence) on the file actually holding the slot, and confirm the pod hasn't OOMKilled (§1).

---

## 4. Inspecting a file's ingest result — the "Tabellen" panel

`GET /api/kb/{id}/files/{fileId}/tabular` (view role on the KB) returns the persisted `files.parse_report` plus the file's `tabular_catalog` rows, joined into `tabular.FileTabularDTO` — per-sheet region boundaries, column roles/types, null/distinct counts, coercion failures, and (capped, instruction-filtered) sample/value-set previews. 404 if the file doesn't belong to the KB; a normal `200 {"report":null,"tables":[]}` for a non-spreadsheet file (not an error). The frontend's "Tabellen" modal on the sources list opens this endpoint per file — use it first when a spreadsheet answer looks wrong, before reaching for SQL: it shows exactly what the profiler and materialiser saw.

---

## 5. Rematerialising after a knob change

Every `tabular_*`/`tabular_profile_*` value is baked into the materialised tables and the rendered hybrid text at ingest time — changing one in the admin UI does **not** retroactively change already-ingested spreadsheets. Two ways to apply a change:

- Re-upload the affected file(s) individually, or
- `POST /api/kb/{id}/tabular/rematerialize` (kbAdmin) — re-ingests **every** spreadsheet file in the KB via `TypeReEmbedding`.

The three Phase 4 size/concurrency keys (`tabular_max_file_bytes`, `tabular_large_file_bytes`, `tabular_large_file_concurrency`) are the exception: none is `RequiresReingest` (confirmed in `internal/siteconfig/registry.go`) — they govern upload rejection and ingest scheduling, not values baked into a table, so changing them needs no rematerialise (though `tabular_large_file_concurrency` needs a worker restart per §1).

All three read only from the global site_config reader — the upload adapter (`internal/app/routes.go`) and the processor (`internal/app/worker.go`) both wire the deployment-wide `chatStore`, never a per-KB one — so, exactly like the pre-existing `tabular_max_rows`/`tabular_embed_max_rows`, a value set through the per-KB settings editor takes effect deployment-wide, not just for that KB, even though the editor shows them per KB; `tabular_large_file_concurrency` is additionally snapshotted once into the `*processor.LargeFileGate` at worker startup, so a live edit needs the worker restart mentioned above before it applies at all.

---

## 6. The orphan-table sweep (R65)

`internal/tabular.OrphanSweeper` runs as the `tabular_orphan_cleanup` maintenance loop (`internal/worker/maintenance.go`), gated on `WORKER_MAINTENANCE` (same gate as the other maintenance loops — set `WORKER_MAINTENANCE=false` to disable all of them, there is no per-loop toggle). Schedule: first run 5 minutes after worker startup, then every 6 hours.

**What it drops:** every `tabular.sheet_<32-hex-file-id>_<sheetIdx>_<regionIdx>[__stage]` table (and the matching `tabular_column_values` rows, keyed by table name) whose embedded file id no longer has a row in `files` — i.e. the owning file was deleted but its materialised tables survived. Caps at 100 drops per tick; a later tick picks up where the previous one left off (the next listing simply excludes what's already gone).

**What it deliberately leaves alone:** a table whose `files` row still exists but whose `tabular_catalog` row is missing. That is not an orphan — the next ingest of that file recreates the catalog row and reuses (or replaces) the table. Only look for orphans by file-existence, never by catalog-row-existence.

**Log lines** (there is currently no Prometheus metric for this sweep — see §7):
- `tabular.orphan_cleanup dropped=<n> candidates=<n>` — emitted by the sweeper itself on every run, including a no-op run (`dropped=0`).
- `tabular orphan sweep completed dropped=<n>` — emitted by the maintenance-loop wrapper, only when `dropped > 0`.
- `tabular orphan sweep failed error=<err>` — the whole sweep tick errored (e.g. a DB connectivity issue); the next tick retries from scratch.

**Manual trigger:** there is no admin-UI button or API endpoint for this sweep — it only runs on the worker's own 6-hour timer. To force an off-cycle cleanup, restart the worker pod (fires the 5-minutes-after-startup run) or wait for the next tick.

---

## 7. Metrics to alert on

`GET /metrics` (admin-protected), all under the `rag_` prefix:

| Metric | Alert condition | Meaning |
|---|---|---|
| `rag_tabular_ingest_total{outcome="error"}` | rate > 0 sustained | A spreadsheet's `ingest.Ingester.Ingest` call (profile → materialise → render) failed outright. Check the file's `error_message`/`error_stage` and the `tabular.ingest.done` log line for the failing file. |
| `rag_tabular_router_total{outcome=~"sql_error\|validator_rejected"}` | rising rate relative to `fired_ok` | The router is proposing SQL that fails execution or the AST validator. `validator_rejected` growing specifically is worth escalating — either a prompt-injection attempt or the schema summary is misleading the SQL generator. Cross-reference `tabular_query_log` (kb_id, question, sql, outcome) for the actual statements. |
| `rag_tabular_ingest_duration_seconds` | p99 climbing | Ingest is getting slower — check whether large-file concurrency is saturated (§1) or spreadsheet sizes are trending up. |

**Known gap:** the orphan sweep has no Prometheus counter yet (`rag_tabular_orphan_tables_dropped_total` was planned but is not wired — `internal/worker/maintenance.go`'s `sweepTabularOrphans` says so in a comment). Alert on the `tabular orphan sweep failed` log line instead until that metric lands; a healthy sweep logs `dropped=0` on most ticks and is not itself alert-worthy.

The Phase-3 router family (`rag_tabular_router_rows`, `rag_tabular_router_repairs_total`, `rag_tabular_profile_llm_total`) and the two eval-time rates (`tabular_router_fire_rate`, `tabular_sql_error_rate` — printed by `cmd/eval` under `--production-context`) are documented in `docs/retrieval.md`'s Spreadsheets section; not repeated here.

---

## 8. Read-only role grant checklist

`chat_tabular_query_enabled` and `chat_tabular_router_enabled` are both inert without `JUSTRAG_DB_URL_READONLY` pointed at a role that can actually read the tabular schema — `internal/app/routes.go` logs a startup warning and disables the router if the DSN is unset. Run once, as DB owner/superuser, after applying migrations 0048 and 0069:

```sql
GRANT SELECT ON tabular_catalog TO <readonly_role>;
GRANT SELECT ON tabular_column_values TO <readonly_role>;
GRANT USAGE ON SCHEMA tabular TO <readonly_role>;
GRANT SELECT ON ALL TABLES IN SCHEMA tabular TO <readonly_role>;
ALTER DEFAULT PRIVILEGES FOR ROLE <db_user> IN SCHEMA tabular
    GRANT SELECT ON TABLES TO <readonly_role>;
```

`<db_user>` = the app's `DB_USER` (the role that creates per-sheet tables at ingest time — the `ALTER DEFAULT PRIVILEGES` line is what makes every *future* per-sheet table readable without re-running this checklist per file). `<readonly_role>` = the role behind `JUSTRAG_DB_URL_READONLY`. Verify the role's `search_path` does **not** include `tabular` (`docs/feature-recipes.md`'s SECURITY note explains why: unqualified table-name resolution must fail, or a prompt-injected bare table name could bypass the per-KB catalog allowlist).

Quick verification query (run as `<readonly_role>` or with `SET ROLE`):

```sql
SELECT count(*) FROM tabular_catalog;               -- must succeed
SELECT count(*) FROM tabular_column_values;          -- must succeed
SELECT current_setting('search_path');               -- must NOT list 'tabular'
```

---

## 9. Acceptance procedure

Before enabling `chat_tabular_query_enabled`/`chat_tabular_router_enabled` on a production deployment (or after a change to the router's LLM/prompt/validator), run the spreadsheet golden set:

```bash
# 1. Seed a KB from the sheetsource test fixtures and resolve the golden set's kb_id:
JUSTRAG_ADMIN_PASSWORD=... ./eval/fixtures/seed-spreadsheets.sh
# writes eval/golden/spreadsheets-de.local.jsonl (gitignored)

# 2. Run the acceptance pass through the real Supervisor path:
cd go-backend
go build ./cmd/eval
./cmd/eval/eval --golden ../eval/golden/spreadsheets-de.local.jsonl \
  --production-context --judge \
  --output ../eval/golden/spreadsheets-de.report.json

# 3. Extract the four numbers:
jq '{fire: .tabular_router_fire_rate, sqlerr: .tabular_sql_error_rate, faith: .aggregate.faithfulness, relevance: .aggregate.answer_relevance}' \
  ../eval/golden/spreadsheets-de.report.json
```

Thresholds: router fire rate ≥ 0.90 on the `lookup` + `complex_reasoning` subset, SQL error rate ≤ 0.05, judged correctness ≥ 0.85 on lookups and aggregations (faithfulness × answer relevance per question, used as the correctness proxy — see `eval/golden/spreadsheets-de.acceptance.md` for the filled-in run and `eval/golden/README.md`'s "Spreadsheet set" section for the full seed → run → acceptance procedure and how the ~40-question set is composed from `go-backend/internal/sheetsource/testdata`). If no model endpoint is reachable, the seeding script alone can still be run (materialisation needs no LLM when `tabular_profile_llm_enabled` is off) — record the acceptance as "not run" with the blocker rather than skipping the record entirely.

Re-run this procedure after any change to `chat_tabular_router_model`, the SQL-generation prompt, `tabular/sqlcheck`'s allowlists, or a fixture change under `internal/sheetsource/testdata` — the fire rate and SQL error rate are exactly the numbers a prompt or validator regression moves.
