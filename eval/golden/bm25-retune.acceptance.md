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
  variance estimate. The enumeration route's 0.0 pp band is an artefact of one
  repeat over 14 questions, not evidence of zero variance — treat the rule's
  per-route recall/MRR guard on that route as approximate.
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
