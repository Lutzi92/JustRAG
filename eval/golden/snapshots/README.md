# Evaluation snapshots

This directory holds snapshots of `cmd/eval` output on the production golden set (`production-ppm-2026-08.jsonl`; formerly `production.jsonl`). Each snapshot captures a specific pipeline configuration and serves as a local regression gate for future changes.

**Privacy scope.** The JLU-internal golden fixture (`production.jsonl`) and any snapshot files (`*.json` under this directory) are **not committed** to the public repo — they live only on operator workstations. Root `.gitignore` excludes `/eval/golden/production.jsonl`, `/eval/golden/production-q032fix.jsonl`, `/eval/golden/production-ppm-2026-08.jsonl`, `/eval/golden/*.xlsx`, `/eval/golden/*.zip`, and `/eval/golden/snapshots/*.json`. Only this README and the methodology docs in `eval/golden/README.md` are tracked by git. See `eval/golden/README.md` §Sampling methodology #7 for the privacy rationale.

## Files

### `ab-*.json` naming convention (local A/B grids, gitignored)

A one-off multi-cell A/B grid (e.g. the 2026-09 `bm25_scoring_mode` ts_rank-vs-BM25 grid — see "Keyword arm scoring: ts_rank vs BM25 (2026-09)" in `docs/retrieval.md`) is captured as `ab-<cell>.json` per cell (`ab-A.json`, `ab-B.json`, …), each with a matching `ab-<cell>.log` for the raw `cmd/eval` output (stage logs, regression-gate lines, aggregate/per-route summary). This is distinct from the durable `baseline.json`/`baseline_unrouted.json` files above, which persist as *the* regression gate for a given flag across time — an `ab-*` grid is a single investigation's working files, kept only long enough to write up the numbers in the relevant `docs/retrieval.md` subsection, and is not itself re-used as a future `--baseline` target. Same gitignore scope as every other snapshot file (`/eval/golden/snapshots/*.json`) — never committed.

### `baseline_unrouted.json` (gitignored; regenerate locally)

Output of `cmd/eval --golden production-ppm-2026-08.jsonl --top-k 10 --production-context` captured on 2026-04-23 with today's site-config defaults and **no routing** (pre-Phase-3 baseline). Per `docs/superpowers/specs/2026-04-22-consolidated-retrieval-plan-design.md`, this was the regression gate for Plan 4 (Route-Aware Routing): with `routing_enabled=false`, a fresh snapshot produced on the same golden set was compared against the locally-held `baseline_unrouted.json` (originally byte-for-byte via `diff`; see "Comparing against a baseline" below for the current `--baseline`-flag mechanism).

Because the snapshot is not in git, the gate is enforced locally by the operator before merging routing changes — not by CI. CI integration would require mounting the fixture and snapshot from a private source at build time; that is out of scope for this plan.

**Golden set:** 89 active questions across three routes (lookup 43, enumeration 14, complex_reasoning 32). `global_synthesis` is not represented in this fixture. 11 unanswerable questions are excluded (`cmd/eval-genset` skips them by category — they have no gold documents, and the loader rejects an empty ground truth).

> ⚠️ **`baseline_unrouted.json` is stale and its byte-for-byte gate is void.** It was captured against a KB that no longer exists (its `kb_id` and every `must_cite_file_ids` entry point at a deleted generation), and the active fixture now runs against KB `PPM-Eval` (`83262307-…`) with ground truth keyed on **file names**, not UUIDs. Re-capture a baseline on the current KB before using this directory as a regression gate again; a diff against the old file is meaningless, not a regression.

**Normalization.** Non-deterministic fields (`generated_at`, per-question `latency_ms`) are overwritten with stable values (`"SNAPSHOT"` and `0`) so an old-style `diff` / `jq` inspection produces clean output. No longer required for the `--baseline` comparison below — `cmd/eval` parses both reports and compares mean recall / MRR (overall and per route), so cosmetic differences in timestamps, latency, or field order never trip it — but still worth doing if you're eyeballing two snapshots with `diff` yourself.

**Regenerating.** Only regenerate when a deliberate retrieval-behavior change lands and the new output is the new intended baseline. Since the snapshot is not committed, replacing the local file is the regeneration — keep a brief note in your own operator log describing why a new baseline was adopted. This is the same rule as before; only the enforcement mechanism changed (2026-09-05): a `diff`/`jq` byte-for-byte comparison is replaced by `cmd/eval --baseline`, which exits **3** on a real regression instead of flagging every incidental byte difference.

Pre-flight:

```bash
# 1. Ensure the dev stack is running (main DB on :5432, vectordb on :5433, redis, minio).
docker compose up -d

# 2. Build a fresh eval binary.
cd go-backend && go build -o /tmp/eval ./cmd/eval   # do NOT overwrite the tracked cmd/eval/eval binary
```

Capturing a baseline (keep it as `baseline.json`, or reuse any prior `cmd/eval --output` snapshot — `baseline_unrouted.json` above works too once re-captured against the current KB, see the stale-file warning):

```bash
cd go-backend
DB_HOST=localhost DB_PORT=5432 DB_USER=postgres DB_PASSWORD=postgres DB_NAME=rag_db \
VECTOR_DB_HOST=localhost VECTOR_DB_PORT=5433 VECTOR_DB_USER=postgres VECTOR_DB_PASSWORD=postgres VECTOR_DB_NAME=rag_vector_db \
REDIS_HOST=localhost REDIS_PORT=6379 REDIS_PASSWORD=redis \
S3_ENDPOINT=http://localhost:9000 S3_ACCESS_KEY=minioadmin S3_SECRET_KEY=minioadmin S3_BUCKET=rag-files S3_REGION=us-east-1 \
./cmd/eval/eval --golden ../eval/golden/production-ppm-2026-08.jsonl --top-k 10 \
                --production-context --output ../eval/golden/snapshots/baseline.json
```

**Comparing against a baseline.** After a retrieval-behavior change, run a fresh candidate report and diff it against `baseline.json` with `--baseline` (same DB/Redis/S3 env vars as above, omitted for brevity):

```bash
./cmd/eval/eval --golden ../eval/golden/production-ppm-2026-08.jsonl --top-k 10 \
                --production-context --output /tmp/candidate.json \
                --baseline ../eval/golden/snapshots/baseline.json
echo "exit=$?"   # 0 = no regression, 1 = question errors, 2 = usage/unreadable baseline, 3 = regression
```

A regression (exit 3) prints a per-route delta table (recall/MRR/nDCG, baseline → candidate) before exiting; `--regress-recall-pp`/`--regress-mrr-pp` override the default 2.0/3.0 pp thresholds per invocation. Only overwrite `baseline.json` with the candidate's output when you've deliberately decided the new behavior is the intended baseline — a clean exit-0 run is not itself a reason to adopt a new baseline, since a knob that scores flat today can still drift the next time it's touched.

## Parity note

The `cmd/eval --production-context` path was spot-checked against the HTTP chat endpoint on 5 stratified questions picked deterministically from the production golden set:

- `jlu-q010` — lookup
- `jlu-q014` — lookup
- `jlu-q041` — enumeration
- `jlu-q047` — enumeration
- `jlu-q057` — complex_reasoning

No significant deviation was reported by the operator. Chunk-level repeats in the eval output (same file ID appearing multiple times when multiple chunks from that file rank in the top-k) are expected and do not count as deviation; the UI Sources panel deduplicates these to file level.
