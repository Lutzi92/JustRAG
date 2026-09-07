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
| `expected_points`    | string[] | (optional, W4-R5) 2-6 short statements a complete answer must contain, scored by the `--judge` coverage judge. Loader caps: ≤ 12 points, ≤ 300 runes each. Absent/empty skips the coverage judge — see "Coverage judge" below. |
| `notes`              | string   | (optional) Human context; ignored by the runner.          |
| `turns`              | Turn[]   | (optional) Marks this row as a multi-turn conversation instead of a single question — see "Multi-turn set" below. When present, the top-level `question`/`must_cite_*` fields are not required; ground truth lives per turn. Each `Turn` is `{question, kind, query_type?, must_cite_file_names?, answer?, answer_sources?, notes?}` — `kind` is one of `corpus`, `pronoun_ref`, `topic_shift`, `answer_ref`, `post_abstain`; `must_cite_file_names` is required for every kind except `answer_ref`, where it defaults to the previous turn's `answer_sources`. **`cmd/eval`-only**: only `cmd/eval` calls `ExpandTurns` to replay a `turns` row, so `internal/eval.ParseGoldenSetContent` (the path behind the admin UI / `eval_golden_sets` DB copy) rejects any row with a non-empty `turns` array — save and run multi-turn sets as a file via `cmd/eval`, never through the admin UI. |
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

**Final decision (Wave 2, Task 9):** `chat_condense_keep_raw_enabled`
stays **off**. On `pronoun_ref` (n=12, the kind it should help), keep-raw
ON scored *worse* than OFF on both recall (0.833 → 0.806, a −2.8 pp delta
against a 2.8 pp same-flag noise band) and MRR (0.833 → 0.767, −6.7 pp,
well outside the 0.0 pp noise band on that metric) — the MTRAG
"rewrite ⊕ raw" gain did not replicate on this fixture. See
`docs/feature-recipes.md`'s "Rewrite ⊕ raw last turn" recipe and
`docs/retrieval.md`'s "Raw-query lane" section for the full numbers and
caveats.

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
| `--longcontext on\|off` | Per-run override for `chat_longcontext_enabled`. `on` puts `OrchLongContext` at the top of the eval orchestrator ladder, so a global-synthesis set can be measured on a deployment where the flag is off. Empty = live site_config. |
| `--longcontext-mode flat\|map_reduce` | Per-run override for `chat_longcontext_mode` — which consumer the long-context orchestrator uses. Only has an effect together with `--longcontext on` (or a live-on flag). Empty = live site_config, whose **default is `map_reduce` since Wave 5** (W5-R1), so `--longcontext-mode flat` is now the one that overrides it. An unrecognised stored value normalises to `flat`. |
| `--golden-query-type` | Forward each row's curated `query_type` into the retrieval pipeline instead of classifying the question. Default **off** so existing reports keep their historical shape. Does **not** affect orchestrator dispatch, which classifies independently — if a question fails to reach the intended orchestrator, rewrite the question, not the label. |
| `--bm25-mode ts_rank\|bm25` | Per-run override for `bm25_scoring_mode`. Combine with `--refresh-bm25-stats` (recomputes the golden set's KBs' BM25 statistics first) whenever the KB hasn't had a recent refresh — without stats the arm silently falls back to `ts_rank` and the A/B measures nothing. |
| `--bm25-tiered-boost on\|off` | Per-run override for `bm25_tiered_boost_enabled` (deprecated; see `docs/retrieval.md`). |
| `--recency-boost on\|off` | Per-run override for `recency_boost_enabled`. |
| `--rrf-weight-bm25 <f>` / `--rrf-weight-vector <f>` / `--rerank-blend-alpha <f>` | Per-run overrides for the fusion weights and the **global** reranker α. Per-route α overrides (`rerank_blend_alpha_lookup` etc.) are NOT overridden — set those in `site_configs` if you want to grid them. Used together with `--bm25-mode bm25` for the Wave-3 retune grid (`eval/golden/bm25-retune.acceptance.md`). |
| `--keep-raw on\|off` | Multi-turn only: per-run override for `chat_condense_keep_raw_enabled`. |
| `--conflict-surfacing on\|off` | Per-run override for `chat_conflict_surfacing_enabled` (W5-R7). `on` makes every turn whose assembled set spans ≥ 2 distinct files run the fast-tier conflict / supersession pass; the resulting report is written per question as `conflicts` in the JSON report — the same bare array a chat turn persists and streams (`claim, sourceA, sourceB, kind, newer, fileA, fileB`) — and the human summary gains a `Conflict surfacing:` block whenever at least one question carries an entry. Effective on the standard `PrepareChatContext` path and, under `--orchestrator-dispatch`, on the Supervisor path. Empty = live site_config. |

These are all per-run **overlays**: they wrap the site-config reader for that
process only and never write `site_configs`.
`--longcontext`/`--longcontext-mode`/`--conflict-surfacing` are chat-layer keys
and share one overlay wrapper (`chatOverlayReader` in `cmd/eval/main.go`),
chained after `--crag`; the vector-layer flags (`--bm25-mode`,
`--recency-boost`, …) use the separate `overlaySiteConfig`.

The conflict pass decides supersession DIRECTION from each source's
`published_at`/`created_at` date line, so `cmd/eval` wires the same
`chat.FileDateLookup` production uses (`eval.WithFileDates`). Without it every
date renders "unknown" and `newer` can only ever be `"unknown"` — which is
also what the public API / OpenAI-compat / MCP-server paths get today, since
they leave `FileDates` nil.

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
evaluates it against three metrics, plus a fourth when the row carries
`expected_points`:

- **Faithfulness** — fraction of claims in the answer that are supported by
  the retrieved context.
- **Answer relevance** — how well the answer addresses the question (Likert
  1..5 normalized to 0..1).
- **Context precision** — fraction of retrieved top-k chunks that are
  relevant to the question.
- **Coverage** (W4-R5, optional) — fraction of the row's `expected_points`
  the answer states or restates equivalently. See "Coverage judge" below.

Use `--judge-model <name>` to run judge prompts on a specific (typically
smaller/cheaper) model; empty falls back to the KB's chat model.

Judge calls use temperature 0 for determinism. All metrics are optional and
run independently — a single judge failure is captured in
`judge.judge_errors` and does not abort the others.

**Tolerant parsing + per-metric counts (Wave 4, W4-R1).** Judge responses are
parsed leniently so a well-formed *verdict* in a slightly off-shape envelope
is kept rather than discarded: a score emitted as a numeric string
(`"score":"5"`) is accepted and clamped to 1–5, and a boolean list whose
length differs from the contexts or points is truncated or padded, recorded
as an entry in `judge.judge_warnings`. A sample is dropped only when the JSON
cannot be parsed at all. Because a dropped sample used to shrink the
denominator invisibly, the aggregate now carries `faithfulness_n`,
`answer_relevance_n`, `context_precision_n` and `coverage_n` — how many
questions actually contributed to each mean — and the human summary prints
`(n=…)` next to every judge mean. `judged_count` keeps its old meaning
(questions with a Judge block at all). **Comparability:** pre-Wave-4 judge
numbers are unchanged for well-formed responses — the same response scores
the same — but `n` may now be higher on a set where the model occasionally
mis-shapes its reply, so compare `*_n` alongside the means when reading an
old report against a new one.

**Wave-3 comparability note:** since Wave 3, `ChatContextForQuestion` serves the
dispatched orchestrator's OWN assembled context for every orchestrator branch,
not just for long-context — before, orchestrator-branch questions missed that
cache and the judge graded a generic content-based answer instead of the prompt
the orchestrator actually built. Judge-mode numbers (faithfulness, context
precision) are therefore **not comparable** with pre-Wave-3 judge runs; re-run
the baseline if you need a delta. Retrieval metrics — and therefore `--baseline`
recall/MRR/nDCG deltas — are unaffected.

```bash
./cmd/eval/eval --golden ../eval/golden/example.jsonl --judge --judge-model gpt-4o-mini
```

### Cost note

Judge mode executes roughly 4 LLM calls per question (1 answer + 3 judges).
For a 20-question golden set on a mid-tier model, expect 80 calls total.
Plan accordingly. For fast iteration, keep `--judge` off and rely on
retrieval metrics alone.

## Pairwise judge

The Likert answer-relevance judge saturates: two configurations that differ
visibly to a reader both land around 0.9, so it cannot rank them. The
**pairwise preference judge** (ruling W4-R4) can — it never places an answer
on an absolute scale, it only says which of two answers is better for the
same question.

It is an **offline** mode: it reads the `judge.answer` of two finished reports
already persisted, so both must come from runs made with `--judge`. No
retrieval, no answer generation, no DB writes, no golden set — the two
report files are the whole input.

```bash
# 1. Produce two judged reports of the SAME golden set, one per config.
./cmd/eval/eval --golden ../eval/golden/global-synthesis-de.jsonl --judge \
  --production-context --longcontext on --longcontext-mode flat \
  --output flat.json
./cmd/eval/eval --golden ../eval/golden/global-synthesis-de.jsonl --judge \
  --production-context --longcontext on --longcontext-mode map_reduce \
  --output mapreduce.json

# 2. Compare them. The win rate is A's.
./cmd/eval/eval --pairwise-a flat.json --pairwise-b mapreduce.json \
  --judge-model gemma-4-26b --pairwise-out pairwise-flat-vs-mapreduce.json
```

Every pair is judged **twice with the positions swapped**, and a win counts
only when both orderings name the same answer. That is the whole point: an
LLM preference judge prefers whatever it read first, and without the swap a
position-biased judge hands side A a 100 % win rate. Pairs the judge flips on
are recorded as **ties**, reported separately and excluded from the win rate
(never averaged into it).

Output: win/tie/loss counts, the win rate with a **95 % Wilson** interval
(z = 1.96, over wins/(wins+losses)), the tie rate, a per-route breakdown
keyed on the golden `query_type` (`unclassified` when a row carries none),
and a per-question table with the winner and whether both orders agreed.
`--pairwise-out` additionally writes the whole result as JSON, including
both orders' reasoning per pair.

Reading it:

- **Win rate alone is not a result.** With 12 questions, 8–4 gives a Wilson
  interval of roughly [0.39, 0.86] — it does not exclude 0.5. The W4-R7
  decision rule (win rate ≥ 0.60 **and** Wilson lower bound > 0.50, on both
  of two independent report pairs) exists for exactly this reason.
- **A high tie rate means the instrument, not the configs, is speaking.**
  Ties include every pair the judge flipped on, so a run with 2 wins, 1 loss
  and 9 ties has a win rate of 0.667 resting on three decided pairs.
- Questions present in only one report (`only_in_a` / `only_in_b`) and pairs
  with a missing answer on one side are **skipped and counted**, never
  silently dropped — that is how you notice you compared two different
  golden sets.

Cost: 2 judge calls per pair, no answer generation — a 12-question set is 24
calls. Exit code is always 0 on a completed comparison (this measures, it
does not gate); only an unusable invocation (missing report, no shared
question ids, no reachable AI provider) exits non-zero.

Worked example: `eval/golden/global-synthesis-de.acceptance.md` §2 (Wave 4
flat-vs-map_reduce re-measurement) — two cross pairs, a self-pair control,
disagreement excerpts, and the observation that the W4-R7 per-pair Wilson
criterion is under-powered at 9–11 decisive pairs (one pair needed 9/11 wins
and landed on 8/11).

### Pooling two comparisons — `--pairwise-pool`

One comparison of 12 questions decides 9–11 pairs, and a Wilson interval on
that many pairs straddles 0.5 almost whatever the outcome — which is how
Wave 4 ended up with both cross pairs pointing the same way and neither
clearing the per-pair bar. `--pairwise-pool` sums two (or more) finished
`--pairwise-out` JSONs into one tally and **recomputes** the win rate, the
tie rate and the 95 % Wilson interval on the pooled counts:

```bash
./cmd/eval/eval --pairwise-out pooled-flat-vs-mapreduce.json \
  --pairwise-pool pw-flat1-vs-mr1.json pw-flat2-vs-mr2.json
```

The first path is the flag value, the rest are positional — so **every other
flag must come before them**. Go's flag parsing stops at the first positional
argument, which would turn a trailing `--pairwise-out pooled.json` into two
more "paths" while the real flag stayed empty; the command rejects that with
an error naming the cause instead of failing later on a missing file.

Recomputed, not averaged: averaging the two win rates would weight a pair with
4 decisive verdicts the same as one with 16. Ties stay out of every denominator exactly
as in `--pairwise-a/-b`, so pooling 3/1/8 and 1/3/8 gives 4 wins / 4 ties /
16 losses = **20 decisive pairs**, side B taking 16 of them: 0.800 with a
95 % Wilson of [0.584, 0.919]. (Folding those 4 ties into the denominator
would read 16/24 = 0.667, [0.467, 0.820] — the same data failing the rule.)

Output names the perspective on every line: the pooled counts are in side
**A's** terms, because that is how each input result is expressed, and side
**B's** win rate with its own Wilson interval is printed underneath, since a
rule may be stated in either direction and the bounds do not merely swap when
the perspective flips — they reflect (`low_B = 1 − high_A`). A per-input and
a per-route pooled table follow.

Two caveats. Pooling assumes **every input assigned the same configuration to
side A**; a pairwise JSON carries no report paths, so the command cannot check
that and prints a warning instead. And pooling is only honest when the rule
was registered on the pooled statistic **before** the runs — pooling after
seeing two per-pair results that each missed is exactly the analysis W4-R7 was
supposed to prevent. This mode reads only files: no retrieval, no judge, no
database. Exit 0 on a completed pooling, 2 on a usage error.

**Ruling W5-R1 (registered 2026-09-06, pre-registered before the extended-set
run).** The decision rule for `chat_longcontext_mode` is stated on the POOLED
decisive cross pairs: over N ≥ 24 questions, two cross pairs (flat1 vs mr1,
flat2 vs mr2), pooled `map_reduce` wins / (wins + losses) ≥ 0.60 with the
pooled Wilson lower bound (z = 1.96) > 0.50, **and** pooled mean coverage of
`map_reduce` not below flat's by more than the flat1-vs-flat2 coverage band.
The flat1-vs-flat2 control must stay inside a [0.35, 0.65] win rate — outside
it the judge is unstable and the run is inconclusive. Cost is reported, not a
veto. This replaces W4-R7 for all future runs.

Worked example: `eval/golden/global-synthesis-de.acceptance.md` §4 (Wave 5
re-measurement on the extended n=24 set) — all four sub-criteria pass
(pooled win rate 0.9444, Wilson low 0.8186, pooled coverage delta +5.03 pp
against a 1.39 pp band, control win rate 0.3636), so `chat_longcontext_mode`
flips to `map_reduce`.

## Coverage judge — optional `expected_points`

Faithfulness/answer-relevance/context-precision each grade some aspect of
the answer in isolation; none asks "did the answer actually say the things
a complete answer needs to say?" — an absolute (not pairwise) synthesis
metric, useful for a scheduled/nightly run where there's no second config
to compare against. The **coverage judge** (ruling W4-R5) fills that gap.

Add an optional `expected_points` array to a golden row: 2-6 short,
independently-checkable statements a complete answer must contain (loader
caps: ≤ 12 points total, ≤ 300 runes each — see the format table above).
Author them from ground truth (file contents, `must_cite_file_names`), not
from any model's answer.

```jsonl
{"id":"gs01","question":"...","kb_id":"...","language":"de","must_cite_file_names":["..."],"query_type":"global_synthesis","expected_points":["Nennt CVE-2026-1234","Nennt den betroffenen Produktnamen X","Nennt den CVSS-Score"]}
```

When `--judge` is on and the row carries `expected_points`, one additional
structured call scores `coverage = covered / len(expected_points)`: the
judge sees the question, the generated answer, and the numbered point list,
and returns one boolean per point in order (`{"covered":[true,false,...]}`).
Same tolerant parsing as the other boolean-list judge (context precision,
W4-R1): a mismatched boolean count is truncated/padded rather than dropped,
recorded as a `judge_warnings` entry. The answer and the points are treated
as **data**, not instructions — text inside either that looks like a
directive to the judge is ignored.

Rows **without** `expected_points` skip the coverage judge entirely — no
extra LLM call, `judge.coverage` stays absent (`null`), and the question
does not contribute to `aggregate.mean_coverage`/`coverage_n`. A report's
`aggregate.mean_coverage` averages over only the questions that both carry
`expected_points` and got a successful coverage call; `coverage_n` is that
count, printed as `mean_coverage = 0.xyz (n=N)` in the human summary
alongside the other judge means.

Cost: +1 LLM call per question that carries `expected_points`, on top of
the ~4 calls judge mode already makes (see "Cost note" above) — zero calls
added for rows without the field.

Worked example: `eval/golden/global-synthesis-de.acceptance.md` §2 — four
runs' `mean_coverage`/`coverage_n`, a per-question coverage table across
flat×2/map_reduce×2, and the flat-vs-flat run pair used as the coverage
noise band.

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

## Global-synthesis set (Wave 3 Task 4)

`global-synthesis-de.jsonl` — **gitignored** (derived from the JLU
Confluence corpus; the question text names real internal projects). **24**
German questions against the `PPM-Eval` KB
(`83262307-3a1b-49bc-bd08-3b925a868a92`, 297 files / 1815 chunks), all
`query_type: "global_synthesis"` — G01–G12 from Wave 3, G13–G24 added in
Wave 5 under ruling W5-R2 (6 `expected_points` each, every point verified
against a source chunk fragment; curation record in the acceptance file §3).
It exists to measure the long-context orchestrator (`OrchLongContext`, ruling
W3-R5) and to A/B its two consumers, `chat_longcontext_mode = flat` vs
`map_reduce` (W3-R6). The n=24 size is what made the pooled rule W5-R1
adequately powered, and that run flipped the default to `map_reduce`.

**Two gates have to fire for a question to reach that orchestrator**, and
the set is authored so both do, deterministically where possible:

1. `IsGlobalSynthesisQuery` — a pure lower-cased **substring** match against
   the trigger lists in `internal/chat/longcontext.go`. Every question
   therefore carries exactly one documented German trigger **verbatim,
   umlauts included**: `fasse alle` (G01/G05/G09), `überblick über alle`
   (G02/G10), `vergleiche alle` (G03/G08/G12), `gesamtbild` (G04/G11),
   `widersprüche in` (G06), `gemeinsame themen` (G07). An ASCII
   transliteration (`ueberblick`) silently does not match — the question
   then falls through to whatever orchestrator is next on the ladder and
   the run measures nothing. G07 is phrased without an article ("Nenne
   gemeinsame Themen, die …") so the trigger is both verbatim and
   grammatical.
2. The query-type classifier must return `complex_reasoning`. Every question
   is multi-clause ("… und …"), which keeps `ai.HeuristicComplexity` out of
   its short-single-clause `simple` shortcut and lets the LLM classifier
   decide. This arm is an LLM call and therefore **not** deterministic —
   confirm per run that the report's per-question `agent` is `longcontext`
   for all 12 (`grep eval.orchestrator_dispatch` in the run log). If a
   question drifts to `lookup`, rewrite the question; do not touch the
   classifier.

`must_cite_file_names` lists the 12–15 files a curator would expect per
topical cluster (migration/Ablösung, KI-Vorhaben, Informationssicherheit,
Netz/RZ, Campusmanagement, Workshop-Orga, Thementische,
Hochschul-Benchmark, IAM, Verwaltungsdigitalisierung, PPM-Governance,
Client-Management). **Recall/MRR on this set are diagnostics, not the
acceptance metric** (ruling W3-R8): the honest ground truth for a
global-synthesis question is "a large part of the corpus", and the
long-context pool is `chat_longcontext_top_k` (200) chunks against a k=10
metric cutoff, so recall is structurally capped far below 1.0. The
acceptance metrics are judge-based — **answer relevance primary,
faithfulness secondary**. Context precision is deliberately not used for
this route: the judge needs one boolean per context item and the pool is
200.

Two corpus gotchas worth knowing before extending the set: several
Confluence-exported file names contain a **non-breaking space** (U+00A0)
or a double space, so `must_cite_file_names` must be copied byte-exactly
from `SELECT name FROM files WHERE kb_id = …` rather than retyped; and the
KB holds ~20 "other university" pages, of which G08 lists 13 plus the
summary page rather than all of them.

### Running the flat vs map_reduce A/B

```bash
# (a) flat — byte-identical to the pre-orchestrator behaviour
bash <workspace>/run-eval.sh --golden eval/golden/global-synthesis-de.jsonl \
  --production-context --orchestrator-dispatch=true --judge \
  --longcontext on --longcontext-mode flat --output <workspace>/t4-a-flat.json

# (b) map_reduce, compared against (a)
bash <workspace>/run-eval.sh --golden eval/golden/global-synthesis-de.jsonl \
  --production-context --orchestrator-dispatch=true --judge \
  --longcontext on --longcontext-mode map_reduce \
  --baseline <workspace>/t4-a-flat.json --output <workspace>/t4-b-mapreduce.json

# (c) flat again — the noise band. NEVER conclude from (a) vs (b) alone.
```

`--baseline` compares **retrieval** metrics only and exits 3 on a regression;
on this set that gate is noise, so read the judge means out of the reports
(`.aggregate.mean_answer_relevance` / `.mean_faithfulness`) and compare the
(b)−(a) delta against the |(c)−(a)| noise band on **both** metrics. Map/reduce
group counts are trajectory events, not report fields — scrape them from the
run log (`rag.longcontext.map_reduce` carries `groups`, `failed_groups`,
`findings`, `pool`; `longcontext.map_group_failed` marks a degraded group).

Results and the standing recommendation: `global-synthesis-de.acceptance.md`
— §1 for the Wave-3 three-run A/B above, §2 for the Wave-4 re-measurement.

**Use the Wave-4 shape for any new attempt**, not the three-run one above:
answer relevance saturates on this route, so curate `expected_points` per row
(see "Coverage judge") and run **two** reports per mode — flat ×2 and
map_reduce ×2 — then compare each cross pair with `--pairwise-a/--pairwise-b`
plus one same-mode control pair. The control pair is what supplies both the
coverage noise band and the judge's tie rate at this margin. Wave 4's verdict
on that shape: `map_reduce` raised coverage in both cross pairs and took 16
of 20 pooled decisive pairs, but the pre-registered per-pair Wilson rule
missed on one pair at n=12 — grow the set to 24–36 questions, or re-register
the rule on the pooled pairs *before* the next run, rather than pooling after
seeing the result.

Wave 5 did both: the set grew to **24 questions** (G13–G24 authored under
ruling W5-R2, curation record in `global-synthesis-de.acceptance.md` §3), and
the rule was re-registered on the pooled pairs as **W5-R1** — see "Pooling two
comparisons — `--pairwise-pool`" above for the rule text and the command. All
four sub-criteria passed (pooled win rate 0.9444 over 36 decisive pairs,
Wilson low 0.8186, coverage +5.03 pp against a 1.39 pp band, control 0.3636),
so **`chat_longcontext_mode` now defaults to `map_reduce`** — record in
`global-synthesis-de.acceptance.md` §4. Cost, reported and not a veto: 1.28×
wall time. A run that wants the old consumer must pass `--longcontext-mode
flat` explicitly.

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

**Final decision (Wave 2, Task 9):** `recency_boost_enabled` stays **off**
by default. The standard-path-forced table (the trustworthy isolated
estimate, dispatch confound removed) shows overall recall 0.651 → 0.696
with the boost on, against a 4.0 pp same-flag noise band, and the
UPDATE-outranks-NEU win rate on the 8 NEU/UPDATE pairs rising from 3/8 to
5/8 — a marginal but directionally positive, noise-exceeding effect on
n=25 questions. Recommended for RSS/CERT-style time-sensitive KBs
specifically, not as a new global default. `chat_recency_listing_enabled`
and `chat_date_awareness_enabled` were both already default-on and are
unaffected by this decision; the fixture additionally confirms the
listing mechanism fires correctly whenever a question reaches the
standard path (it does not fire when orchestrator dispatch routes the
same question elsewhere — a pre-existing production interaction, not a
fixture defect). See `docs/retrieval.md`'s "Recency prior" section and
`docs/feature-recipes.md`'s "Recency prior" / "Date-aware chat" recipes.

## BM25 keyword-arm cost check (Wave 3 Task 7)

`eval/fixtures/bm25-scale/` holds a throwaway, SQL-only fixture for profiling
the keyword arm an order of magnitude above the production golden set:

```bash
# 1. Seed ~100k chunks (55 salted copies of the PPM-Eval corpus) into
#    document_chunks_768 under an obviously synthetic KB, then refresh that
#    KB's BM25 stats with the real refresher. Prints the KB id.
DB_PASSWORD=… JWT_SECRET=… eval/fixtures/bm25-scale/seed-scale-kb.sh --copies 55

# 2. EXPLAIN (ANALYZE, BUFFERS) both scoring builders x three query shapes.
#    The statements come from `cmd/eval --print-keyword-sql`, so they are the
#    real builders' output, not a hand-copy.
DB_PASSWORD=… JWT_SECRET=… eval/fixtures/bm25-scale/time-keyword-sql.sh \
  --kb-id 5ca1e000-0000-4000-8000-000000000001 --out /tmp/scale --label scale100k

# 3. Always drop it — it is a synthetic corpus with no embeddings.
eval/fixtures/bm25-scale/seed-scale-kb.sh --drop
```

`cmd/eval --print-keyword-sql "<query>" --kb-id <uuid> [--top-k 50]` is
usable on its own: it prints one JSON document carrying the keyword arm's
SQL for **both** scoring modes with the KB's real resolved settings (chunk
table, text-search config, simple arm, tiered boost, k1/b, dim-keyed stats
tables), including a placeholder-free `executable_sql` per mode. It runs no
search and needs no golden set.

Results and the measurement caveats: `eval/golden/bm25-retune.acceptance.md`
and `docs/retrieval.md` §"Keyword arm scoring: ts_rank vs BM25".
