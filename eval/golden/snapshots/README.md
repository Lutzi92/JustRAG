# Evaluation snapshots

This directory holds snapshots of `cmd/eval` output on the production golden set (`production-ppm-2026-08.jsonl`; formerly `production.jsonl`). Each snapshot captures a specific pipeline configuration and serves as a local regression gate for future changes.

**Privacy scope.** The JLU-internal golden fixture (`production.jsonl`) and any snapshot files (`*.json` under this directory) are **not committed** to the public repo — they live only on operator workstations. Root `.gitignore` excludes `/eval/golden/production.jsonl`, `/eval/golden/production-q032fix.jsonl`, `/eval/golden/production-ppm-2026-08.jsonl`, `/eval/golden/*.xlsx`, `/eval/golden/*.zip`, and `/eval/golden/snapshots/*.json`. Only this README and the methodology docs in `eval/golden/README.md` are tracked by git. See `eval/golden/README.md` §Sampling methodology #7 for the privacy rationale.

## Files

### `baseline_unrouted.json` (gitignored; regenerate locally)

Output of `cmd/eval --golden production-ppm-2026-08.jsonl --top-k 10 --production-context` captured on 2026-04-23 with today's site-config defaults and **no routing** (pre-Phase-3 baseline). Per `docs/superpowers/specs/2026-04-22-consolidated-retrieval-plan-design.md`, this is the regression gate for Plan 4 (Route-Aware Routing): with `routing_enabled=false`, a fresh snapshot produced on the same golden set must diff byte-for-byte against the locally-held `baseline_unrouted.json`.

Because the snapshot is not in git, the gate is enforced locally by the operator before merging routing changes — not by CI. CI integration would require mounting the fixture and snapshot from a private source at build time; that is out of scope for this plan.

**Golden set:** 89 active questions across three routes (lookup 43, enumeration 14, complex_reasoning 32). `global_synthesis` is not represented in this fixture. 11 unanswerable questions are excluded (`cmd/eval-genset` skips them by category — they have no gold documents, and the loader rejects an empty ground truth).

> ⚠️ **`baseline_unrouted.json` is stale and its byte-for-byte gate is void.** It was captured against a KB that no longer exists (its `kb_id` and every `must_cite_file_ids` entry point at a deleted generation), and the active fixture now runs against KB `PPM-Eval` (`83262307-…`) with ground truth keyed on **file names**, not UUIDs. Re-capture a baseline on the current KB before using this directory as a regression gate again; a diff against the old file is meaningless, not a regression.

**Normalization.** Non-deterministic fields (`generated_at`, per-question `latency_ms`) are overwritten with stable values (`"SNAPSHOT"` and `0`) so `diff` / `jq` produce clean output.

**Regenerating.** Only regenerate when a deliberate retrieval-behavior change lands and the new output is the new intended baseline. Since the snapshot is not committed, replacing the local file is the regeneration — keep a brief note in your own operator log describing why a new baseline was adopted.

Pre-flight:

```bash
# 1. Ensure the dev stack is running (main DB on :5432, vectordb on :5433, redis, minio).
docker compose up -d

# 2. Build a fresh eval binary.
cd go-backend && go build -o /tmp/eval ./cmd/eval   # do NOT overwrite the tracked cmd/eval/eval binary
```

Regeneration:

```bash
cd go-backend
DB_HOST=localhost DB_PORT=5432 DB_USER=postgres DB_PASSWORD=postgres DB_NAME=rag_db \
VECTOR_DB_HOST=localhost VECTOR_DB_PORT=5433 VECTOR_DB_USER=postgres VECTOR_DB_PASSWORD=postgres VECTOR_DB_NAME=rag_vector_db \
REDIS_HOST=localhost REDIS_PORT=6379 REDIS_PASSWORD=redis \
S3_ENDPOINT=http://localhost:9000 S3_ACCESS_KEY=minioadmin S3_SECRET_KEY=minioadmin S3_BUCKET=rag-files S3_REGION=us-east-1 \
./cmd/eval/eval --golden ../eval/golden/production-ppm-2026-08.jsonl --top-k 10 \
                --production-context --output ../eval/golden/snapshots/baseline_unrouted.json
jq '.generated_at = "SNAPSHOT" | .questions |= map(.latency_ms = 0)' \
   ../eval/golden/snapshots/baseline_unrouted.json > /tmp/snap.json
mv /tmp/snap.json ../eval/golden/snapshots/baseline_unrouted.json
```

## Parity note

The `cmd/eval --production-context` path was spot-checked against the HTTP chat endpoint on 5 stratified questions picked deterministically from the production golden set:

- `jlu-q010` — lookup
- `jlu-q014` — lookup
- `jlu-q041` — enumeration
- `jlu-q047` — enumeration
- `jlu-q057` — complex_reasoning

No significant deviation was reported by the operator. Chunk-level repeats in the eval output (same file ID appearing multiple times when multiple chunks from that file rank in the top-k) are expected and do not count as deviation; the UI Sources panel deduplicates these to file level.
