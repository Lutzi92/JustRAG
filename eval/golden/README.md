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
| `turns`              | Turn[]   | (optional) Marks this row as a multi-turn conversation instead of a single question — see "Multi-turn set" below. When present, the top-level `question`/`must_cite_*` fields are not required; ground truth lives per turn. Each `Turn` is `{question, kind, query_type?, must_cite_file_names?, answer?, answer_sources?, notes?}` — `kind` is one of `corpus`, `pronoun_ref`, `topic_shift`, `answer_ref`, `post_abstain`; `must_cite_file_names` is required for every kind except `answer_ref`, where it defaults to the previous turn's `answer_sources`. |
| `history`            | HistoryEntry[] | Populated by `ExpandTurns` on the per-turn `Question`s it produces from `turns` (one prior `{role, content, sources?}` entry per earlier turn); never authored directly — the field exists so a report round-trips it. |
| `turn_kind`          | string   | Populated by `ExpandTurns` (copied from the originating `Turn.kind`); never authored directly — labels each expanded per-turn question for `turn_kind_aggregates`. |

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

## Multi-turn set

`multi-turn-de.jsonl` (Wave 2, Task 4) exercises follow-up condensation
(`chat.CondenseFromHistory`) against KB `PPM-Eval`
(`83262307-3a1b-49bc-bd08-3b925a868a92`), the same KB as
`production-ppm-2026-08.jsonl`. It is **derived from the JLU-internal
production set** — every opening turn except the 3 `post_abstain` ones
(see below), and every `topic_shift`/`post_abstain` follow-up, reuses a
question and `must_cite_file_names` from `production-ppm-2026-08.jsonl` —
so it carries the same privacy status and is **gitignored**
(`/eval/golden/multi-turn-de.jsonl` in `.gitignore`), never committed.

**Composition:** 18 conversations (`MT01`..`MT18`), 45 turns total. Every
conversation opens with a `corpus` turn copied verbatim from an existing
production question (same `query_type`, same `must_cite_file_names`),
except the 3 `post_abstain` openers, which are newly authored unanswerable
questions (see below). Follow-up turns:

- **12 `pronoun_ref`** — a pronoun/ellipsis follow-up on the opener's
  subject ("und wer ist dafür verantwortlich?", "seit wann läuft es?",
  "welche Version ist das?", "wer vertritt sie?"). Ground truth is
  authored directly (usually the same file(s) as the opener, since the
  pronoun refers to the same project page) rather than copied from another
  golden question.
- **6 `topic_shift`** — a second, unrelated existing question dropped in
  after the opener; ground truth is that second question's own
  `must_cite_file_names`. Condensation must not drag the first subject into
  the rewritten query.
- **6 `answer_ref`** — a retrieval-free reformat of the prior turn's answer
  ("das als Tabelle", "fass das kürzer zusammen", "als Stichpunkte bitte",
  "kannst du das übersetzen?"). No `must_cite_file_names` is authored; per
  `Turn.MustCiteFileNames`'s doc comment, `ExpandTurns` defaults it to the
  previous turn's `answer_sources`.
- **3 `post_abstain` conversations** (2 turns each, not counted in the
  12/6/6 above): turn 1 is a `corpus` question the corpus cannot answer (a
  budget/date figure the target project page does not contain),
  `must_cite_file_names` = the page that *should* have been consulted,
  `answer` = `"Dazu enthält die Wissensbasis keine Angaben."`,
  `answer_sources` = that same page (`notes` says why it's unanswerable);
  turn 2 (`kind: post_abstain`) is a normal, answerable question on a
  related subject, reusing another existing question's ground truth.

Every turn with a following turn carries an authored `answer` (1–2 German
sentences paraphrasing the question) and `answer_sources` — **not verified
facts**, just plausible history text to steer the condensation LLM the way
a real prior turn would (each turn's `notes` says so explicitly).

**How to run** (dev stack; builds `cmd/eval` fresh):

```bash
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/multi-turn-de.jsonl --production-context \
  --keep-raw off --output <out>.json
```

`--production-context` is required — the multi-turn replay adapter only
wires up under the standard `PrepareChatContext` path (no orchestrator
dispatch, no team). `--keep-raw on|off` forces
`chat_condense_keep_raw_enabled` for the run regardless of the live
site_config, so an A/B needs no DB mutation. The report's
`turn_kind_aggregates` gives recall/MRR/nDCG per `turn_kind`; each
question's `condensed_query` shows what the condenser produced for that
follow-up. See `eval/golden/multi-turn-de.acceptance.md` for the recorded
keep-raw on/off/noise run and the flag-default decision it produced.

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

## Spreadsheet set (Phase 4 release acceptance)

`spreadsheets-de.jsonl` is the release-acceptance golden set for the
2026-09-04 spreadsheet-ingest rework (spec
`.superpowers/sdd/2026-09-05-spreadsheet-ingest-phase4`, §7.3). It is the
retrieval-golden answer to the Tabular-Q&A follow-up above, scoped to what
the *existing* harness can already score: 40 German questions over the
`internal/sheetsource/testdata` fixtures (lookup by ID/name, aggregation and
filter, fuzzy free text, cross-sheet/multi-region disambiguation, format
quirks, and two deliberately unanswerable negatives), evaluated through
`--production-context --judge` rather than through a dedicated
`table_query`/SQL-exact-match harness. `notes` on every row spells out the
exact expected value read straight off the fixture with
`go run ./cmd/fixture-dump/main.go internal/sheetsource/testdata/<file>`
(`go-backend/cmd/fixture-dump/main.go`, `//go:build ignore` — prints every
sheet's rows via `sheetsource.Open` + `ReadSheet` so the golden set is
authored from real parsed values, never guessed).

Every question also carries `tabular_expected` (Ruling R75): `true` marks a
question that targets a materialised SQL table region the deterministic
tabular router should engage on, `false` marks one that deliberately
shouldn't (a form-field/dropdown-list region rendered as text, or an
unanswerable negative). `TabularRouterRates` uses this flag as the
fire-rate denominator whenever any question in the report set carries it
(exactly the case here), falling back to the legacy `query_type`-based
rule only for report sets where no question is annotated.

`kb_id` in the committed file is the placeholder
`REPLACE_WITH_FIXTURE_KB_ID` — it is not a real KB and must not be run
as-is (see "Ground truth by name, not by UUID" above for why the set is
authored with `must_cite_file_names`, not `must_cite_file_ids`, despite
using a placeholder id rather than a real one: this is a fixture set meant
to be re-seeded fresh in any environment, not tied to one KB's lifetime).

### Seeding

```bash
JUSTRAG_URL=http://localhost:3000 \
JUSTRAG_ADMIN_USER=admin \
JUSTRAG_ADMIN_PASSWORD=<the ADMIN_PASSWORD the instance was started with> \
  ./eval/fixtures/seed-spreadsheets.sh
```

Logs in as the admin user (whose password is whatever `ADMIN_PASSWORD` the
target instance was started with — `go-backend/internal/migrate/seed.go`
always seeds the superadmin as username `admin`), creates or reuses a KB
named "Spreadsheet Fixtures", uploads every fixture under
`go-backend/internal/sheetsource/testdata/*.{xlsx,xls,ods,csv}` (skipping
any `*large_synthetic*` fixture and any file already present in the KB, so
reruns are cheap), polls `GET /api/kb/{id}/files` every 5s until nothing is
`pending`/`processing` (failing loudly, with the file name and
`errorMessage`, on any `error`), prints the KB id, and writes
`eval/golden/spreadsheets-de.local.jsonl` — the committed set with `kb_id`
rewritten to the real KB id. That file is gitignored
(`eval/golden/*.local.jsonl`); never edit `spreadsheets-de.jsonl` itself to
point at a real KB.

Ingestion needs no LLM call as long as `tabular_profile_llm_enabled` is off
(the default) — the seeding step alone can run against an instance with no
model configured, per Ruling R68 below.

### Running the acceptance eval

```bash
cd go-backend
go build ./cmd/eval
./eval --golden ../eval/golden/spreadsheets-de.local.jsonl \
  --production-context --judge \
  --output ../eval/golden/spreadsheets-de.report.json
jq '{fire: .tabular_router_fire_rate, sqlerr: .tabular_sql_error_rate, faith: .aggregate.mean_faithfulness, relevance: .aggregate.mean_answer_relevance}' \
  ../eval/golden/spreadsheets-de.report.json
```

Note the binary is `./eval`, not `./cmd/eval/eval`: `go build ./cmd/eval`
(a single import path, no `-o`) writes the executable named after the
package into the **current** directory — `go-backend/eval` when run from
`go-backend/` — not into `cmd/eval/`. If a stale binary is sitting directly
under `cmd/eval/` from an earlier `-o`-qualified build, ignore or delete it;
the freshly built `./eval` in `go-backend/` is the one the command above
runs.

### Thresholds (spec §7.3)

| Metric | Threshold | Subset |
|---|---|---|
| Tabular router fire rate (`tabular_router_fire_rate`) | ≥ 0.90 | questions with `tabular_expected: true` (the eligible set per `TabularRouterRates`, Ruling R75 — see above) |
| Tabular SQL error rate (`tabular_sql_error_rate`) | ≤ 0.05 | of fired questions |
| Judged correctness | ≥ 0.85 | `lookup` + `complex_reasoning` questions |

The fire-rate numbers recorded in `eval/golden/spreadsheets-de.acceptance.md`
predate the `tabular_expected` annotation and were computed under the older
`lookup`/`complex_reasoning`-subset rule; they were not recomputed against
the new, narrower `tabular_expected: true` subset.

**Judged-correctness proxy.** The harness has no single "correctness" field
— `internal/eval/judge.go` exposes `faithfulness`, `answer_relevance`, and
`context_precision` per question. Per the spec, "correctness" here is the
mean of `faithfulness` and `answer_relevance` (not `context_precision`,
which measures retrieval precision rather than answer quality), computed
over the `lookup`/`complex_reasoning` subset. Each report entry under
`.questions[]` nests the golden question under a `question` key (siblings:
`retrieved`, `metrics`, `judge`, `agent`), so the query-type filter is
`.question.query_type`, not `.query_type`:

```bash
# Primary: mean of the two per-metric means (matches how
# internal/eval/metrics.go's Aggregate itself treats a missing judge
# metric — excluded from that metric's mean, not zero-filled). Only exact
# when every row in the report is lookup/complex_reasoning, as in
# spreadsheets-de.jsonl; otherwise filter .questions[] by
# .question.query_type first and recompute both means from the filtered
# set instead of reading .aggregate.* directly.
jq '(.aggregate.mean_faithfulness + .aggregate.mean_answer_relevance) / 2' \
  eval/golden/spreadsheets-de.report.json

# Stricter, per-question-paired variant: combine faithfulness and
# answer_relevance within each question before averaging, treating a
# missing metric (that question's own judge call failed, e.g. non-JSON
# judge output) as a hard 0 instead of excluding it. Filters explicitly by
# query_type so it works even when the set mixes in other types.
jq '[.questions[] | select(.question.query_type == "lookup" or .question.query_type == "complex_reasoning") | select(.judge != null) | ((.judge.faithfulness // 0) + (.judge.answer_relevance // 0)) / 2] | add / length' \
  eval/golden/spreadsheets-de.report.json
```

The two variants can diverge when some judge calls fail outright (see
`eval/golden/spreadsheets-de.acceptance.md` for a worked example); both
cleared the 0.85 bar in the Phase 4 acceptance run, so the choice only
affected the margin, not the pass verdict, there.

### If no instance is reachable (Ruling R68)

If `curl -fsS $JUSTRAG_URL/health` fails, or an instance is up but no admin
credential is available in the environment, the acceptance record documents
"not run" with the specific blocking reason instead of fabricating numbers.
That is not a failure of this task — see
`eval/golden/spreadsheets-de.acceptance.md`.

## CERT recency set (Wave 2 Task 8)

`cert-recency-de.jsonl` is a fully synthetic, fictional German CERT-advisory
corpus (`eval/fixtures/cert-advisories/*.md`, 40 files + `manifest.tsv`)
purpose-built to exercise the date-aware chat mechanisms that a static
corpus cannot: `chat_recency_listing_enabled` (the deterministic
"Welche neuen Meldungen gibt es?" listing path,
`internal/chat/recency_listing.go`) and `recency_boost_enabled` (the
exponential-decay freshness prior post-rerank,
`internal/vector/recency_boost.go`). 12 fictional products (OpenSSL, Citrix
NetScaler, Cisco IOS XE, Microsoft Exchange, Atlassian Confluence, Fortinet
FortiOS, Ivanti Connect Secure, VMware ESXi, Apache Tomcat, GitLab, Moodle,
TYPO3), 26 fictional `WID-SEC-2026-NNNN` advisories (14 issued as an
initial `NEU` advisory later followed by an `UPDATE` on the same WID id,
each UPDATE dated younger than its NEU; 12 issued once and never updated),
ages spread 1–120 days, 9 files inside the default 7-day recency-listing
window (5 of them NEU). All CVE numbers are fictional
(`CVE-2026-1xxxx`) and no real vendor text is used anywhere in the corpus.

File names follow the CERT-Bund feed convention the name-marker arm keys
on: `NEU WID-SEC-2026-0101 OpenSSL - Schwachstelle ermoeglicht Denial of
Service.md` / `UPDATE WID-SEC-2026-0101 OpenSSL - ....md`. Titles
deliberately avoid umlauts/ß (`ermoeglicht`, not `ermöglicht`) for
readability — the corpus's `.md` filename stem doubles as the exact
`title` sent to `POST /api/kb/{id}/text`, and the resulting `files.name`
is `req.Title` **verbatim**: `AddTextSource`
(`internal/files/http_ingest.go`) only runs the title through
`sanitizeTitle`/`SafeNameSegment` to build the on-disk *storage path*; the
`files.name` DB column it writes is the raw title, no extension appended
(confirmed against a live-seeded row — an earlier draft of this doc
assumed a `.txt` suffix that does not exist in practice). So
`must_cite_file_names` in the golden set are the titles as-is, matching
the corpus's `.md` filenames minus the extension.

25 questions in 5 groups: 6 recency-listing (`query_type: lookup`; `notes`
on each spells out which window/marker arm should fire and why, since
"Welche neuen Meldungen gibt es?"-style questions land on **every**
NEU-labeled file once the name-marker arm fires — see
`internal/chat/recency_listing.go`'s two-arm design — while a
window-only phrasing like "Welche Meldungen wurden in den letzten 6 Tagen
veröffentlicht?" is scoped to just that window's NEU files), 8 NEU/UPDATE
product lookups (`must_cite` is the UPDATE file only — tests whether the
recency boost/prior promotes the newer, more complete advisory over its
near-duplicate NEU predecessor), 6 CVE/WID-id lookups (lexical/BM25
exercise; two target a CVE that exists only in an UPDATE, not its NEU
predecessor), 3 cross-advisory enumerations (`query_type: enumeration`),
and 2 "newest for product" lookups (recency boost ranking a single file,
deliberately phrased to avoid tripping the recency-listing classifier).

`kb_id` in the committed file is the placeholder
`REPLACE_WITH_FIXTURE_KB_ID` (see "Ground truth by name, not by UUID"
above); one question (`cert-r06`) carries a `{TODAY-14}` placeholder in its
question text that the seed script rewrites to an ISO date.

### Seeding

```bash
JUSTRAG_URL=http://localhost:3000 \
JUSTRAG_ADMIN_USER=admin \
JUSTRAG_ADMIN_PASSWORD=<the ADMIN_PASSWORD the instance was started with> \
  ./eval/fixtures/seed-cert.sh
```

Logs in as the admin user, creates or reuses a KB named "CERT Fixtures",
ingests every corpus file via `POST /api/kb/{id}/text` (skipping any title
already present, so reruns are cheap), polls until ingestion finishes,
**verifies** every manifest row landed under its expected name (fails
loudly otherwise — a mismatch here would make the backdating UPDATE below
a silent 0-row no-op), then backdates each file's
`files.created_at` per `manifest.tsv`'s `days_ago` column via
`UPDATE files SET created_at = now() - make_interval(days => N) WHERE
kb_id = ... AND name = ...`, run through
`${PSQL_CMD:-docker compose -p justrag exec -T db psql -U postgres -d rag_db}`.
Only `files.created_at` needs backdating — both the date-window filter
(`SearchService.fileIDsInDateRange`) and the recency boost
(`fileCreatedTimes`) read `files.created_at` on the **main** DB; there is
no `document_chunks_<dim>` row to touch (verified against
`internal/vector/recency_boost.go`). Every run (fresh seed or `--restamp`)
re-runs the backdating UPDATEs relative to `now()`, so a previously seeded
KB stays inside the 7-day recency-listing window no matter how much
wall-clock time has passed since it was first seeded. Writes
`eval/golden/cert-recency-de.local.jsonl` — the committed set with `kb_id`
rewritten to the real KB id and `{TODAY-N}` rewritten to an ISO date. That
file is gitignored (`eval/golden/*.local.jsonl`); never edit
`cert-recency-de.jsonl` itself to point at a real KB.

`--restamp --kb-id <uuid>` skips ingestion entirely and only re-runs the
backdating UPDATEs (plus regenerating the `.local.jsonl`) against an
already-seeded KB — useful for refreshing the window without waiting for a
full re-ingest.

### Running the A/B

`cmd/eval` has no site_config to flip for `recency_boost_enabled` short of
the same overlay mechanism the other ablation flags use (`--bm25-tiered-boost`
etc.): pass `--recency-boost on|off` (a vector-layer key, applied via the
searchReader overlay — see `docs/retrieval.md`'s Recency prior section for
the underlying mechanism). `chat_recency_listing_enabled` and
`chat_date_awareness_enabled` both default **on** in production and need no
override for this set.

Write reports to a scratch location **outside** `eval/golden/` — that
directory holds committed golden sets and gitignored `*.local.jsonl`
copies only; an ad-hoc `--output` path landing there is an untracked file
`.gitignore` doesn't cover. The acceptance run below used
`.superpowers/sdd/2026-09-05-rag-sota-wave2/task8-out/` (gitignored
wholesale via `.superpowers/sdd/`) through the dev-stack helper
`run-eval.sh`, which builds `cmd/eval` fresh and exports the compose-stack
env:

```bash
OUT=.superpowers/sdd/2026-09-05-rag-sota-wave2/task8-out
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl \
  --production-context --recency-boost off \
  --output "$OUT/cert-off1.json"
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl \
  --production-context --recency-boost on \
  --output "$OUT/cert-on.json"
# Noise band: repeat the first (off) run and diff against cert-off1.json.
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl \
  --production-context --recency-boost off \
  --output "$OUT/cert-off2.json"
```

`--production-context` defaults `--orchestrator-dispatch` to `true`
(production-parity routing); the acceptance record's second table repeats
the same three runs with `--orchestrator-dispatch=false` to isolate the
recency mechanisms from the LLM query-classifier's run-to-run
non-determinism (recency listing fires only on the standard
`PrepareChatContext` path — a question the classifier routes to
`plan_execute`/`supervisor`/`agentic` skips it entirely for that turn).

See `eval/golden/cert-recency-de.acceptance.md` for both tables
(production-like dispatch-on, and dispatch-forced-standard), the 6 listing
questions' window-file `FinalChunks` coverage, the per-pair UPDATE-vs-NEU
ranking outcome, and a log line proving the recency listing fired under
`cmd/eval` (`grep recency` in the run's JSON log output).
