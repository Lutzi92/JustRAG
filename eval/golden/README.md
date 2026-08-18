# Golden evaluation sets

Each `.jsonl` file here is a curated list of questions used by the
`cmd/eval` binary to evaluate retrieval quality.

## Format

One JSON object per line. Blank lines and `#`-prefixed lines are ignored.

Fields:

| Field                | Type     | Description                                               |
|----------------------|----------|-----------------------------------------------------------|
| `id`                 | string   | Stable identifier (e.g. `kb-ws25-q001`).                  |
| `question`           | string   | The natural-language question to retrieve for.            |
| `kb_id`              | string   | KB UUID. Files in `must_cite_file_ids` must belong to it. |
| `language`           | string   | `"de"` or `"en"`. Feeds PG text-search config.            |
| `must_cite_file_ids` | string[] | UUIDs of files a correct retrieval must surface.          |
| `must_cite_file_names` | string[] | File names a correct retrieval must surface. Alternative ground-truth key to `must_cite_file_ids` — **use one or the other per file, never both** (see below). |
| `query_type`         | string   | (optional) One of `lookup`, `enumeration`, `global_synthesis`, `complex_reasoning`. Enables per-route metrics. |
| `expected_kb_ids`    | string[] | (optional, AP-A4) KBs the sub-KB router should pick. Empty defaults to `[kb_id]` (single-KB). Multi-element rows test cross-KB fan-out. |
| `expected_tools`     | string[] | (optional, Phase 2 §2.2) MCP tool names the agent should invoke. |
| `notes`              | string   | (optional) Human context; ignored by the runner.          |

## Ground truth by name, not by UUID

`must_cite_file_names` matches a retrieved chunk on its file **name**;
`must_cite_file_ids` matches on its UUID. Prefer names.

A file's UUID is regenerated on every delete + re-upload, so a set authored
purely by UUID dies the moment the KB is rebuilt — and it dies *silently*:
every question scores recall 0.000 with no error, which reads like a
retrieval regression rather than a stale fixture. Names survive re-ingest.
`production-q032fix.jsonl` was lost this way when its KB was cleared. Once the
KB row is gone there is no old-UUID→name mapping left in the repo — not in
`snapshots/baseline_unrouted.json` (UUIDs only, from yet another KB
generation), not in the `*_files_list.zip` exports (Confluence page IDs), not
in git history. What saves you is `JLU_RAG_Eval_Set_v1.xlsx` plus
`cmd/eval-genset`: the XLSX keys ground truth on **Confluence page IDs**,
which are stable across re-ingests, and eval-genset resolves them against
`files.confluence_page_id` in any KB. Regenerate, then rewrite the emitted
`must_cite_file_ids` to names.

**Never list the same logical file under both keys.** `buildTruth` cannot
dedupe them (it does no DB lookup), so the file counts twice in the truth
set and recall halves even when retrieval is perfect.

Name matching needs `RetrievedChunk.FileName` to be populated on every eval
path — the retrieval-only adapter in `cmd/eval/main.go` once dropped it,
which is what `TestLegacySearchAdapterPropagatesFileName` now guards.

## Production sets

- `production-ppm-2026-08.jsonl` — **the active set.** The same 89 questions as
  the retired `production-q032fix.jsonl`, re-pointed at KB `PPM-Eval`
  (`83262307-…`, JLU Digitalprojekt-Portfolio + "Neue Wege mit KI" Confluence
  spaces, 297 files). Ground truth regenerated from the XLSX via
  `cmd/eval-genset`, then rewritten to `must_cite_file_names`.
  Q006 and Q087 carry a substitute source: their XLSX gold doc is the
  Confluence page "PPM Startseite" (`427983309`), which this export omits.
  Baseline 2026-08-18 (k=10, retrieval-only, qwen3-embedding-8b + jina-v3 at
  α=0.8, parent-child off, 0 errors): recall **0.911** / precision 0.328 /
  MRR **0.919** / nDCG 0.918; lookup 0.948, enumeration 0.899 (MRR 1.000),
  complex_reasoning 0.868. Full breakdown and caveats in `docs/retrieval.md`
  §"Current baseline: qwen3-embedding-8b".
- `production.jsonl` / `production-q032fix.jsonl` — superseded; both point at
  the deleted KB `a4dab03f-…` and cannot be run.

### Regenerating after a KB rebuild

```bash
cd go-backend && go build -o /tmp/eval-genset ./cmd/eval-genset
/tmp/eval-genset --xlsx ../eval/golden/JLU_RAG_Eval_Set_v1.xlsx \
                 --kb-id <new-kb-uuid> --output /tmp/genset.jsonl
```

Then map `must_cite_file_ids` → `must_cite_file_names` (one `files` lookup)
and drop the ids. Read the tool's WARN lines: an unresolved Confluence page ID
means the page is not in the new KB, and eval-genset **drops** the question
rather than emitting a half-truth — a silently shorter set is the thing to
watch for, not a crash.

## Authoring tips

- Start with ~20 questions per KB covering breadth (different topics) and
  depth (multi-hop, paraphrased, German colloquial forms).
- Use file-level ground truth, not chunk-level — it's cheap to author and
  correlates well with retrieval quality.
- When a real support ticket exposes a bad retrieval, convert it into a
  golden entry so you never regress on it.

## Route taxonomy

`query_type` labels the question's intent so `cmd/eval` can report per-route
metrics. The four routes correspond to distinct retrieval strategies:

- **`lookup`** — single-fact queries where one chunk contains the answer.
  *"What is the deadline for X?"*, *"Wer ist der Ansprechpartner für Y?"*
- **`enumeration`** — exhaustive list queries where the answer must cite
  every matching file. *"Welche Projekte arbeiten mit Embedded Systems?"*,
  *"List all policies that mention Z."*
- **`global_synthesis`** — corpus-level questions that summarize or
  compare across many documents. *"Was sind die wiederkehrenden Themen
  in den Projektprofilen?"*, *"Summarize the main trends in the KB."*
- **`complex_reasoning`** — multi-hop or compare/contrast questions where
  retrieval must surface several chunks whose combined content is the
  answer. *"How do the requirements in X differ from those in Y?"*

When in doubt, pick `lookup` — it's the default route and will route
through the simplest pipeline.

## Multi-hop set (AP-C5)

`multi_hop.jsonl` is the Phase C eval gate: GraphRAG vs. Vector-only
on questions whose answer requires evidence from ≥2 chunks (ideally
with a named entity bridging them — that's where graph traversal
should beat ANN). 30 rows is the target; the current commit ships a
10-row skeleton with placeholder UUIDs (search `TODO-C5`).

Hard rule: every row must have ≥2 `must_cite_file_ids` — a
single-file answer is a "lookup" by definition, not multi-hop. The
loader test (`TestLoadGoldenSet_MultiHop`) asserts this invariant
on at least one row so a regression that empties the multi-hop
section can't silently slip past CI.

Phase C accept criterion: recall +5pp AND MRR +3pp on this set vs.
Vector-only baseline, AND no >1pp regression on the 89-question
production set (`production-ppm-2026-08.jsonl`). Failing → keep the GraphRAG code in repo, leave the
`graph_search` tool default-off, document and revisit after
corpus-side annotation work.

## Multi-KB sets (AP-A5)

`multi_kb.jsonl` is the AP-A4 sub-KB router evaluation set, separate from
the per-KB production set. The current commit ships a 10-row skeleton
with placeholder UUIDs (search for `TODO-A5`); the target shape is:

- **30 single-KB rows** — each question is answerable from one KB only.
  Pick rows where the surface terms could plausibly match other KBs but
  only one actually contains the answer; that's where the router earns
  its keep. Set `expected_kb_ids: []` (default) — the router-eval falls
  back to `[kb_id]`.
- **20 cross-KB rows** — questions whose answer requires evidence from
  multiple KBs. Set `expected_kb_ids` to the full list (the primary KB
  in `kb_id` should be the first element by convention). The AP-A4
  metric is "did top-1 land in the list" plus "Jaccard overlap of
  router's fan-out with the full list".

When extending the file: append, never rewrite. The fixture is already
loaded by a regression test (`TestLoadGoldenSet_MultiKB`) that fails if
the cross-KB section disappears.

## Sampling methodology

`production-ppm-2026-08.jsonl` is the active golden set (see §Production sets; it superseded `production.jsonl`). It is drawn from real production chat logs following a reproducible, stratified process:

1. **Sample window.** Pull ≥100 recent chat queries from prod logs (most recent 30-day window, ≥5 distinct real users). Exclude test/internal queries by filtering on the known internal admin user IDs.

2. **Anonymization.** Redact identifying strings in question text (personal names, email addresses, IBANs, project-internal codes that would leak PII outside the fixture's readership). Keep the semantic intent intact. The `must_cite_file_ids` UUIDs point at internal documents and do not leak PII on their own.

3. **Stratification.** Manually label each sampled query into one of `lookup`, `enumeration`, `global_synthesis`, `complex_reasoning`. Select ~50 whose distribution clears ≥8 per route. Accept ≥5 for `global_synthesis` if prod traffic does not justify 8.

4. **Ground truth.** For each labeled question, record:
   - `question` — the (anonymized) query text.
   - `kb_id` — the KB the original query ran against.
   - `language` — `"de"` or `"en"`.
   - `must_cite_file_ids` — UUIDs of files a correct retrieval must surface. Sourced from the operator's own knowledge of the KB, or by spot-checking the prior production response when it was correct.
   - `query_type` — the labeled route.
   - `notes` — optional human context (e.g., why the query is tricky).

5. **Class imbalance.** `global_synthesis` is rare in practice. The per-route aggregates remain interpretable at lower n; expected variance is higher and is reflected in the human-summary output.

6. **Extending the set.** To add more questions later, append new lines. Do NOT rewrite existing lines — snapshots under `snapshots/` diff on stable question IDs; rewriting invalidates the diff.

7. **Privacy.** If prod transcripts are not privacy-cleared for commit, store the actual `production.jsonl` in a private location and mount at eval time. Commit only the schema (this README) and any snapshot README fragments that describe the configuration; do not commit the raw fixture.

## Running

```bash
cd go-backend
go build ./cmd/eval
# Retrieval-only (fast regression check, matches the current CI baseline):
./cmd/eval/eval --golden ../eval/golden/example.jsonl --top-k 10 --output ../eval-report.json
```

The JSON report path is consumed by (future) Phase 2.3 CI integration.
Exit code is non-zero if any question erred out; scoring thresholds
belong to Phase 2.3, not here.

### Production-parity mode

Route retrieval and answer generation through the full chat pipeline — CRAG,
neighbor expansion, truncation, sandwich order, enumeration pre-pass,
contextual prefix, abstain/low-confidence notices — so eval measures the same
code path that serves users.

```bash
./cmd/eval/eval --golden ../eval/golden/example.jsonl --production-context
```

### Orchestrator dispatch (default since 2026-05)

When `--production-context` is on, each question now routes through the same
orchestrator predicate `chat.tryDeepChat` uses — Supervisor, Plan-Execute
(flat or DAG), Agentic, or the standard fallback — driven by the live
`chat_supervisor_enabled` / `chat_plan_execute_enabled` /
`chat_agentic_enabled` gates. The report adds two fields:

- **per-question** `agent`: `{orchestrator, specialist?, tools, hops?, plan?, dispatch_reason}`.
- **report-level** `orchestrator_aggregates`: same shape as `route_aggregates`,
  bucketed by which orchestrator handled the question.

This is on by default so eval reflects what production actually runs. Two
practical consequences:

- **Latency increases** for `complex_reasoning` questions — typically 2–5×
  per question, because the orchestrator's planner LLM call + sub-query
  fan-out + multi-hop work happens for real.
- **Reports gain new JSON keys.** Both `agent` and `orchestrator_aggregates`
  are `omitempty`, so legacy reports without orchestrator dispatch stay
  byte-stable.

Pass `--orchestrator-dispatch=false` to reproduce pre-2026-05 retrieval-only
behaviour (no dispatch, no new fields) — useful for diffing against
historical reports.

### Ablation flags

| Flag | Effect |
|---|---|
| `--enhance rewrite\|expand\|spell` | Query-enhancement mode (applies in both modes). |
| `--hyde` | Enable HyDE query expansion. |
| `--multi-query` | Enable multi-query retrieval. |
| `--crag on\|off` | Force CRAG on/off regardless of KB config (production-context mode only). |
| `--enumeration on\|off` | Force enumeration pre-pass on/off regardless of `IsEnumerationQuery` (production-context mode only). |

Example — measure the contribution of the enumeration pre-pass on
enumeration-labeled questions:

```bash
./cmd/eval/eval --golden ../eval/golden/example.jsonl --production-context \
                --enumeration off --output /tmp/eval-no-enum.json
./cmd/eval/eval --golden ../eval/golden/example.jsonl --production-context \
                --enumeration on --output /tmp/eval-with-enum.json
# Diff the `route_aggregates["enumeration"]` blocks.
```

## Judge mode

Opt-in LLM-as-judge evaluation is available via `--judge`. When enabled, the
runner generates an answer per question (using the KB's chat model) and
evaluates it against three metrics:

- **Faithfulness** — fraction of claims in the answer that are supported by
  the retrieved context.
- **Answer relevance** — how well the answer addresses the question (Likert
  1..5 normalized to 0..1).
- **Context precision** — fraction of retrieved top-k chunks that are
  relevant to the question.

Use `--judge-model <name>` to run judge prompts on a specific (typically
smaller/cheaper) model; empty falls back to the KB's chat model.

Judge calls use temperature 0 for determinism. All three metrics are
optional and run independently — a single judge failure is captured in
`judge.judge_errors` and does not abort the others.

```bash
./cmd/eval/eval --golden ../eval/golden/example.jsonl --judge --judge-model gpt-4o-mini
```

### Cost note

Judge mode executes roughly 4 LLM calls per question (1 answer + 3 judges).
For a 20-question golden set on a mid-tier model, expect 80 calls total.
Plan accordingly. For fast iteration, keep `--judge` off and rely on
retrieval metrics alone.

## Tabular Q&A (table_query) — follow-up

The existing harness is retrieval-oriented: questions are scored on recall,
MRR, and NDCG against `must_cite_file_ids`. Tabular Q&A (`table_query` tool)
produces deterministic SQL results (exact numeric sums, counts, lookups), not
a retrieved-chunk set, so it does not map cleanly onto this schema — there is
no `expected_answer` field and the eval runner has no exact-match comparator.

A dedicated tabular eval harness is a **Phase-1 follow-up**. A future fixture
(`eval/golden/tabular.jsonl`) would require: (1) a seeded KB with a known
fixture spreadsheet ingested under `chat_tabular_query_enabled=true`, giving
real, stable file UUIDs; (2) an `expected_answer` field (or similar) in the
golden schema for deterministic SQL-result comparison; and (3) a runner that
calls the `table_query` tool, executes the returned SQL against the tabular
schema, and asserts the result matches the expected value exactly. Until that
infrastructure exists, tabular questions can be evaluated via the `--judge`
path (LLM-as-judge answer relevance), which uses the existing schema without
modification.

**Phase 2 fuzzy free-text-cell search** adds a second eval need: a fixture containing a spreadsheet with a free-text column (long, high-cardinality) plus a set of expected `_rowid` values that fuzzy kb_search should surface for a given query. This also requires a dedicated exact-answer harness (not the retrieval golden), since the assertion is on matched row IDs and the downstream `table_query` aggregation result, not on retrieved chunk recall.

**Phase 3 charts/pivots** need a dedicated rubric (e.g. "did the answer emit a valid `chart` block with the right series for the asked aggregation"), not the retrieval golden.
