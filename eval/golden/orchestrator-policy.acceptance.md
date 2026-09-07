# Orchestrator-policy measurement on the PPM fixture — acceptance record

- **Date:** 2026-09-07
- **Commit:** `6cdacae` (`feat/rag-sota-wave6`; this record lands on top of that commit)
- **Task:** Wave-6 Task 10 (`.superpowers/sdd/2026-09-07-rag-sota-wave6/task-10-brief.md`),
  rulings **W6-R7** (policy measurement), **W6-R7a** (measurement harness
  correction — dispatch-on production-context runs, not the trajectory
  harness), **W6-R8a** (tool-set measurement is not performed on dev; a
  documented operator procedure stands in), **W6-R12** (measurement
  discipline: standard-path features with `--orchestrator-dispatch=false`,
  policy/tool measurements with repeats where LLM dispatch is involved, every
  record states its rule before its numbers, no default flips except as
  ruled above).
- **Model stack (from the run logs):** `jlu/gemma-4-26b-it` (answer model),
  `jlu-internal/gemma-4-26b-it-bulk` (fast tier), `jlu/jina-rerank`
  (reranker); qwen3-embedding-8b / 4096-dim embedder (deployment default,
  embedding calls don't log a `model` field).
- **Fixture:** `eval/golden/production-ppm-2026-08.jsonl` — 89 questions
  (43 `lookup`, 32 `complex_reasoning`, 14 `enumeration`), KB `PPM-Eval`
  `83262307-3a1b-49bc-bd08-3b925a868a92`.
- **Live orchestrator flags on dev** (read-only SQL, taken immediately before
  the run):

  ```
  chat_plan_execute_enabled | true
  chat_agentic_enabled      | true
  chat_supervisor_enabled   | (unset/off)
  chat_plan_execute_dag     | (unset/off)
  chat_drift_enabled        | (unset/off)
  chat_longcontext_enabled  | (unset/off)
  ```

  So the **ladder cell** (no `--policy`) dispatches a LIVE-classified
  `complex_reasoning` turn to flat `plan_execute` (DAG off) and a
  LIVE-classified `lookup`/`enumeration` turn to `standard`
  (`PrepareChatContext`) — `chat_agentic_enabled` never wins because
  `plan_execute` sits above it on the ladder (see CLAUDE.md's orchestrator
  table). No `site_configs` row was written for this measurement; every cell
  is an eval-side overlay (`--policy`), per the global constraints.

## W6-R7a: why this is measured on `--production-context
--orchestrator-dispatch=true`, not the trajectory harness

`cmd/eval --trajectory` carries no retrieval metrics — `TrajectoryRecord.
RetrievalHits` is declared but never populated — and its `off` mode runs no
answer at all, so a `query_type × orchestrator → recall/MRR` table cannot be
built from it. The measurement instead runs `--production-context
--orchestrator-dispatch=true` (the real dispatch predicate,
`eval.OrchestratorDispatchAdapter`) five times with `--policy` forcing each
orchestrator in turn (`ladder`, `supervisor`, `plan_execute`,
`plan_execute_dag`, `agentic`), two repeats per cell. Recall/MRR come from
each question's `metrics` block; cost comes from the new per-question
`latency_ms` and `agent.llm_calls` (Task 10, commit `6cdacae`) — the
deliverable this task's code half exists to produce. `--trajectory` records
still carry `policy_rule`, but that field is a join key, not "what ran"
(Task-6 review note 7); it plays no part in this measurement.

S1 (a forced orchestrator on a `lookup`/`enumeration` query being
unreachable in production while the eval mirror asserted the opposite) and
Q1 (the eval mirror dropping `chat_plan_execute_dag` for a forced
`plan_execute`) were both resolved in Task 6's fix round 1 (commit
`910d9ad`), so every `--policy` row below — including the `lookup` and
`enumeration` rows of the forced cells — measures a route production can
actually take, not an eval-mirror artifact. `force plan_execute_dag` is used
verbatim (not `force plan_execute` relying on the DAG flag), matching Task
6's amended note 5.

## Commands

```bash
# t10-policy.sh — 5 cells x 2 repeats, sequential, from the worktree root
RUN=".superpowers/sdd/2026-09-07-rag-sota-wave6/run-eval.sh"
GOLDEN="eval/golden/production-ppm-2026-08.jsonl"
OUT=".superpowers/sdd/2026-09-07-rag-sota-wave6/t10-out"

"$RUN" --golden "$GOLDEN" --production-context --orchestrator-dispatch=true \
  --output "$OUT/ladder-<n>.json"                                    # ladder (no --policy)

"$RUN" --golden "$GOLDEN" --production-context --orchestrator-dispatch=true \
  --policy '[{"when":{},"orchestrator":"supervisor","mode":"force"}]' \
  --output "$OUT/supervisor-<n>.json"

"$RUN" --golden "$GOLDEN" --production-context --orchestrator-dispatch=true \
  --policy '[{"when":{},"orchestrator":"plan_execute","mode":"force"}]' \
  --output "$OUT/plan_execute-<n>.json"

"$RUN" --golden "$GOLDEN" --production-context --orchestrator-dispatch=true \
  --policy '[{"when":{},"orchestrator":"plan_execute_dag","mode":"force"}]' \
  --output "$OUT/plan_execute_dag-<n>.json"

"$RUN" --golden "$GOLDEN" --production-context --orchestrator-dispatch=true \
  --policy '[{"when":{},"orchestrator":"agentic","mode":"force"}]' \
  --output "$OUT/agentic-<n>.json"
```

for `<n>` in `1 2`. Full script:
`.superpowers/sdd/2026-09-07-rag-sota-wave6/t10-policy.sh`. Analysis:
`.superpowers/sdd/2026-09-07-rag-sota-wave6/t10-analyse.py t10-out` (run from
the same directory as the script). Raw reports and per-run logs are under
`.superpowers/sdd/2026-09-07-rag-sota-wave6/t10-out/<cell>-<n>.{json,log}`;
wall times in `t10-out/times.txt`; the full analysis transcript in
`t10-out/analysis.txt`.

## Wall time

| Cell | Repeat 1 | Repeat 2 |
|---|---|---|
| ladder | 26m29s | 22m14s |
| supervisor | 11m03s | 11m09s |
| plan_execute | 38m16s | 22m43s |
| plan_execute_dag | 35m02s | 35m37s |
| agentic | 85m44s | 91m17s |

Total: 6h19m34s for the ten runs. Both `agentic` repeats exceeded the brief's
40-minute-per-run flag (`t10-policy.sh` logs a `NOTE:` line for each, per the
brief's instruction to log and continue rather than abort); both repeats
completed cleanly (exit 0), so both are kept. `agentic`'s cost is consistent
with its own quality/cost numbers below — 2-4 critique hops per question at
full completion latency each, no early-exit fast path.

## Data-quality note: four transient embedding errors, unrelated to orchestrator choice

`plan_execute-1` and `plan_execute-2` each report 2 errored questions (all
other 8 reports: 0 errors). All four are transport-layer failures at the
SEARCH stage, before any orchestrator branch runs:

```
plan_execute-1  Q044 (enumeration): "generate embedding: context deadline exceeded"
plan_execute-1  Q045 (enumeration): "generate embedding: ... ai: do request: Post ... EOF"
plan_execute-2  Q012 (lookup):      "generate embedding: ... api error 502 ... Proxy Error"
plan_execute-2  Q013 (lookup):      "generate embedding: ... api error 502 ... Proxy Error"
```

Different questions in each repeat (no question fails twice), both a timeout
and a 502 from the shared embedding endpoint — an infra flake under the
`plan_execute` cell's own retry/backoff timing, not a defect in the
orchestrator-policy mechanism. Errored questions are excluded from
`Aggregate` (and hence from every mean below) by existing, pre-Wave-6
behaviour; `plan_execute`'s `lookup` bucket is n=84 (not 86) and its
`enumeration` bucket is n=26 (not 28) as a result — visible in the table
below.

## W6-R6 aside: one silent fallback observed

`plan_execute-1` question `Q046` (`enumeration`) shows
`agent.orchestrator = "standard"` with `dispatch_reason =
"plan_execute_error_fallback"` even though the cell's policy forces
`plan_execute` unconditionally — the forced orchestrator errored for that one
question and production's documented silent-fallback behaviour (Task-6
review, Q4) answered it on the standard path instead. `plan_execute-1`'s
`questions_routed_by_a_rule` is `86/89` (2 errors + this 1 fallback = 3 of 89
not routed by the rule), matching. This is the mechanism working as
documented, not a bug; it costs the `plan_execute` lookup/enumeration buckets
one extra non-`plan_execute` sample each cell x repeat where it occurs (only
`plan_execute-1`, once).

## RULES (verbatim, printed before every number by `t10-analyse.py`)

> cost = mean latency_ms and mean llm_calls per question; quality = mean
> recall and MRR at k=10; cells are compared per GOLDEN query_type (the
> row's own label) x agent.orchestrator; the noise band per route =
> |repeat1 - repeat2| on the ladder cell, floored at 1.0 pp; a difference
> counts only beyond the band.

## Noise band (ladder cell's own two repeats)

| Route | recall band | MRR band |
|---|---|---|
| lookup | 4.07 pp | 4.65 pp |
| enumeration | 1.00 pp (floor) | 1.00 pp (floor) |
| complex_reasoning | 2.01 pp | 1.00 pp (floor) |

## Per-cell x query_type table (pooled over the two repeats; spread = the cell's own two-repeat difference for that route)

`orch(s)` lists every `agent.orchestrator` value actually observed in that
bucket — a cell's forced policy does not guarantee a single label: the LIVE
classifier (independent of the golden `query_type` label) can route a
golden-`lookup`/`enumeration` question into the complex lane, and vice versa
(see the caveat below the table), and the one `plan_execute` fallback above
adds a `standard` sample to that cell.

| Cell | Route | n | orch(s) | recall | MRR | Δrecall | ΔMRR | latency_ms | llm_calls |
|---|---|---|---|---|---|---|---|---|---|
| ladder | lookup | 86 | plan_execute, standard | 0.861 | 0.872 | 4.07pp | 4.65pp | 12170.3 | 4.94 |
| ladder | enumeration | 28 | plan_execute, standard | 0.860 | 1.000 | 0.00pp | 0.00pp | 15989.2 | 7.07 |
| ladder | complex_reasoning | 64 | plan_execute, standard | 0.686 | 0.843 | 2.01pp | 0.91pp | 22284.3 | 9.59 |
| supervisor | lookup | 86 | supervisor | 0.876 | 0.884 | 2.33pp | 2.33pp | 7624.0 | 2.93 |
| supervisor | enumeration | 28 | supervisor | 0.864 | 1.000 | 0.00pp | 0.00pp | 6912.5 | 3.00 |
| supervisor | complex_reasoning | 64 | supervisor | 0.764 | 0.910 | 0.00pp | 0.21pp | 7531.2 | 3.00 |
| plan_execute | lookup | 84 | plan_execute | 0.881 | 0.869 | 0.09pp | 0.62pp | 10884.2 | 7.00 |
| plan_execute | enumeration | 26 | plan_execute, standard | 0.850 | 1.000 | 2.33pp | 0.00pp | 16721.4 | 9.54 |
| plan_execute | complex_reasoning | 64 | plan_execute | 0.728 | 0.838 | 0.00pp | 2.60pp | 19454.3 | 10.47 |
| plan_execute_dag | lookup | 86 | plan_execute_dag | 0.805 | 0.789 | 5.12pp | 3.72pp | 17760.5 | 7.91 |
| plan_execute_dag | enumeration | 28 | plan_execute_dag | 0.750 | 0.786 | 0.00pp | 0.00pp | 22952.9 | 9.79 |
| plan_execute_dag | complex_reasoning | 64 | plan_execute_dag | 0.734 | 0.734 | 3.12pp | 4.93pp | 32319.2 | 12.72 |
| agentic | lookup | 86 | agentic | 0.813 | 0.794 | 1.51pp | 3.99pp | 61100.1 | 8.48 |
| agentic | enumeration | 28 | agentic | 0.860 | 0.907 | 0.00pp | 6.43pp | 44847.2 | 8.43 |
| agentic | complex_reasoning | 64 | agentic | 0.690 | 0.812 | 3.12pp | 4.84pp | 64201.2 | 9.27 |

**Ladder cell's mixed dispatch is expected, not an artifact.** On `ladder-1`,
of the 32 `complex_reasoning`-labelled questions, only 22 were LIVE-classified
`complex_reasoning` and dispatched to `plan_execute`; 10 were
LIVE-reclassified `lookup`/`enumeration` and answered on `standard` — the
same route production would take for a turn the live classifier does not
consider complex (confirmed: `agent.classified_query_type` on those 10 rows
is exactly `lookup`/`enumeration`, never `complex_reasoning`, so this is S1's
gate resolving correctly, not the pre-S1 "no orchestrator enabled" fallback
path). Symmetrically, a handful of golden-`lookup`/`enumeration` questions get
LIVE-classified complex and land on `plan_execute` (1-4 per repeat). This
table pools whatever the cell actually produced for that GOLDEN route — the
honest measurement of "what happens to this route's questions under this
policy" — not an idealized one-orchestrator-per-bucket count.

## Strict (query_type x agent.orchestrator) pooled table, across all cells (reference)

The RULES text keys strictly on `(golden query_type, agent.orchestrator)`,
not on which cell produced the sample. Because of the live/golden
classification mismatch above, the same pair can appear in two cells (e.g. a
`ladder` `complex_reasoning` question landing on `plan_execute` joins the
same bucket as a forced-`plan_execute` question) — pooling across cells for
that strict key raises n but mixes "ladder's default choice happened to match"
with "the policy forced it". The per-cell table above is what installing a
given policy actually produces end to end and is what the recommendation
below is computed from; this table is reference-only.

| Route | Orchestrator | n | recall | MRR | latency_ms | llm_calls | source cells |
|---|---|---|---|---|---|---|---|
| lookup | agentic | 86 | 0.813 | 0.794 | 61100.1 | 8.48 | agentic |
| lookup | plan_execute | 88 | 0.880 | 0.875 | 10882.0 | 7.02 | ladder, plan_execute |
| lookup | plan_execute_dag | 86 | 0.805 | 0.789 | 17760.5 | 7.91 | plan_execute_dag |
| lookup | standard | 82 | 0.862 | 0.866 | 12235.5 | 4.82 | ladder |
| lookup | supervisor | 86 | 0.876 | 0.884 | 7624.0 | 2.93 | supervisor |
| enumeration | agentic | 28 | 0.860 | 0.907 | 44847.2 | 8.43 | agentic |
| enumeration | plan_execute | 32 | 0.803 | 1.000 | 18144.1 | 10.25 | ladder, plan_execute |
| enumeration | plan_execute_dag | 28 | 0.750 | 0.786 | 22952.9 | 9.79 | plan_execute_dag |
| enumeration | standard | 22 | 0.932 | 1.000 | 13720.2 | 5.36 | ladder, plan_execute |
| enumeration | supervisor | 28 | 0.864 | 1.000 | 6912.5 | 3.00 | supervisor |
| complex_reasoning | agentic | 64 | 0.690 | 0.812 | 64201.2 | 9.27 | agentic |
| complex_reasoning | plan_execute | 108 | 0.681 | 0.830 | 22303.7 | 10.89 | ladder, plan_execute |
| complex_reasoning | plan_execute_dag | 64 | 0.734 | 0.734 | 32319.2 | 12.72 | plan_execute_dag |
| complex_reasoning | standard | 20 | 0.850 | 0.900 | 13123.5 | 5.40 | ladder |
| complex_reasoning | supervisor | 64 | 0.764 | 0.910 | 7531.2 | 3.00 | supervisor |

## Recommendation (best pooled MRR within the ladder-derived band, tie -> lowest mean latency)

| Route | Winner | MRR | Band | Best MRR | Latency ms | Tie set |
|---|---|---|---|---|---|---|
| lookup | supervisor | 0.884 | 4.65pp | 0.884 | 7624.0 | ladder, supervisor, plan_execute |
| enumeration | supervisor | 1.000 | 1.00pp | 1.000 | 6912.5 | ladder, supervisor, plan_execute |
| complex_reasoning | supervisor | 0.910 | 1.00pp | 0.910 | 7531.2 | supervisor (alone — the only cell within band of its own 0.910 best) |

On this fixture, `supervisor` is the recommendation on **every** route: it
either wins outright on MRR (`complex_reasoning`, by 6.7pp+ over every other
cell — 0.910 vs the next best 0.843 on `ladder`, well past the 1.00pp band)
or is statistically tied with the ladder's default and the flat planner on
`lookup`/`enumeration`, and it is the **cheapest** orchestrator on every route
by a wide margin: ~7-8s per question and 3 LLM calls vs 10-64s and 5-13 calls
for everything else. `plan_execute_dag` underperforms flat `plan_execute` on
both quality (MRR 0.734-0.789 vs 0.838-1.000) and cost (17.8-32.3s / 7.9-12.7
calls vs 10.9-19.5s / 7.0-10.5 calls) on every route in this fixture —
consistent with Wave 4's `bm25`-grid finding that the DAG planner's extra
structure did not pay for itself on this same 89-question set, though that
was a different knob. `agentic` is the most expensive orchestrator measured
(44.8-64.2s per question) without a compensating quality gain.

```json
[
  {"when": {"query_type": ["lookup"]}, "orchestrator": "supervisor", "mode": "force"},
  {"when": {"query_type": ["enumeration"]}, "orchestrator": "supervisor", "mode": "force"},
  {"when": {"query_type": ["complex_reasoning"]}, "orchestrator": "supervisor", "mode": "force"}
]
```

Since the recommendation is uniform across all three routes on this fixture,
the equivalent (and simpler) single-rule document is:

```json
[
  {"when": {}, "orchestrator": "supervisor", "mode": "force"}
]
```

**The shipped default stays `[]`.** This is a single-fixture,
single-deployment-config measurement (dev's live flags: `plan_execute` +
`agentic` on, `supervisor`/`plan_execute_dag`/`drift`/`longcontext` off) —
exactly the caveat W6-R7 requires: a recommended policy for the dev fixture,
documentation only, not a default flip. An operator who wants this policy
enables it explicitly via the admin Agent panel's `chat_orchestrator_policy`
editor (Task 8) or a `site_configs` write; nothing in this task changes what
a fresh deployment does.

## W6-R8a: tool sets were not measured

`chat_answer_tools_enabled` is off on dev (not among the flags read above,
and not set by this task), and `cmd/eval`'s harness — trajectory or
production-context — never drives the answer-time tools loop
(`RunAnswerWithTools`), so `agent_decisions.tool_calls` has nothing to
measure here: **not measured on dev**, per the ruling. The unit tests at both
boundaries this task's code touches (`internal/ai/callcount_test.go`,
`internal/eval/report_test.go`'s `TestAggregate_MeanLatencyAndLLMCalls`) stand
in for what live traffic would otherwise exercise.

The documented operator procedure for measuring per-route tool-mix drift once
answer tools are live, comparing a window before/after a policy or route-tools
change:

```sql
-- Per (orchestrator mode, tool name) call counts and per-decision average
-- tool-call count, over a time window. mode is agent_decisions.mode (the
-- orchestrator that answered); tool_calls is a JSONB array of {name, ...}.
SELECT
  mode,
  jsonb_array_elements(tool_calls) ->> 'name' AS tool_name,
  count(*) AS calls,
  count(DISTINCT id) AS decisions_using_tool
FROM agent_decisions
WHERE created_at > NOW() - INTERVAL '7 days'
  AND tool_calls IS NOT NULL
  AND jsonb_array_length(tool_calls) > 0
GROUP BY mode, tool_name
ORDER BY mode, calls DESC;

-- Mean tool-call count per decision, per mode (the denominator every
-- decision, including ones with zero tool calls):
SELECT
  mode,
  count(*) AS decisions,
  avg(coalesce(jsonb_array_length(tool_calls), 0)) AS mean_tool_calls
FROM agent_decisions
WHERE created_at > NOW() - INTERVAL '7 days'
GROUP BY mode
ORDER BY mode;
```

Run once before and once after the change under test (same window length),
diff the two result sets. Requires live production or staging traffic with
`chat_answer_tools_enabled = true` and at least one MCP tool registered;
inapplicable to the dev stack used for this measurement.

## Verification

- `go test ./...`, `go vet ./...`, `gofmt -l .` all clean from `go-backend/`
  before this record was written (see Task 10's code commit `6cdacae`).
- `go build -o /dev/null ./cmd/eval` clean; `run-eval.sh` rebuilds the binary
  fresh for every one of the ten runs.
- Every run's exit code was 0 (`t10-out/times.txt`); the four transient
  embedding errors above are the only per-question errors across 890
  question-attempts (89 questions x 10 runs).
- `--policy` overlay confirmed present in every forced cell's log
  (`"eval: applying chat site_config overlays for this run"`), and each
  forced cell's `questions_routed_by_a_rule` matches (89/89, or 86/89 for
  `plan_execute-1` accounting for its 2 errors + 1 fallback).
- No `site_configs` row was written by this measurement (every override is a
  `cmd/eval` flag); confirmed by re-reading the same read-only SQL after the
  run and getting the identical result.
