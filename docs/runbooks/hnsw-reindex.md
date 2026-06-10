# HNSW Index Reindex Runbook

**When to use:** After T1.2 ships, the build-time HNSW constants in `internal/vector/schema.go` are `hnswM = 24, hnswEfConstruction = 128`. Tables created on a worker that ran AFTER those constants were set already have indexes at the right params. Tables whose HNSW indexes were built earlier (when pgvector defaults `m = 16, ef_construction = 64` were still in effect) should be rebuilt to recover ~5–10% recall headroom at the same `ef_search`.

This runbook is the alternative to the auto-rebuild migration that was originally planned. It was rejected because `CREATE INDEX CONCURRENTLY` cannot run inside a transaction (or a `DO $$ ... $$` block), and the only honest in-migration alternative is non-concurrent rebuild that briefly blocks writes — unacceptable for production.

**Safety:** `CREATE INDEX CONCURRENTLY` does not block reads or writes during build. The subsequent `DROP INDEX` + `ALTER INDEX ... RENAME` swap takes an `ACCESS EXCLUSIVE` lock for the rename only, which is sub-second.

---

## Step 1 — Audit existing index params

Connect to the vector DB:

```bash
docker compose exec vectordb psql -U postgres -d vector
```

Then check every HNSW index on every chunk table:

```sql
SELECT
    indexname,
    indexdef
FROM pg_indexes
WHERE schemaname = 'public'
  AND indexname LIKE 'document_chunks%embedding%idx'
ORDER BY indexname;
```

Each `indexdef` includes a `WITH (m='N', ef_construction='M')` clause. Two outcomes:

- **Already at `m='24', ef_construction='128'`**: no action needed for that index. Skip it.
- **Different params (e.g. `m='16', ef_construction='64'`)**: rebuild via Step 2.

---

## Step 2 — Rebuild outdated indexes (per table)

For each table whose index needs rebuilding, run the appropriate block.

### Pattern A — Regular `vector` HNSW (dim ≤ 2000)

Index name: `<table>_embedding_idx`. Replace `<TABLE>` (e.g. `document_chunks_1536`):

```sql
-- Build the new index alongside the old one. CONCURRENTLY does not block.
CREATE INDEX CONCURRENTLY <TABLE>_embedding_idx_v2
    ON <TABLE>
    USING hnsw (embedding vector_cosine_ops)
    WITH (m = 24, ef_construction = 128);

-- Atomic swap: drop the old, rename the new. Brief ACCESS EXCLUSIVE lock.
BEGIN;
DROP INDEX <TABLE>_embedding_idx;
ALTER INDEX <TABLE>_embedding_idx_v2 RENAME TO <TABLE>_embedding_idx;
COMMIT;
```

### Pattern B — `halfvec` HNSW (dim 2001–4000, e.g. the production `document_chunks_2560`)

Index name: `<table>_embedding_halfvec_idx`. The expression in the `USING` clause includes the explicit `halfvec(N)` cast — substitute `<DIM>`:

```sql
-- Replace <TABLE> with the table name, <DIM> with the dimension number
-- (e.g. document_chunks_2560 → DIM = 2560).
CREATE INDEX CONCURRENTLY <TABLE>_embedding_halfvec_idx_v2
    ON <TABLE>
    USING hnsw ((embedding::halfvec(<DIM>)) halfvec_cosine_ops)
    WITH (m = 24, ef_construction = 128);

BEGIN;
DROP INDEX <TABLE>_embedding_halfvec_idx;
ALTER INDEX <TABLE>_embedding_halfvec_idx_v2 RENAME TO <TABLE>_embedding_halfvec_idx;
COMMIT;
```

### Concrete example: production `document_chunks_2560` (Pattern B)

```sql
CREATE INDEX CONCURRENTLY document_chunks_2560_embedding_halfvec_idx_v2
    ON document_chunks_2560
    USING hnsw ((embedding::halfvec(2560)) halfvec_cosine_ops)
    WITH (m = 24, ef_construction = 128);

BEGIN;
DROP INDEX document_chunks_2560_embedding_halfvec_idx;
ALTER INDEX document_chunks_2560_embedding_halfvec_idx_v2
    RENAME TO document_chunks_2560_embedding_halfvec_idx;
COMMIT;
```

---

## Step 3 — Verify

Re-run the audit query from Step 1. Every index now matches `WITH (m='24', ef_construction='128')`. Then optionally run an eval pass to confirm the recall/latency curve shifted as expected.

---

## Notes

- Build time scales with `(rows × dimensions × m)`. On a 100k-chunk 2560-dim table expect a few minutes. On 1M+ rows expect tens of minutes. `CONCURRENTLY` lets reads/writes continue throughout.
- `maintenance_work_mem` controls how much memory the build uses. If `psql` reports slow build, raising this in the session can help: `SET maintenance_work_mem = '2GB';` before the `CREATE INDEX CONCURRENTLY`.
- If a `CONCURRENTLY` build is interrupted, it leaves an `INVALID` index. Drop it with `DROP INDEX <name>_v2;` and retry.
- The runtime `hnsw.ef_search` knob (admin Site Config: `hnsw_ef_search`, default 150) is independent of the build-time `m` / `ef_construction` and can be tuned without rebuilding.
