# BM25 retune grid + 100k-chunk cost check — acceptance record

- **Date:** 2026-09-06
- **Commit:** `e81e763` (`feat/rag-sota-wave3`; the docs commit lands on top of
  this record)
- **Task:** Wave-3 Task 7 (`.superpowers/sdd/2026-09-06-rag-sota-wave3/task-7-brief.md`),
  rulings **W3-R13** (retune decision rule) and **W3-R14** (synthetic scale check)
- **Model stack (from the run logs):** `jlu/gemma-4-26b-it` (answer / CRAG
  grader), `jlu/jina-rerank` (reranker), qwen3-embedding-8b / 4096-dim
  embedder (deployment default; embedding calls don't log a `model` field).
- **Fixture:** `eval/golden/production-ppm-2026-08.jsonl` — 89 questions
  (43 `lookup`, 32 `complex_reasoning`, 14 `enumeration`), KB `PPM-Eval`
  `83262307-3a1b-49bc-bd08-3b925a868a92`, 1815 chunks in
  `document_chunks_4096`, 297 files.
- **Run mode:** `--top-k 10 --production-context --orchestrator-dispatch=false
  --refresh-bm25-stats`, no judge. Dispatch is forced off so the grid measures
  the retrieval pipeline rather than the LLM orchestrator classifier's own
  non-determinism (the Wave-2 A/B convention for this fixture).
- **No `site_configs` mutation.** Every cell's knobs are per-run overlay flags
  (`--bm25-mode`, `--rrf-weight-bm25`, `--rerank-blend-alpha`), applied through
  `cmd/eval`'s `overlaySiteConfig` wrapper. Live values at run time:
  `bm25_simple_arm_enabled=true`, `rerank_blend_alpha=0.8`,
  `rerank_blend_alpha_entity=0.3`, `rrf_weight_bm25`/`rrf_weight_vector` unset
  (=1.0), `bm25_scoring_mode` unset (=`ts_rank`), `bm25_tiered_boost_enabled`
  unset (=off). Per-KB overrides on PPM-Eval include `crag_enabled=true`,
  `chat_supervisor_enabled=true`, `query_cache_enabled=false`.

## Commands

```bash
# The whole grid, sequential, one cell per run (script:
# .superpowers/sdd/2026-09-06-rag-sota-wave3/t7-grid.sh)
run-eval.sh --golden eval/golden/production-ppm-2026-08.jsonl --top-k 10 \
  --production-context --orchestrator-dispatch=false --refresh-bm25-stats \
  --bm25-mode ts_rank --output …/t7-A.json                       # A  (baseline)
… --bm25-mode ts_rank --baseline …/t7-A.json --output …/t7-A2.json   # A2 (noise repeat)
… --bm25-mode bm25 --rrf-weight-bm25 <0.5|0.75|1.0> \
  --rerank-blend-alpha <0.6|0.8> --baseline …/t7-A.json          # the 6 bm25 cells
```

`--refresh-bm25-stats` runs on **every** cell (not just the bm25 ones) so the
cells are procedurally identical; on a `ts_rank` cell it is a no-op for
scoring.

## Step 1 — the retune grid (W3-R13)

Eight cells, sequential, one `cmd/eval` process each; wall time 16m18s–17m39s
per cell (2h13m total). Raw reports: `t7-<cell>.json`, run logs
`t7-<cell>.log`, wall times `t7-walltimes.txt` (all under
`.superpowers/sdd/2026-09-06-rag-sota-wave3/`).

### Evidence the mode actually ran

| Cell | `keyword_mode` in `rag.search.stages` | BM25 stats fallbacks | `cmd/eval --baseline` exit |
|---|---|---|---|
| A | 94 × `ts_rank` | 0 | 0 (no baseline) |
| A2 | 97 × `ts_rank` | 0 | **3** |
| bm25 w0.5 α0.6 | 95 × `bm25` | 0 | 0 |
| bm25 w0.5 α0.8 | 95 × `bm25` | 0 | 0 |
| bm25 w0.75 α0.6 | 93 × `bm25` | 0 | 0 |
| bm25 w0.75 α0.8 | 96 × `bm25` | 0 | 0 |
| bm25 w1.0 α0.6 | 95 × `bm25` | 0 | 0 |
| bm25 w1.0 α0.8 | 95 × `bm25` | 0 | 0 |

Not one search in any `bm25` cell fell back to `ts_rank` (`grep` count 0 for
every fallback reason), so every cell measured the mode it names. The overlay
line is in each log, e.g.
`"overlays":{"bm25_scoring_mode":"bm25","rerank_blend_alpha":"0.6","rrf_weight_bm25":"0.5"}`.

The **A2 row is the important one**: a same-flag repeat of the baseline
tripped `cmd/eval`'s own regression gate (default thresholds recall 2 pp /
MRR 3 pp) on `lookup.recall` (−4.7 pp), `lookup.mrr` (−4.7 pp) and
`overall.recall` (−2.2 pp), while **every** `bm25` cell passed the same gate.

### Results

| Cell | Mode | w_bm25 | α | errors | overall R/MRR/nDCG | lookup R/MRR/nDCG | enumeration R/MRR/nDCG | complex_reasoning R/MRR/nDCG | wall |
|---|---|---|---|---|---|---|---|---|---|
| A | ts_rank | live | live | 0 | 0.828/0.906/0.906 | 0.887/0.895/0.898 | 0.864/1.000/1.000 | 0.733/0.878/0.876 | 16m49s |
| A2 | ts_rank | live | live | 0 | 0.806/0.888/0.888 | 0.841/0.849/0.852 | 0.864/1.000/1.000 | 0.733/0.891/0.887 | 17m39s |
| bm25-w0.5-a0.6 | bm25 | 0.5 | 0.6 | 0 | 0.856/0.916/0.918 | 0.909/0.895/0.904 | 0.899/1.000/1.000 | 0.765/0.906/0.902 | 16m29s |
| bm25-w0.5-a0.8 | bm25 | 0.5 | 0.8 | 0 | 0.850/0.916/0.919 | 0.909/0.895/0.904 | 0.899/1.000/1.000 | 0.749/0.906/0.902 | 16m38s |
| bm25-w0.75-a0.6 | bm25 | 0.75 | 0.6 | 0 | 0.828/0.899/0.900 | 0.886/0.884/0.890 | 0.903/1.000/0.993 | 0.718/0.875/0.872 | 16m18s |
| bm25-w0.75-a0.8 | bm25 | 0.75 | 0.8 | 0 | 0.844/0.904/0.908 | 0.886/0.872/0.881 | 0.899/1.000/1.000 | 0.764/0.906/0.902 | 16m44s |
| bm25-w1.0-a0.6 | bm25 | 1.0 | 0.6 | 0 | 0.846/0.904/0.907 | 0.909/0.895/0.904 | 0.899/1.000/1.000 | 0.738/0.875/0.869 | 16m34s |
| bm25-w1.0-a0.8 | bm25 | 1.0 | 0.8 | 0 | 0.843/0.899/0.900 | 0.886/0.884/0.890 | 0.935/1.000/0.994 | 0.744/0.875/0.871 | 16m44s |

Deltas vs A (percentage points, recall / MRR):

| Cell | overall ΔR | overall ΔMRR | lookup ΔR | lookup ΔMRR | enumeration ΔR | enumeration ΔMRR | complex_reasoning ΔR | complex_reasoning ΔMRR |
|---|---|---|---|---|---|---|---|---|
| A2 | -2.2 | -1.8 | -4.7 | -4.7 | +0.0 | +0.0 | +0.0 | +1.2 |
| bm25-w0.5-a0.6 | +2.8 | +1.0 | +2.2 | +0.0 | +3.6 | +0.0 | +3.2 | +2.8 |
| bm25-w0.5-a0.8 | +2.2 | +1.0 | +2.2 | +0.0 | +3.6 | +0.0 | +1.6 | +2.8 |
| bm25-w0.75-a0.6 | +0.0 | -0.7 | -0.1 | -1.2 | +3.9 | +0.0 | -1.5 | -0.3 |
| bm25-w0.75-a0.8 | +1.6 | -0.1 | -0.1 | -2.3 | +3.6 | +0.0 | +3.1 | +2.8 |
| bm25-w1.0-a0.6 | +1.8 | -0.1 | +2.2 | +0.0 | +3.6 | +0.0 | +0.5 | -0.3 |
| bm25-w1.0-a0.8 | +1.5 | -0.7 | -0.1 | -1.2 | +7.1 | +0.0 | +1.1 | -0.3 |

Noise band and the rule applied mechanically (output of `t7-analyse.py`):

```
Noise band (|A2 − A|, pp):
  overall: recall 2.2 / MRR 1.8
  lookup: recall 4.7 / MRR 4.7
  enumeration: recall 0.0 / MRR 0.0
  complex_reasoning: recall 0.0 / MRR 1.2

W3-R13 decision rule (win = lookup MRR gain > lookup-MRR noise band AND
no route loses recall or MRR beyond that route's own noise band):
  bm25-w0.5-a0.6: no  (lookup MRR +0.0pp <= noise 4.7pp)
  bm25-w0.5-a0.8: no  (lookup MRR +0.0pp <= noise 4.7pp)
  bm25-w0.75-a0.6: no  (lookup MRR -1.2pp <= noise 4.7pp; complex_reasoning recall -1.5pp < -0.0pp)
  bm25-w0.75-a0.8: no  (lookup MRR -2.3pp <= noise 4.7pp)
  bm25-w1.0-a0.6: no  (lookup MRR +0.0pp <= noise 4.7pp)
  bm25-w1.0-a0.8: no  (lookup MRR -1.2pp <= noise 4.7pp)
```

### Decision

**No cell wins. The `bm25_scoring_mode` default stays `ts_rank`, and no
`rrf_weight_*` / `rerank_blend_alpha` default changes.**

W3-R13: *flip only if a cell beats the baseline on lookup MRR beyond the noise
band **and** loses no route's recall or MRR beyond noise.* Four of six cells
satisfy the second half. The first half fails in every cell — no `bm25` cell
moved lookup MRR at all (best +0.0 pp, worst −2.3 pp) against a 4.7 pp
lookup-MRR noise band. The rule is applied as written; the outcome is "no
change to the default", which is a valid result.

Two findings behind that verdict:

1. **Wave 2's `complex_reasoning` MRR regression does not appear on the
   standard path — and this grid did not test the path it was measured on.**
   The two runs differ in more than the weights: Wave 2's A/B
   (`ab-A.json`/`ab-C.json`) ran `--production-context` with orchestrator
   dispatch at its **default (on)**, which sent 27–29 of the 89 questions —
   the entire `complex_reasoning` route among them — through **plan-execute**.
   This grid ran `--orchestrator-dispatch=false`, so every question took the
   standard `PrepareChatContext` path and **plan-execute was never
   exercised**. The `w=1.0, α=0.8` cell here is therefore *not* a rerun of
   Wave-2's cell C, and the −7.0 pp is neither reproduced nor refuted by these
   numbers. The supported statement is: **at identical weights, `bm25` costs
   −0.3 pp of `complex_reasoning` MRR on the standard path**, inside that
   route's 1.2 pp band. Wave 2's "RRF weights are off-scale for bm25's score
   range" explanation is correspondingly **untested** here, not disproven —
   it gains no support on the standard path (the effect is not monotone in the
   weight: C1/C2/C4 +2.8 pp, C3/C5/C6 −0.3 pp), but the path it was proposed
   for was not measured. **Follow-up:** rerun the grid with
   `--orchestrator-dispatch=true`.
2. **The fixture's resolution on `lookup` is ~5 pp with one run per cell.**
   CRAG is enabled on this KB, putting an LLM call inside every question's
   retrieval path; that is the likeliest dominant variance source. A future
   retune of these weights needs repeated runs per cell, or `--crag off`,
   before a 3–5 pp effect is interpretable.

**Direction-stable but not universal:** `bm25` lifts recall in most cells,
not in all. Enumeration recall is up in all six (+3.6 to +7.1 pp) and overall
recall is up or flat in all six (+0.0 to +2.8 pp); but lookup recall is
−0.1 pp in three cells, and **C3 (0.75 / 0.6) loses 1.5 pp of
`complex_reasoning` recall** — the grid's only route-recall loss, and the one
the mechanical rule check flags as violating the second half of W3-R13 as
well as the first. MRR is flat to slightly better everywhere except C4's
lookup (−2.3 pp). The recall direction agrees with Wave 2's; the magnitude is
smaller and the exceptions are real.

**Documented operating point** for an operator who opts a KB into `bm25`
(explicitly NOT a new default, and NOT a change for `ts_rank` deployments,
which keep `rrf_weight_bm25 = rrf_weight_vector = 1.0` and α 0.8):
**`rrf_weight_bm25 = 0.5`, α unchanged at 0.8** — cell C2, the best-behaved
cell that keeps the live α (largest overall MRR gain, +1.0 pp; no route below
baseline on either metric). The α-0.6 twin is equivalent within noise;
nothing here justifies moving α.

### Caveats

- One run per cell; the "noise band" is a single repeat of the baseline, not a
  variance estimate. The enumeration route's 0.0 pp band is what one repeat
  over 14 questions produces, not evidence of zero variance — treat the rule's
  per-route recall/MRR guard on that route as approximate. (The Wave-4
  dispatch-on rerun below therefore floors every per-route band at 1.0 pp and
  runs 3 repeats per cell.)
- The band (4.7 pp on lookup) is larger than most of the effects being
  measured, which is the honest summary of what this grid can and cannot say.
- `rerank_blend_alpha_entity = 0.3` is a live per-route override that
  `--rerank-blend-alpha` does **not** touch (by design), so entity-shaped
  queries used α 0.3 in every cell including the α-0.6 ones.
- Orchestrator dispatch is off, so these numbers describe the standard
  `PrepareChatContext` retrieval path, not what the Supervisor path would
  produce on the same questions.
- The working tree gained commit `b018db0` at 11:20, between cell A (started
  11:13) and cell A2 (started 11:30); `run-eval.sh` rebuilds the binary per
  cell, so **A ran the pre-change binary and A2 plus all six `bm25` cells ran
  the post-change one**. That commit touches only `--print-keyword-sql`'s
  literal-inlining helper — no code on the search path, no behaviour reachable
  from an eval run — and the rendered SQL was verified byte-identical before
  and after. It is disclosed because the baseline and its own repeat were not
  built from identical trees, not because it can explain the A-vs-A2 gap.

## Step 2 — 100k-chunk cost check (W3-R14)

**Corpus.** `eval/fixtures/bm25-scale/seed-scale-kb.sh --copies 55` seeds a
throwaway KB (`5ca1e000-0000-4000-8000-000000000001`,
"ZZZ-SYNTHETIC bm25 scale check") into `document_chunks_768` — **99 825
chunks / 16 335 synthetic file ids**, each row a copy of a PPM-Eval chunk
with a per-copy `salt<N>` token appended, tsvectors recomputed with the same
expression ingest uses (`to_tsvector(<cfg>, COALESCE(contextual_prefix,'') ||
' ' || content)`) for both the `german` and the `simple` arm. `embedding`
stays NULL (nullable; the keyword arm never reads it). `ANALYZE` afterwards,
then BM25 stats via the **real** refresher (`cmd/eval --refresh-bm25-stats`
→ `vector.BM25StatsRefresher.RefreshKB`), which reported
`lang doc_count=99 825 avg_len=95.40 / 16 842 terms` and
`simple doc_count=99 825 avg_len=124.09 / 20 697 terms`. The lang term count
is exactly PPM's 16 787 + 55 salt tokens, i.e. the salt did what W3-R14
intended: document frequencies scale with the corpus instead of every lexeme
appearing in 100 % of documents.

**Statements.** Both builders' SQL comes from
`cmd/eval --print-keyword-sql "<q>" --kb-id <id> --top-k 50` (new in this
task), so what was EXPLAINed is what the query path sends, not a hand-copy.
LIMIT 50 = the legacy pre-rerank candidate depth at top-k 10 with a reranker.
Three query shapes: `rare` (`Stud.IP-Update Projektsteckbrief`), `common`
(`die Verwaltung der Daten und Systeme`), `phrase` (`"Stud.IP"` — quoted, no
unquoted remainder, so no websearch/OR-floor group). 3 runs each, median
reported; plans in `.superpowers/sdd/2026-09-06-rag-sota-wave3/t7-scale/`.

| Query shape | Mode | Candidates 1.8k | Candidates 100k | Exec ms 1.8k | Exec ms 100k | Growth | Plan ms 1.8k | Plan ms 100k | GIN 1.8k | GIN 100k | Buffers 100k (shared hit) | cand CTE 100k |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| rare | `ts_rank` | 817 | 44935 | 7.7 | 206.8 | 27× | 1.6 | 1.4 | yes | no | 909,982 | — |
| rare | `bm25` | 817 | 44935 | 52.3 | 2648.7 | 51× | 2.8 | 2.4 | yes | no | 1,207,096 | 6.3 MB (Memory) |
| common | `ts_rank` | 1766 | 97130 | 18.2 | 282.0 | 16× | 1.7 | 1.7 | no | no | 1,344,949 | — |
| common | `bm25` | 1766 | 97130 | 113.1 | 5623.2 | 50× | 2.8 | 2.9 | no | no | 2,653,993 | 17.2 MB (Memory) |
| phrase | `ts_rank` | 64 | 3520 | 1.0 | 26.2 | 25× | 1.3 | 1.3 | yes | yes | 28,638 | — |
| phrase | `bm25` | 64 | 3520 | 4.5 | 207.4 | 46× | 2.2 | 2.6 | yes | yes | 56,017 | 0.5 MB (Memory) |

**bm25 / ts_rank execution-time ratio** (median of 3): `rare` 6.8× → 12.8×,
`common` 6.2× → 19.9×, `phrase` 4.3× → 7.9× going from 1.8k to 100k chunks.

**Part of the widening is lost parallelism, not extra work.** At 100k the two
low-selectivity `ts_rank` plans go parallel (`Gather Merge`, `Workers
Launched: 2` plus the leader, `loops=3` on the `Parallel Seq Scan`) while both
`bm25` plans stay **serial** — the materialised `cand` CTE blocks parallelism.
Charging the `ts_rank` side for its extra workers puts the ratio at roughly
**6.6× (`common`)** and **4.3× (`rare`)** in CPU terms rather than 19.9× and
12.8× in wall-clock terms. The `phrase` shape is serial on both sides at both
sizes (no `Gather` in any of its plans), so its 7.9× is a clean like-for-like
number. Wall clock is what a user waits for, so the operational conclusion
stands — but the CPU-side gap is materially smaller than the wall-clock gap.
The candidate count grows exactly 55× (the copy factor) on all three shapes;
`ts_rank` execution grows 16–27×, `bm25` grows 46–51×. **BM25's relative cost
therefore grows with the corpus**, it does not merely track it: the per-
candidate work (`unnest` of each candidate's tsvector, a join against the
query lexemes, a `LEFT JOIN` to `bm25_term_stats_*` per matched lexeme, per
arm) is what scales, and the simple arm doubles it.

**Headline.** The worst case measured is `bm25` + `common` at 100k chunks:
**5.62 s** for one keyword arm, against 282 ms for `ts_rank` on the same
candidate set. That is a full chat turn's latency budget spent inside one of
the two retrieval arms, and the arm runs once per query *plus* once per
alternative phrasing when multi-query/RAG-Fusion/sub-queries are on. At the
1815-chunk production fixture the same comparison is 113 ms vs 18 ms — which
is why Wave 2's latency measurement (mean `rag.search` duration flat across
modes) saw nothing: at that size the difference is inside the reranker's
noise.

**Memory / temp files.** The `cand` CTE is materialised; the plan reports
`Storage: Memory` with `Maximum Storage` 471 kB (`phrase`), 6.3 MB (`rare`)
and 17.2 MB (`common`) at 100k. No spill: `Sort Method` stayed
`top-N heapsort` (31 kB) in every bm25 plan, no `external merge`, no
`temp read/written` in any of the 36 plans. So the footprint is bounded by
the candidate count, not by the corpus, and stays in `work_mem` at this size
— but it is per concurrent query, and 17.2 MB × concurrency is a real number
for a busy deployment.

**Index usage.** The two GIN indexes (`…_vector_index_idx`,
`…_vector_index_simple_idx`) are used for the selective `phrase` shape at
both sizes. For `rare` (45 % of the corpus matches, because the OR-token
recall floor is deliberately generous) the planner uses GIN at 1.8k and
switches to a sequential scan at 100k; for `common` (97 % match) it never
uses GIN at either size. That is the planner doing the right thing — a bitmap
over 45–97 % of a table is more expensive than reading it — and it is
identical in both scoring modes, since the candidate WHERE clause is
byte-identical between them.

**Caveats.**
1. Synthetic text (W3-R14): 55 near-duplicate copies of one corpus. Lexeme
   *frequencies* are realistic by construction, but the vocabulary is not 55×
   richer than PPM's, so a real 100k-chunk corpus would have a larger
   `bm25_term_stats` table and slightly different join selectivity.
2. The scale KB is the **only** occupant of `document_chunks_768`, so
   `kb_id = …` has no selectivity and the planner has no reason to prefer the
   `kb_id` index. In production a 100k-chunk KB sharing a dim table with
   other KBs would likely get a bitmap scan on `kb_id` instead of a seq scan;
   the buffer counts would change, the per-candidate scoring work — which is
   what makes bm25 5.6 s — would not.
3. Dim 768 vs production's 4096: irrelevant to the keyword arm (it reads
   `content`/`vector_index` only, never `embedding`), but it does mean the
   heap rows are much narrower than a real 4096-dim table's, so the
   *absolute* buffer counts here are optimistic for a production-shaped
   table.
4. Single-client measurement on a dev machine with a warm cache
   (`shared read` ≈ 0–17 k blocks against ≈ 0.9–2.7 M `shared hit`).

**Cleanup.** `eval/fixtures/bm25-scale/seed-scale-kb.sh --drop` removed
99 825 chunk rows, 2 `bm25_kb_stats_768` rows, 37 539 `bm25_term_stats_768`
rows and the 1 `knowledge_bases` row; the script's own post-drop count query
reported `chunks=0 kb_stats=0 term_stats=0` and `kb_rows=0`, and an
independent `SELECT count(*) FROM document_chunks_768` returned 0 (the table
is empty again, as it was before seeding). The seed writes no other main-DB
rows, so nothing was left to cascade.

## Dispatch-on rerun (Wave 4, 2026-09-06)

- **Task:** Wave-4 Task 6 (`.superpowers/sdd/2026-09-06-rag-sota-wave4/task-6-brief.md`),
  ruling **W4-R8**.
- **Question this closes:** the Wave-3 retune grid above ran with
  `--orchestrator-dispatch=false`, so it never exercised the path Wave-2's
  A/B measured its −7 pp `complex_reasoning` MRR regression on
  (`complex_reasoning` routes through `plan_execute` only when dispatch is
  on). This rerun repeats the ts_rank/bm25 comparison with dispatch on the
  dev-stack default, 3 repeats per cell, to measure that path directly.

### Setup

- **Fixed binary**: `eval-wave4-grid`, built from the clean local `main`
  checkout at commit `270bcf5` (`.superpowers/sdd/2026-09-06-rag-sota-wave4/build-grid-bin.sh`,
  refused to build unless `main`'s HEAD was exactly `270bcf5` and clean) —
  so in-progress Wave-4 branch edits could not leak into the measurement,
  and the retrieval code path is identical to the branch's base.
- **Fixture / args**: same as Wave 2's `ab-A`/`ab-C` and the Wave-3 retune
  grid — `eval/golden/production-ppm-2026-08.jsonl` (89 questions: 43
  lookup, 32 complex_reasoning, 14 enumeration), `--production-context
  --top-k 10 --bm25-tiered-boost off`, plus `--bm25-mode ts_rank|bm25`
  (`--refresh-bm25-stats` before every `bm25` cell) and, for the C5 arm,
  `--rrf-weight-bm25 0.5`. **Dispatch left at its default (on)** — the one
  deliberate difference from the Wave-3 retune grid, and the one that
  matches Wave 2's A/B.
- **Dispatch-path facts** (`t6-grid.log`, checked immediately before the
  run): dev `site_configs` — `chat_plan_execute_enabled=true`,
  `chat_agentic_enabled=true`, `chat_supervisor_enabled=<null>`,
  `chat_drift_enabled`/`chat_longcontext_enabled` absent — so a
  `complex_reasoning` question with dispatch on routes through
  `plan_execute` (never `supervisor`/`drift`/`longcontext`), as in Wave 2.
- **The 15:44 stack-restart incident.** The first grid attempt
  (`t6-grid.sh`, log `t6-grid.log`) started cell A1 at 15:42:29; the dev
  stack restarted mid-run, and A1 exited with a non-zero status after
  121 s wall time. A2 started at 15:44:30 and failed after 15 s for the
  same reason. Both partial/failed outputs were discarded (deleted, never
  analysed) and the grid was relaunched at 15:49:50 with the resumable
  driver `t6-grid-resume.sh` (log `t6-grid-resume.log`, lock
  `t6-grid.lock`), which skips a cell only if its JSON report exists **and**
  its log ends with a `JSON report:` line — i.e. a genuinely completed run.
  All 9 cells then ran to completion with `exit=0`, back to back, finishing
  at 18:57:04 (≈ 20–21.5 min per cell, matching the Wave-2/Wave-3 wall
  times for this fixture). No cell in the analysed set carries any trace of
  the incident.

### Cells

| cell | mode | errors | plan_execute | agent.orchestrator | keyword_mode lines |
|---|---|---|---|---|---|
| A1 | ts_rank | 0 | 29 | {standard: 60, plan_execute: 29} | {ts_rank: 127} |
| A2 | ts_rank | 0 | 29 | {standard: 60, plan_execute: 29} | {ts_rank: 128} |
| A3 | ts_rank | 0 | 28 | {standard: 61, plan_execute: 28} | {ts_rank: 128} |
| C1 | bm25 | 0 | 27 | {standard: 62, plan_execute: 27} | {bm25: 127} |
| C2 | bm25 | 0 | 29 | {standard: 60, plan_execute: 29} | {bm25: 128} |
| C3 | bm25 | 0 | 29 | {standard: 60, plan_execute: 29} | {bm25: 126} |
| C51 | bm25 | 0 | 28 | {standard: 61, plan_execute: 28} | {bm25: 126} |
| C52 | bm25 | 0 | 29 | {standard: 60, plan_execute: 29} | {bm25: 126} |
| C53 | bm25 | 0 | 29 | {standard: 60, plan_execute: 29} | {bm25: 125} |

Every cell: 0 errors, 89/89 questions answered, plan_execute count 27–29
(the 25–30 expected range), and `keyword_mode` lines exclusively the
cell's own mode (no A cell logged `bm25`, no C/C5 cell logged `ts_rank`,
confirming no stats-fallback and no leaked mode) — all 9 cells pass the
per-cell validity rules and none was discarded.

### Per route / metric (mean ± max-spread over 3 repeats, pp)

| route | metric | A (ts_rank) | C (bm25) | C5 (bm25, w=0.5) | band A (pp) |
|---|---|---|---|---|---|
| overall | recall | 0.802 ± 0.1 (3) | 0.827 ± 0.0 (3) | 0.818 ± 2.6 (3) | 1.0 |
| overall | mrr | 0.889 ± 1.0 (3) | 0.874 ± 0.7 (3) | 0.859 ± 1.7 (3) | 1.0 |
| overall | ndcg | 0.894 ± 0.7 (3) | 0.882 ± 0.6 (3) | 0.872 ± 1.9 (3) | 1.0 |
| lookup | recall | 0.865 ± 1.9 (3) | 0.886 ± 0.0 (3) | 0.877 ± 5.1 (3) | 1.9 |
| lookup | mrr | 0.880 ± 2.3 (3) | 0.884 ± 0.0 (3) | 0.872 ± 3.5 (3) | 2.3 |
| lookup | ndcg | 0.882 ± 2.6 (3) | 0.889 ± 0.1 (3) | 0.879 ± 3.8 (3) | 2.6 |
| enumeration | recall | 0.860 ± 0.0 (3) | 0.932 ± 0.0 (3) | 0.896 ± 0.0 (3) | 1.0 |
| enumeration | mrr | 1.000 ± 0.0 (3) | 1.000 ± 0.0 (3) | 0.952 ± 0.0 (3) | 1.0 |
| enumeration | ndcg | 1.000 ± 0.0 (3) | 0.994 ± 0.0 (3) | 0.964 ± 0.0 (3) | 1.0 |
| complex_reasoning | recall | 0.691 ± 2.5 (3) | 0.702 ± 0.0 (3) | 0.705 ± 0.4 (3) | 2.5 |
| complex_reasoning | mrr | 0.854 ± 2.6 (3) | 0.807 ± 2.1 (3) | 0.801 ± 0.0 (3) | 2.6 |
| complex_reasoning | ndcg | 0.864 ± 2.3 (3) | 0.824 ± 1.6 (3) | 0.821 ± 0.1 (3) | 2.3 |

Band = max spread across A1..A3 for that route/metric, floored at 1.0 pp
(W4-R8).

**Lookup / enumeration recall gains** (the direction Wave 2 and the Wave-3
retune grid both found): lookup recall C +2.1 pp vs A (beyond the 1.9 pp
band), C5 +1.2 pp (within band); enumeration recall C +7.1 pp (beyond the
1.0 pp band), C5 +3.6 pp (beyond the 1.0 pp band). `bm25` keeps lifting
recall on the plan-execute path exactly as it did on the standard path —
the cost this rerun surfaces is specific to ranking on
`complex_reasoning`, not to recall anywhere.

### Decision (W4-R8)

`bm25_scoring_mode` flips only if C or C5 beats A's mean lookup MRR beyond
the band AND no route's mean recall/MRR drops beyond its band.

- **C**: lookup MRR +0.4 pp vs band 2.3 pp → **within band** (does not
  clear the first half of the rule). Losses beyond band regardless: overall
  MRR −1.5 pp (band 1.0), complex_reasoning MRR −4.7 pp (band 2.6).
  → **does not win.**
- **C5**: lookup MRR −0.8 pp vs band 2.3 pp → **within band** (the mean
  actually moves the wrong way). Losses beyond band: overall MRR −3.0 pp
  (band 1.0), enumeration MRR −4.8 pp (band 1.0), complex_reasoning MRR
  −5.2 pp (band 2.6). → **does not win.**

**`bm25_scoring_mode` stays `ts_rank`.** Neither bm25 arm clears the
lookup-MRR bar on the plan-execute path — the recall-loss guard isn't even
needed to reach the decision, though both arms would fail it too.

### The Wave-2 claim, settled

On the plan-execute path (dispatch on), complex_reasoning MRR under bm25
is **0.807 ± 2.1 pp (n=3)** vs ts_rank **0.854 ± 2.6 pp (n=3)** — a
**−4.7 pp** difference against a **2.6 pp** band. The Wave-2 −7 pp is
**confirmed in direction** (a real loss beyond the noise band, on the
correct path this time) but **not in full magnitude**: −4.7 pp measured
here over 3 repeats vs −7.0 pp on Wave-2's single run.

### Per-question drivers

Top 5 of 32 shared `complex_reasoning` question ids by |Δ mean reciprocal
rank| between the 3-repeat A-mean and 3-repeat C-mean:

| question id | \|Δ RR\| | A mean RR | C mean RR | question |
|---|---|---|---|---|
| Q058 | 0.889 | 1.000 | 0.111 | Welche KI-Dienste werden auf den Best-Practice-Seiten zu Stanford und Oxford jeweils dokumentiert? |
| Q025 | 0.500 | 0.500 | 0.000 | Welche Projekte im Portfolio 2026 adressieren laut Titel und Zielsetzung das Thema Informationssicherheit direkt? |
| Q023 | 0.444 | 0.444 | 0.000 | Welche KI-bezogenen Projekte aus dem Digital-PPM haben bereits den Status 'entschieden' erreicht? |
| Q056 | 0.333 | 0.667 | 1.000 | Welche Projekte haben höhere geschätzte Projektkosten: 'JLU Future Data Center - Teil 1' oder 'M365-Planung'? |
| Q029 | 0.153 | 0.708 | 0.556 | Welche Projekte aus dem Portfolio befassen sich schwerpunktmäßig mit der Ablösung oder Erneuerung von Microsoft-Technologien? |

Four of the five swing questions move against bm25 (Q058, Q025, Q023,
Q029); one (Q056) moves in its favor. Q058 alone accounts for most of the
route MRR gap — a single question falling from rank 1 to effectively
unranked (RR 0.111 ≈ rank 9) under bm25 in all 3 repeats — consistent with
a ranking effect (the tokeniser-divergence / RRF-scale-mismatch mechanisms
already documented above), not a recall failure, since route recall does
not regress (0.702 vs 0.691, actually +1.1 pp).

### Operating point for opt-in KBs

**No change to the documented operating point** above
(`rrf_weight_bm25 = 0.5`, α unchanged, for a KB opted into `bm25` on the
standard path). This rerun gives no reason to move it: C5 (weight 0.5) is
not better than C (default weight 1.0) on the plan-execute path — it loses
more, not less, beyond the bands (enumeration MRR −4.8 pp vs C's clean
pass, complex_reasoning MRR −5.2 pp vs C's −4.7 pp) — so there is no new,
dispatch-on-specific operating point to recommend; the standard-path
recommendation is unaffected because it was never about the plan-execute
path.

### Artifacts

- `.superpowers/sdd/2026-09-06-rag-sota-wave4/t6-{A1,A2,A3,C1,C2,C3,C51,C52,C53}.json`
  — full `eval.Report` per cell (workspace-only, gitignored).
- `.superpowers/sdd/2026-09-06-rag-sota-wave4/t6-{A1,A2,A3,C1,C2,C3,C51,C52,C53}.log`
  — driver stdout/stderr per cell, including `rag.search.stages`
  `keyword_mode` evidence (workspace-only, gitignored).
- `.superpowers/sdd/2026-09-06-rag-sota-wave4/t6-grid.sh`,
  `t6-grid-resume.sh` — the grid drivers (first attempt / resumable
  relaunch).
- `eval/fixtures/bm25-scale/analyse-dispatch-grid.py` — the analysis
  script (tracked; produces every table and number in this section from
  the 9 JSON/log pairs above; supersedes the controller's draft
  `.superpowers/sdd/2026-09-06-rag-sota-wave4/t6-analyse.py`, whose numbers
  it reproduces exactly on spot-check and extends with the
  `agent.orchestrator` distribution, nDCG, the C5 arm, the recall-gain
  narrative, and the per-question driver table above).
