# `global-synthesis-de.jsonl` acceptance record

- **Date:** 2026-09-06
- **Branch / commit:** `feat/rag-sota-wave3`, `5b1b59a`
  (`feat(eval): --longcontext on|off and --golden-query-type run overrides for
  cmd/eval` — the flags the runs below use; the docs commit lands on top of
  this record).
- **Model stack (from `ai_models` + the run logs):** answer generation and all
  three judges `jlu/gemma-4-26b-it` (the KB carries no per-KB
  `chat_model`/`embedding_model`/`rerank_model` override — all three columns
  are NULL); map-stage finding extractor `jlu-internal/gemma-4-26b-it-bulk`
  (`model_tier_fast`, no `chat_longcontext_map_model` override set);
  reranker `jlu/jina-rerank`; embedder `jlu/qwen3-embedding` (4096-dim).
- **Fixture:** `eval/golden/global-synthesis-de.jsonl`, 12 questions, KB
  `83262307-3a1b-49bc-bd08-3b925a868a92` (`PPM-Eval`, 297 files / 1815 chunks
  in `document_chunks_4096`). Design and the two gating conditions:
  `eval/golden/README.md` §"Global-synthesis set (Wave 3 Task 4)".
- **Site config at run time:** unchanged — no `site_configs` row was written
  for this measurement. `chat_longcontext_*` are all unset in the dev DB
  (defaults), `chat_supervisor_enabled` and `chat_drift_enabled` are empty,
  `crag_enabled = true`. `chat_longcontext_enabled` and
  `chat_longcontext_mode` were supplied per run through the new
  `--longcontext` / `--longcontext-mode` overlays (`chatOverlayReader`,
  `go-backend/cmd/eval/main.go`), confirmed in each run's first log line:
  `eval: applying chat site_config overlays for this run
  {"chat_longcontext_enabled":"true","chat_longcontext_mode":"flat"}`.

## Fixture validation

All 12 questions reached `OrchLongContext` in **every** run — 36/36
question-runs, `errors = 0` throughout:

```
{"msg":"eval.orchestrator_dispatch","question_id":"G01",
 "query_type":"complex_reasoning","orchestrator":"longcontext",
 "dispatch_reason":"complex_reasoning_longcontext_gate"}
{"msg":"rag.longcontext.fired","mode":"flat","max_tokens":100000,
 "top_k":200,"chunks":200}
```

No question was ever classified `lookup`/`enumeration`, so no question had to
be rewritten. Note that the `complex_reasoning` half of the gate is an LLM
call and is **not** guaranteed to reproduce; re-check the `agent` column
before trusting any future run of this set.

## Commands run

```bash
# (a) flat — the current default, byte-identical to the pre-orchestrator path
bash .superpowers/sdd/2026-09-06-rag-sota-wave3/t4-run.sh a-flat flat

# (b) map_reduce, retrieval-regression-gated against (a)
bash .superpowers/sdd/2026-09-06-rag-sota-wave3/t4-run.sh b-mapreduce map_reduce \
  --baseline .superpowers/sdd/2026-09-06-rag-sota-wave3/t4-a-flat.json

# (c) flat again — the noise band
bash .superpowers/sdd/2026-09-06-rag-sota-wave3/t4-run.sh c-flat2 flat
```

`t4-run.sh` expands to
`--golden eval/golden/global-synthesis-de.jsonl --production-context
--orchestrator-dispatch=true --judge --longcontext on --longcontext-mode <mode>`.

## Results

### Judge metrics (n=12, k=10)

| Run | answer relevance | faithfulness | context precision | mean answer length | mean context-assembly latency | wall time |
|---|---|---|---|---|---|---|
| (a) flat | **1.000** | 0.613 | 0.409 | 3232 chars | 10.2 s | 563 s |
| (c) flat (repeat) | **1.000** | 0.597 | 0.420 | 3356 chars | 10.3 s | 568 s |
| (b) map_reduce | **1.000** | 0.565 | 0.600 | 3995 chars | 63.4 s | 954 s |

### Retrieval metrics — diagnostics only, per W3-R8

| Run | recall@10 | MRR | nDCG@10 | precision@10 |
|---|---|---|---|---|
| (a) flat | 0.214 | 0.854 | 0.841 | 0.283 |
| (c) flat (repeat) | 0.233 | 0.792 | 0.741 | 0.308 |
| (b) map_reduce | 0.287 | 0.743 | 0.735 | 0.375 |

`FinalChunks` is the same `TruncateChunksToFit(chunks, MaxTokens)` pool in
both modes (`longcontext_consume.go` — only the *ordering* differs: flat
sandwich-orders it, map_reduce keeps score order for grouping), and the eval
adapter re-sorts by score before cutting at k. The retrieval spread above is
therefore ANN/reranker nondeterminism, **not** a mode effect; the 6.2 pp MRR
gap between the two nominally identical flat runs makes that plain.

### Noise band

| Metric | same-flag mean gap \|(c)−(a)\| | mean \|per-question delta\|, same flag | SE of the n=12 mean | (b)−(a) |
|---|---|---|---|---|
| answer relevance | 0.000 | 0.000 | 0.000 | **+0.000** |
| faithfulness | 0.016 | **0.354** | 0.141 | **−0.048** |
| context precision | 0.011 | 0.130 | 0.054 | **+0.191** |

The middle two columns matter more than the first. The two flat runs' *means*
happen to land 1.6 pp apart on faithfulness, but that is cancellation, not
stability: individual questions move by up to a full point between two runs of
the *identical* configuration (G07 0.000 → 1.000, G06 0.857 → 0.083, G01
1.000 → 0.357). The implied standard error of a 12-question faithfulness mean
is ≈ 0.14, so a 1.6 pp same-flag gap is a lucky draw and must not be quoted as
the band.

## Decision

**Recommendation: keep `chat_longcontext_mode = flat` as the default.**
`map_reduce` stays a documented opt-in.

> **SUPERSEDED by §4 (Wave 5, 2026-09-07).** This section's verdict stood
> until the pooled rule W5-R1 was pre-registered and the set grew to 24
> questions; the default is now `map_reduce`. The Wave-3 numbers below are
> unchanged and remain the record of what was measured at the time.

The brief's decision rule — adopt `map_reduce` for the route iff *mean answer
relevance improves beyond the noise band* **and** *mean faithfulness does not
drop beyond it* — fails on both arms:

1. **Answer relevance cannot improve: it is already saturated.** All 12
   questions scored 5/5 in run (a), all 12 again in (c), and 11 of 12 in (b)
   — the twelfth is a judge-parser failure, not a low score; its own reasoning
   text says 5 (see the anomalies below). The
   judge (`prompts.AnswerRelevanceSystemPrompt`) sees only *question +
   answer*, never the context, and grades a Likert 1–5 "does this address the
   question"; a 3000–5000-character structured German synthesis answer scores
   5 essentially by construction. This is a **measurement-instrument
   limitation, not a property of the two modes** — the primary metric has zero
   discriminative power on this route, and no configuration of the map-reduce
   path could have satisfied arm 1 as written.
2. **Faithfulness does not improve.** map_reduce is 4.8 pp *below* flat. That
   delta is only 0.34 × the standard error of the mean, so the honest reading
   is "no detectable difference" rather than "map_reduce is worse" — but the
   rule requires no drop, and there is certainly no gain to trade the cost
   against.

The cost side is unambiguous and was measured, not estimated: map_reduce adds
**25 fast-tier LLM calls per question** (25 groups × 8 chunks over the 200-chunk
pool) and takes **6.2× the context-assembly latency** (63.4 s vs 10.2 s mean
per question; 954 s vs 563 s wall for the set). Flipping the default would put
that on every global-synthesis turn in exchange for no measured answer-quality
gain.

**The one metric that does move** is context precision: 0.409 → 0.600, +19.1 pp
at 3.5 × the standard error — comfortably outside the noise. That is the
findings-based CONTEXT block doing exactly what W3-R6 designed it to do (the
answer LLM is handed extracted claims instead of 200 raw chunk bodies). It is
not part of the decision rule (and W3-R8 explicitly de-scoped it for this
route, because the judge grades one boolean per top-10 item against a 200-chunk
pool), so it is recorded as supporting evidence for keeping map_reduce
available, not as grounds for making it the default.

## Per-question anomalies (reported, not averaged away)

- **Two map-group timeouts on G02** (run b):
  `{"msg":"longcontext.map_group_failed","group":13,"chunks":8,"error":"context
  deadline exceeded"}` and the same for group 23. The W3-R7 fallback did its
  job — those two groups contributed raw first-600-rune findings instead of
  extracted ones, which is why G02 reports 207 findings against a 25-group /
  200-chunk pool where the other questions report 33–156. G02 was also the
  slowest question in the set at 134 s. No question errored and no answer was
  lost; this is the designed degradation, observed live.
- **G07's answer-relevance judge call failed to parse** in run (b):
  `answer_relevance: response is not valid JSON: "{\"score\":\"5\", …"`. The
  model emitted the score as a JSON *string* (`"5"`) instead of a number and
  the judge's strict decoder rejected it. The metric is simply absent for that
  question (the mean is over the remaining 11) — it is not a zero. Worth a
  follow-up: the judge decoder could accept a numeric string, since the same
  model produced a valid object with the right value.
- **G07's map_reduce answer is 1078 characters** against 2600 (a) / 2709 (c) in
  flat — the largest length regression in the set, on the question with the
  *most* findings (156). Consistent with W3-R6's accepted cost: the reduce
  stage can only use what a finding surfaced.
- **G04 scored faithfulness 0.000 in run (b)** with recall 0.000 in the same
  run (0.083 in (a), 0.167 in (c)). Its retrieval simply missed the curated
  cluster that run; the faithfulness score is downstream of that, not an
  independent second failure.
- **A `context_precision` judge warning recurs across runs** — `judge returned
  11 booleans, expected 10` (G02 in (a) and (c), G04 and G07 elsewhere). The
  judge occasionally emits one extra boolean for a 10-item list. Pre-existing,
  independent of this task, and it only drops that one question's context
  precision.

## Artifacts

Under `.superpowers/sdd/2026-09-06-rag-sota-wave3/` (gitignored workspace):
`t4-a-flat.json` / `.log` / `.walltime`, `t4-b-mapreduce.*`,
`t4-c-flat2.*`, plus `t4-analyse.py` (per-question table + deltas, scrapes
`rag.longcontext.map_reduce` group counts out of the logs), `t4-stats.py`
(the noise table above) and `t4-validate-golden.py` (fixture validator:
trigger verbatim-ness, file-name existence against the live KB, row shape).

## §2 Wave 4 re-measurement (2026-09-06)

**Status:** complete. Steps 2–4 (judged flat×2 / map_reduce×2 runs, pairwise
comparison, the W4-R7 decision) ran as dispatch 5b, after the Task 6 BM25
grid finished (`t6-grid-resume.log` ended `== grid done`; `t5b-run.sh`
refuses to start while `t6-grid.lock` names a live pid). Binary built once
from commit `7ebf72f`.

### Setup

Same fixture, KB and site-config baseline as §1 (unchanged `chat_longcontext_*`
defaults; mode supplied per run via `--longcontext`/`--longcontext-mode`
overlays). New in Wave 4: `--judge` now also runs the **pairwise preference
judge** (offline, W4-R4) and the **coverage judge** (W4-R5, driven by the
`expected_points` curated in this file below) in addition to the three
judges from §1.

### Commands (exact, `t5b-run.sh`)

```bash
export DB_HOST=localhost DB_PORT=5432 DB_USER=postgres DB_PASSWORD=postgres DB_NAME=rag_db
export VECTOR_DB_HOST=localhost VECTOR_DB_PORT=5433 VECTOR_DB_USER=postgres VECTOR_DB_PASSWORD=postgres VECTOR_DB_NAME=rag_vector_db
export JWT_SECRET=local-eval-acceptance-secret-0123456789abcdef
export REDIS_HOST=localhost REDIS_PORT=6379 REDIS_PASSWORD=redis
export S3_ENDPOINT=http://localhost:9000 S3_ACCESS_KEY=minioadmin S3_SECRET_KEY=minioadmin S3_BUCKET=rag-files S3_REGION=us-east-1

# four judged runs, alternating flat / map_reduce
eval-wave4 --golden eval/golden/global-synthesis-de.jsonl --production-context \
  --orchestrator-dispatch=true --judge --longcontext on --longcontext-mode flat \
  --output t5-flat1.json        # then mr1 (map_reduce), flat2, mr2, same shape

# three pairwise comparisons (win rate is A's; A is always the first path named)
eval-wave4 --pairwise-a t5-flat1.json --pairwise-b t5-mr1.json   --pairwise-out t5-pw-1.json
eval-wave4 --pairwise-a t5-flat2.json --pairwise-b t5-mr2.json   --pairwise-out t5-pw-2.json
eval-wave4 --pairwise-a t5-flat1.json --pairwise-b t5-flat2.json --pairwise-out t5-pw-ctrl.json
```

Full driver: `.superpowers/sdd/2026-09-06-rag-sota-wave4/t5b-run.sh`. Wall
clock: flat1 575 s, mr1 925 s, flat2 565 s, mr2 951 s (`t5b-run.log`), total
≈ 55 min plus the three pairwise comparisons (fast — no answer generation).

### Run table (n=12, k=10 per run)

All four runs: `errors = 0`, all 12 questions `agent.orchestrator ==
"longcontext"`, `agent.classified_query_type == "complex_reasoning"`,
`judge.coverage` present for all 12 (`coverage_n = 12`).

| Run | mean coverage | mean faithfulness (n) | mean context precision | mean answer relevance | mean answer length (runes) | judge warnings | judge errors | wall time |
|---|---|---|---|---|---|---|---|---|
| flat1 | 0.5625 | 0.5310 (11) | 0.4667 | 1.0000 | 4540¹ | 2 | 1 | 575 s |
| mr1 | 0.6319 | 0.5608 (12) | 0.5583 | 1.0000 | 3940 | 0 | 0 | 925 s |
| flat2 | 0.5458 | 0.6137 (12) | 0.4583 | 0.9167 | 4367 | 4 | 0 | 565 s |
| mr2 | 0.5736 | 0.5424 (12) | 0.5667 | 0.9792 | 4029 | 0 | 0 | 951 s |

¹ flat1's mean answer length is inflated by a single degenerate answer
(G01, 17337 runes, ~15400 of them a runaway underscore repetition before the
generation recovered and produced a correct, complete table — see
"Anomalies" below); excluding G01, flat1's mean is 3377 runes, in line with
the other three runs.

Contrary to the "expect saturation as in Wave 3" prior (§1: answer relevance
5/5 on all-but-one question across three runs), answer relevance is **not**
uniformly saturated this wave — flat2 (11/12 at 1.0, one lower) and mr2
(one question below 1.0) both come in under 1.000. Faithfulness and context
precision were never saturated (consistent with §1) and continue to move
0.46–0.61 across runs — both remain diagnostic, not decision inputs, per
W4-R7 (only coverage and the pairwise judge feed the decision).

**Judge instrument faults, all pre-existing and independent of mode:**
flat1 G08's faithfulness judge call returned a ```` ```json ```` code-fenced
response the strict JSON decoder rejected (`judge_errors`, `faithfulness`
absent for that question, mean is over the other 11); flat1 G02/G07 and
flat2 G02/G07/G08/G11 hit the recurring `context_precision: judge returned
11 booleans, expected 10 — truncated/padded` warning already documented in
§1. mr1 and mr2 carry zero judge warnings/errors.

### Coverage — noise band + per-question table

Noise band = `|mean_coverage(flat1) − mean_coverage(flat2)| = |0.5625 −
0.5458| = 0.0167` (**1.67 pp**).

| Q | flat1 | mr1 | flat2 | mr2 |
|---|---|---|---|---|
| G01 | 1.000 | 1.000 | 0.800 | 0.800 |
| G02 | 0.667 | 0.667 | 0.833 | 0.667 |
| G03 | 0.667 | 0.500 | 0.667 | 0.500 |
| G04 | 0.500 | 0.500 | 0.167 | 0.333 |
| G05 | 0.167 | 0.667 | 0.333 | 0.667 |
| G06 | 0.750 | 0.250 | 0.750 | 0.250 |
| G07 | 0.167 | 0.500 | 0.333 | 0.667 |
| G08 | 0.667 | 0.833 | 0.833 | 0.500 |
| G09 | 0.500 | 0.833 | 0.667 | 0.667 |
| G10 | 0.500 | 0.667 | 0.500 | 0.667 |
| G11 | 0.667 | 0.667 | 0.667 | 0.667 |
| G12 | 0.500 | 0.500 | 0.000 | 0.500 |

Cross-pair coverage deltas (map_reduce − flat, same run pair):
pw1 `mr1 − flat1 = 0.6319 − 0.5625 = +0.0694` (**+6.94 pp**, beyond the 1.67 pp
band); pw2 `mr2 − flat2 = 0.5736 − 0.5458 = +0.0278` (**+2.78 pp**, also
beyond the band). Coverage rises for map_reduce in both cross pairs — the
coverage arm of W4-R7 is satisfied on both pairs.

### Map/reduce trajectory stats (mr1, mr2 logs)

Both runs: 12 questions × 25 groups/question (`chat_longcontext_map_group_size`
default 8, 200-chunk pool ÷ 8 ≈ 25) = 300 groups/run, `dropped_findings = 0`
throughout (no reduce-stage truncation, W3-R7 fallback never needed to spill).

| Run | groups | failed groups | findings | dropped findings |
|---|---|---|---|---|
| mr1 | 300 | 3 | 1068 | 0 |
| mr2 | 300 | 1 | 1095 | 0 |

The 4 failed groups (all `longcontext.map_group_failed`, `error: "context
deadline exceeded"`) landed on G02 in both runs (3 in mr1: groups 1, 20, 22;
1 in mr2: group 2) — the W3-R7 fallback (raw first-600-rune chunk text
instead of an extracted finding) covered them; no question errored.

### Pairwise comparisons (W4-R4; winner is from A's perspective)

| Pair | A | B | wins(A) | ties | losses(A)=wins(B) | decisive | A win rate | A Wilson [lo,hi] | B (map_reduce) win rate | B Wilson [lo,hi] | tie rate | skipped/errors |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| pw1 | flat1 | mr1 | 3 | 1 | 8 | 11 | 0.2727 | [0.097, 0.566] | **0.7273** | **[0.434, 0.902]** | 0.083 | 0/0 |
| pw2 | flat2 | mr2 | 1 | 3 | 8 | 9 | 0.1111 | [0.020, 0.435] | **0.8889** | **[0.565, 0.980]** | 0.250 | 0/0 |
| ctrl | flat1 | flat2 | 3 | 6 | 2 | 5 | 0.6000 | [0.231, 0.882] | 0.4000 | [0.118, 0.769] | 0.545 | 1/1 |

(B's Wilson interval is the exact complement of A's, `[1−hi(A), 1−lo(A)]`,
confirmed by direct Wilson computation in `t5b-analyse.py` §5.)

**Control pair (flat1 vs flat2, self-pair):** win rate 0.60 with a wide
Wilson interval `[0.231, 0.882]` that comfortably spans 0.5, and by far the
highest tie rate of the three pairs (0.545 — 6 of 11 judged pairs). Both are
the expected control signature: a same-configuration comparison should be
indistinguishable, and a judge that ties more than half the time on
identical-quality answers is telling you its discriminative power is modest
at this margin, not that flat1 systematically beats flat2. **One pair was
skipped and counted as an error**: G05's `(B,A)`-order judge call returned a
```` ```json ```` code-fenced response the strict decoder rejected
(`"note":"judge (B,A) failed: response is not valid JSON: ...`); the pair
contributes to neither wins/ties/losses nor the win rate denominator.

### Per-question verdict table

| Q | pw1 (flat1/mr1) | pw2 (flat2/mr2) | ctrl (flat1/flat2) |
|---|---|---|---|
| G01 | B | B | tie |
| G02 | B | B | B |
| G03 | B | B | tie |
| G04 | A | tie | A |
| G05 | B | tie | skipped |
| G06 | A | tie | tie |
| G07 | A | A | B |
| G08 | tie | B | tie |
| G09 | B | B | tie |
| G10 | B | B | tie |
| G11 | B | B | A |
| G12 | B | B | A |

### Three disagreement excerpts (both orders' reasoning, `agree_both_orders = false`)

**pw1 G08** (winner recorded as `tie`):
> **A-then-B order:** "Antwort A ist etwas besser strukturiert, da sie am
> Ende eine sehr hilfreiche Vergleichstabelle bietet... die Tabelle in A
> erhöht die Konkretisierung und den Vergleichswert der Antwort deutlich."
>
> **B-then-A order:** "Antwort A ist umfassender und detaillierter. Sie
> nennt deutlich mehr konkrete Beispiele für Hochschulen... Antwort B ist
> zwar gut strukturiert, lässt aber viele der in A genannten Informationen
> aus."

Both orders pick "the answer shown first" as the winner (a table vs. more
named examples) — a textbook position-bias flip, exactly what the swap-and-
require-agreement design (W4-R4) exists to neutralise into a tie rather than
a false win.

**pw2 G04** (winner recorded as `tie`):
> **A-then-B order:** "Antwort A bietet ein besseres 'Gesamtbild', da sie
> die strategische Verbindung zwischen der physischen Infrastruktur... als
> logische Kette beschreibt."
>
> **B-then-A order:** "Antwort A ist besser, da sie die zeitlichen und
> technischen Abhängigkeiten präziser und konkreter benennt... Antwort B
> bleibt bei den Abhängigkeiten eher auf einer sehr allgemeinen,
> konzeptionellen Ebene."

Same signature as G08 — both orders prefer the first-shown answer, this time
on a genuinely close call (structural coherence vs. concrete dependency
detail), correctly resolved to a tie rather than credited to either mode.

**ctrl G01** (winner recorded as `tie`, between the two flat runs):
> **A-then-B order:** "Antwort A ist vollständiger, da sie zusätzliche
> relevante Projekte (Windows 10-Ablösung, AD-Domaincontroller) enthält,
> die in Antwort B fehlen. Beide Antworten sind inhaltlich korrekt und gut
> strukturiert."
>
> **B-then-A order:** "Antwort A ist deutlich besser, da Antwort B mit
> einer extrem langen Zeichenfolge aus Unterstrichen beginnt, was die
> Lesbarkeit massiv stört. Zudem ist die Tabelle in Antwort A korrekter
> strukturiert..."

This is the flat1 G01 degenerate-answer anomaly (see below) surfacing in the
judge's own reasoning — one order weighs plain completeness and calls it
close, the other explicitly flags the ~15400-underscore garbage run as "massiv
die Lesbarkeit störend" and prefers the other answer outright. The swap
still resolves to a tie (the orders disagree on the *winner*, not merely the
margin), which is the conservative, correct outcome for a pair with a
readability defect on one side.

### Pooled cross-pair statistics (post-hoc supporting evidence — **not** the W4-R7 criterion)

Pooling pw1's and pw2's decisive pairs (excludes both pairs' ties and the
control pair entirely): 16 of 20 decisive pairs went to map_reduce —
**0.800**, Wilson interval **[0.584, 0.919]** — comfortably clears both the
0.60 rate and the >0.50 lower-bound thresholds. Pooled coverage delta
(mean(mr1, mr2) − mean(flat1, flat2)) = **+4.86 pp**, also beyond the 1.67 pp
noise band. This pooled view is reported because it is the more
statistically efficient read of the same two measurements, but it was **not
pre-registered** — W4-R7 specifies the rule per pair, and relaxing to the
pooled statistic after seeing pw1 miss would be exactly the kind of
post-hoc rule-softening the pre-registration is meant to prevent. It is
recorded as directional evidence for a future re-registration (see
"Recommendation" below), not as grounds for flipping the default now.

### Power note: the per-pair Wilson criterion is under-powered at this set size

Minimum wins needed, at a given number of decisive pairs, for the Wilson
lower bound to clear 0.50 (z = 1.96):

| decisive pairs (n) | wins needed | resulting Wilson low |
|---|---|---|
| 9 | 8 | 0.565 |
| 10 | 9 | 0.596 |
| 11 | 9 | 0.523 |
| 12 | 10 | 0.552 |

With only 12 golden questions per run (and 1–3 of them tied away by the
swap-and-agree design each time), a pair typically has 9–11 decisive
comparisons — and at that range the Wilson-low>0.50 bar requires missing at
most one or two losses out of the total. pw2 (9 decisive) cleared it with
8/9; **pw1 (11 decisive) needed 9/11 and landed on exactly 8/11 — one win
short.** The pre-registered per-pair rule is therefore under-powered at
n=12 questions: a single additional map_reduce loss (or a single additional
tie resolving the other way) in either direction would flip the outcome of
either pair. This is a property of the sample size, not evidence that the
true effect is near the boundary — the pooled point estimate (0.800) sits
well clear of it.

**Recommendation (roadmap item, not executed this wave):** extend the
global-synthesis golden set to 24–36 questions before the next flat vs.
map_reduce measurement, or explicitly re-register the decision rule on the
**pooled** cross-pair statistic (rather than requiring both pairs
individually) before that run — either change would let this comparison
resolve at the confidence level W4-R7 intends instead of being decided by
a one-pair margin.

### Decision (W4-R7)

Rule: map_reduce becomes the `chat_longcontext_mode` route default only if
the **map_reduce** win rate is ≥ 0.60 **with Wilson lower bound > 0.50 on
BOTH cross pairs**, and mean coverage does not drop below flat's by more
than the flat-vs-flat noise band. ("Win" here is map_reduce's win rate,
i.e. **losses from flat's (A's) perspective** — pw1/pw2 above report both
directions explicitly to avoid ambiguity.) Cost is reported, does not veto.

| Pair | map_reduce win rate | ≥ 0.60? | Wilson low | > 0.50? | coverage delta | within noise band (no drop)? | Pair verdict |
|---|---|---|---|---|---|---|---|
| pw1 (flat1 vs mr1) | 0.7273 | yes | **0.434** | **no** | +6.94 pp | yes | **FAILS** |
| pw2 (flat2 vs mr2) | 0.8889 | yes | 0.565 | yes | +2.78 pp | yes | PASSES |

**pw1's Wilson lower bound (0.434) is below the required 0.50 threshold.**
Applying W4-R7 exactly as pre-registered — both pairs must pass, no
relaxation after seeing the data — the rule **FAILS overall on pw1 alone**,
regardless of pw2 passing and regardless of the pooled/directional evidence
above.

**`chat_longcontext_mode` stays `flat`.** map_reduce remains an opt-in mode
(`--longcontext-mode map_reduce` / the site_config, gated by
`chat_longcontext_enabled` as before); Task 9 does **not** flip the default
this wave.

> **SUPERSEDED by §4 (Wave 5, 2026-09-07).** The Wave-4 verdict was
> "measured favourably, under-powered set, per-pair rule missed by one
> win". Wave 5 executed both remedies this section named — re-register
> on the pooled statistic first, then grow the set — and the default
> flipped to `map_reduce`. The Wave-4 numbers below are unchanged.

**Cost, reported per W4-R7 (does not veto):** map_reduce's mean wall time
(938 s = mean of 925 s, 951 s) is **1.65×** flat's (570 s = mean of 575 s,
565 s), driven by 25 fast-tier map-stage LLM calls per question on top of
the reduce/answer call. Even had both pairs passed, this is the standing
cost of switching the default.

### Anomalies (reported, not averaged away)

- **flat1 G01: a degenerate answer that self-corrected.** 17337 runes total,
  of which ~15400 are a single unbroken run of `_` characters starting
  immediately after "Bas", followed by a complete, well-formed, correctly
  sourced comparison table. Neither faithfulness (0.5) nor coverage (1.0)
  penalised it much (the judges evidently look past the garbage run to the
  substantive tail), but it visibly influenced the **pairwise** judge — see
  the ctrl G01 disagreement excerpt above, where one ordering explicitly
  cites the underscore run as a readability defect. A generation-layer bug
  (runaway token repetition), independent of long-context mode — flat2's
  G01 answer (1708 runes) is unaffected — and out of scope for this task;
  worth a follow-up ticket against the answer LLM/streaming path.
- **One faithfulness judge parse failure, isolated to flat1 G08**:
  `faithfulness: response is not valid JSON: "```json\n{...` — the model
  wrapped its structured-output JSON in a markdown code fence, which the
  strict decoder rejects. Pre-existing failure mode (also seen in the
  control pair's G05 pairwise call), independent of longcontext mode.
- **The recurring `context_precision: judge returned 11 booleans, expected
  10` warning** (documented in §1) recurred on 6 of the 48 question-runs
  this wave (flat1 G02/G07; flat2 G02/G07/G08/G11) — zero occurrences in
  either map_reduce run. Not investigated further here; same pre-existing
  boolean-count judge quirk as §1.

### Artifacts

Under `.superpowers/sdd/2026-09-06-rag-sota-wave4/` (gitignored workspace):
`t5-flat1.json`/`.log`, `t5-mr1.json`/`.log`, `t5-flat2.json`/`.log`,
`t5-mr2.json`/`.log` (the four judged runs), `t5-pw-1.json`/`.log`,
`t5-pw-2.json`/`.log`, `t5-pw-ctrl.json`/`.log` (the three pairwise
comparisons), `t5b-run.sh`/`t5b-run.log` (the driver + wall-time log),
`t5b-summary.py` (controller's quick summary) and `t5b-analyse.py` (this
record's source of truth — every number above is reproducible by running
`python3 t5b-analyse.py` from that directory; its output is also saved at
`t5b-analyse-output.txt`).

### `expected_points` curation (Task 5 / W4-R5) — see "Fix round 1" below for corrections

Added to all 12 rows of `eval/golden/global-synthesis-de.jsonl` (gitignored;
the file itself is not committed — this table is the reviewable record).
Points were authored **only** from the cited source documents' chunk text
(`document_chunks_4096` in the dev vector DB, `justrag-vectordb-1`), read via
read-only `psql` against `justrag-db-1` (file-id resolution by name) and
`justrag-vectordb-1` (chunk content), never from a model answer. **67 points
total across 12 questions** (4–6 per question, all within the loader's 2–6
range; longest point 305→trimmed to ≤300 runes — see Fix round 1). The
original first pass (45 points, 3–5 per question) is superseded by the
corrected/broadened set below; this table lists the corrected state only.

| Q | Points | Source files consulted (name, as in `must_cite_file_names` unless noted) |
|---|---|---|
| G01 | 5 | SAP - Migration auf S4HANA - Go4S4.md; Windows 10-Ablösung.md; Außerbetriebnahme altes IMAP-E-Mail-System (Dovecot).md; Migration KEMP-Loadbalancer zu VMware AVI.md (incl. the E-Mail/ESA exclusion line); Folio Einführung des cloudbasierten Bibliothekssystems und Ablösung LBS.md |
| G02 | 6 | Planungsprojekt Neue Wege mit KI.md; EP Vermerk Neue Wege mit KI.md; Projektablaufplan Neue Wege mit KI.md; Projektabschlussbericht Neue Wege mit KI.md; Projektkonzeption Themengebiet KI an der JLU.md; Smarte Administration mit KI.md; KI-HUB für innovative Forschung.md; KI-Infrastruktur und KI-Plattformen Konzeption zentral abgestimmter Planung, Beschaffung und Auslast.md; Weiterentwicklung der zentralen JLU-KI-Services (HAWKI, HRZ-API-Service und weitere Schnittstellen; .md; Roadmap KI-Services JLU 2026 mit Ausblick 2027.md; Support-Chatbot mit Websearch, RAG und Website-Widget Bereitstellung des Systems.md; JLU KI Wissensdatenbanken Vorprojekt.md. Not consulted for a point (near-empty/directory chunks): Website KI an der JLU.md; Kerngeschäftsfähigkeiten mit KI.md |
| G03 | 6 | Datenträgerverschlüsselung.md; MFA für Admins Multifaktorauthentifizierung für IT-Admins.md; LAPS-Upgrade.md; Zentraler Log-Server.md; Microsoft-Unternehmenszugriffsmodell Sicherere Administration von Active Directory und M365.md; Aktualisierung Sicherheitskonzept HRZ.md; Informationssicherheits-Richtlinien Überarbeitung und Visualisierung (PoliciesVis).md; Informationssicherheit Policies & Visualisations 2026 (PoliciesVis 2026).md; Informationssicherheits-Audits 2026.md; Aktualisierung der Wiederanlaufpläne inkl. Disaster Recovery.md; Informationssicherheit - wann ist der ISB zu beteiligen.md. Not consulted (left out): Ausbau IT-Sicherheit.md; Passwortverwaltung in der zentralen IT Evaluation.md |
| G04 | 6 | JLU Future Data Center - Teil 1.md; JLU Future Data Center - Teil 2 (Plan und Bedarfsanmeldung Bau).md; Migration Datacenter Netzwerk Optimierung Firewall, Router und Switche.md; Austausch USV 1 & 2 Erneuerung unterbrechungsfreie Stromversorgung Serverräume 1 & 2.md; Erneuerung PDUs (Power Distribution UnitsStromverteilereinheiten Serverräume HRZ).md; Erneuerung Netzwerk-Standortverteiler Phil II.md; Gebäudeanbindung Unizentrum (Ertüchtigung und Modernisierung Glasfaser- und Kupfernetz).md; Kanalsanierung LWL  Kabel (Teilstrecke Glasfaserring).md; Erkundung LWL.md; Machbarkeitsstudie eines Herstellerwechsels im Bereich WLAN.md; WLAN LFE WLAN-Versorgung landwirtschaftlicher Lehr- und Forschungseinrichtungen.md; Umzug TK-Standort VetMed.md |
| G05 | 6 | CaMS  zentralfinanziertes Anschlussvorhaben (zfAV) (für HISinOne) zum Nationalen Once-Only-Technica.md; CaMS  Teilprojekt MVV - Einführung eines Modul- und Veranstaltungsverzeichnisses.md; CaMS  Projekt IDANOOTS+DSC.md; CaMS  Alumni Service - Alumni-Management Einführung von HIS-ALU.md; CaMS  Teilprojekt Langfristige Strategie für das Campusmanagement.md; Stud.IP-Raumverwaltung anpassen.md; Eventmanagement mit Stud.IP.md; Auswertung von Lehrraumbelegungen in Stud.IP.md; Stud.IP-Update.md; ILIAS-Update.md; **ILIAS-Update (V10).md**; ILIAS - Stud.IP Schnittstelle.md; HISinOne MoveON Schnittstelle.md; Weiterentwicklung MoveON.md; European Student Card Initiative (ESCI).md — all 15 must-cite files now represented (the 5-file CaMS cluster, previously absent, is point 1) |
| G06 | 4 | Workshop Agenda und Inhalte.md (comment thread); Raumübersicht Workshop.md; Infos für Human Digitals Orga Workshop am 25.11.2025.md; Save the Date und E-Mailverteiler für Einladung.md; Orga Verwaltungsworkshop 15.04.2026.md; 2025-11-14 Update Workshopplanung.md; Checkliste & Ablaufplan WS.md |
| G07 | 6 | Zusammenfassung Ergebnisse Workshop.md; Thementisch 2 Kulturwandel & Qualifikationsbedarfe für KI an der JLU.md; Thementisch 8 Kulturwandel & Qualifikationsbedarfe für KI an der JLU.md; Thementisch 4 Ethik & Gesellschaft.md; Thementisch 7 Ethik & Gesellschaft.md; Thementisch 1 Implementierung Konkret. Bereits begonnen.md; Thementisch 3 Abläufe und Strukturen.md; Thementisch 5 Technische Voraussetzungen.md; Thementisch 6 Ökosystem & Partnerschaften.md. Not independently cited (support/transcription docs): Thementische Implementierung und Abläufe & Strukturen.md; Transkription aus dem Workshop Übersicht.md; Transkription aus dem Workshop techn. Voraussetzungen, Ökosystem & Partnerschaften, Ethik und Gesel.md; Workshop-Bericht.md |
| G08 | 6 | RWTH Aachen.md; Universität Hamburg.md; Universität Heidelberg.md; **TU München.md**; Stanford University.md (corrected — see Fix round 1); ETH Zürich.md; UC Berkeley.md; University of Oxford.md; Uni Bonn.md; HU Berlin.md; Universität Tübingen.md; Universität Marburg.md; FU Berlin.md — 13 of 14 must-cite files now represented; Sammlung Best Practices KI und Hochschulen.md is a Confluence dashboard macro with no institution content of its own and is not cited |
| G09 | 6 | Moderne AuthN-Infrastruktur Sicherere und zeitgemäße Authentifizierung von Nutzenden.md; Moderne AuthN-Infrastruktur Aufbau einer Produktivumgebung.md; Umstellung der Authentifizierung von LDAP auf Shibboleth für Stud.IP und ILIAS.md; Erneuerung OpenLDAP-Server Infrastruktur der zentralen Authentifizierung.md; Erneuerung AD-Domaincontroller (u.a. Authentifizierungs-Server).md; AD-Kompartmentkonzept.md; IAM Erweiterungen Compliance, Prozessoptimierung und Self Services.md; Implementierung Account-Lifecycle-Prozesse für UKGM-administrierte Landesbedienstete.md; Implementierung Beschäftigten-Lifecycle-Prozesse für die Servicestelle Arbeitsmedizin.md; Benutzeranlage und -pflege in SAP durch IAM.md (title-level); MFA für Admins Multifaktorauthentifizierung für IT-Admins.md; Schnittstelle CAFM-IAM.md — all 12 must-cite files now represented |
| G10 | 6 | Digitalisierung der Aktenführung in der Rechtsabteilung (B1) - Einführung der Kanzleisoftware AnNo.md; Digitales Anforderungsformular Beschaffung FB11.md; Einführung der elektronischen Ausgangsrechnung.md; Einführung ESS (Employee Self Service) in SAP-HCM.md; Vorprojekt Einführung Workflowmanagementsystem Formcycle.md; Pilotierung Workflowmanagement FormCycle.md; Elektronische Signatur an der JLU - Einführung und Pilotierung.md; Elektronische Signatur an der JLU - Pilotprozesse und Rollout Zentralverwaltung.md; DMS  Aktenplanstruktur für das DMS.md; DMS  Elektronische Studierendenakte.md; DMS  Einführung digitales Vertragsmanagement.md; DMS  Einführung einer digitalen dokumentengestützten Vorgangsbearbeitung im hessischen Verbund.md (title/id-level only); Projekt Onlinezugangsgesetz.md (title-level only) — all 13 must-cite files now represented |
| G11 | 6 | Prozess der DigITal-Projektentwicklung Welche Status durchläuft ein Projekt.md (corrected to 6 statuses incl. 60-pausiert — see Fix round 1); Rollendefinition Projektleitung und Projektkoordination.md; Priorisierungs-Methodik im Digital-PPM.md; Must-Have vs. Entscheidbar Einordnung von Projekten.md; Dashboard JLU-Digitalprojekt-Portfoliomanagement.md; Umgang mit Projektsteckbriefen und Informationen im Digitalprojekt-Portfolio.md. Not consulted (near-empty/index pages): Kurzanleitung Erstellung Projektsteckbrief.md; Muster-Steckbrief mit Links zu Anleitungsartikeln.md; Steckbriefe JLU-Digitalprojekte.md; Steckbriefe HRZ.md; Projektportfolio - Steckbriefe.md; Präsidiumsentscheidungen.md (explicitly says its content doesn't exist yet) |
| G12 | 4 | Client-Management-Rollout Softwaremanagement durch Baramundi.md; Windows 10-Ablösung.md; M365-Planung.md; Software Asset Management.md (corrected — see Fix round 1) |

Full point text is not reproduced here since the golden set stays gitignored
and untracked; this table exists so the curation choices (which facts, from
which files) are reviewable without the jsonl. The full text lives in
`eval/golden/global-synthesis-de.jsonl` locally.

**Points deliberately left out** (no reliably verifiable fact found in the
skimmed chunk text within task scope, or the field was template boilerplate
without a filled-in value): exact `Projektende` dates for several rows in the
G01/G04 clusters (the Confluence "Steckbrief" template's `Projektende`
field is frequently edited via changelog entries rather than a stable final
value — e.g. Windows 10-Ablösung's own changelog revises its end date five
times); named `Projektleitung` persons; G03's Ausbau IT-Sicherheit.md and
Passwortverwaltung in der zentralen IT Evaluation.md (their content was not
independently distinguishing enough to add a further point within the
6-point cap once the other 11 files were merged into 6 points); G07's four
transcription/support documents (already represented via the summary point);
G08's Sammlung Best Practices KI und Hochschulen.md (a Confluence dashboard
macro, not an institution profile); G11's index/near-empty pages (Kurzanleitung,
Muster-Steckbrief, three Steckbriefe-listing pages, Präsidiumsentscheidungen).

### Loader validation smoke (Step 1)

```bash
bash .superpowers/sdd/2026-09-06-rag-sota-wave4/run-eval.sh \
  --golden eval/golden/global-synthesis-de.jsonl --question-id G01 \
  --production-context --longcontext on --longcontext-mode flat \
  --output .superpowers/sdd/2026-09-06-rag-sota-wave4/t5a-smoke.json
```

Ran once, single question, no `--judge` (retrieval + answer generation only —
permitted alongside the Task 6 grid per the Task 5 hand-off, since it is one
short run rather than a repeated/long measurement). Result: `errors = 0`,
`eval.orchestrator_dispatch` shows `query_type=complex_reasoning`,
`orchestrator=longcontext`; `rag.longcontext.fired mode=flat`; report written
to `t5a-smoke.json` with `mean_recall=0.500 mrr=1.000 mean_ndcg=0.984`
(retrieval-only numbers, expected to be noisy at n=1 and not the point of
this smoke — the point is that `ParseGoldenSetContent`/the loader accepted
every row, G01's `expected_points` included, without a validation error).
Exit code 0 (`eval-exit=0`). No `--judge`, so the coverage judge itself did
not run in this smoke; that is Step 2 in dispatch 5b.

### Fix round 1 (curation review corrections)

A curation review verified four questions against the chunk text directly
and found four defects, all fixed in place (base commit `24bd17d`):

1. **G08 point 3 misattributed content.** The original point attributed
   "Microsoft Copilot und Grammarly sowie ein Chatbot zu Lehrveranstaltungen"
   to Stanford. Re-reading both files' chunk text: that content is verbatim
   in `TU München.md` (the "TUMtutor" course chatbot, fed by lecturers,
   answers with sources, plus Copilot/Grammarly). `Stanford University.md`'s
   only chunk mentions "AI Playground", "Responsible AI" (a UIT governance
   page) and "AI News" — no chatbot. Fixed: split into two correct,
   contrasting points (TU München's TUMtutor+Copilot/Grammarly vs. Stanford's
   AI Playground/Responsible AI — point 2 in the corrected G08 set), and
   `TU München.md` is now in the source table.
2. **G12 point 4 read as a live initiative.** "Software Asset Management.md"
   was cited for a still-active SAM rollout. Its own changelog shows Status
   changed to "Red50-abgebrochen" on 13.07.2026 with the note "Umsetzung über
   organisatorische Alternativlösungen" (per ALB decision). Fixed: the point
   now states the abandonment and the organisational-alternative outcome
   instead of an ongoing rollout.
3. **G01 point 4 overclaimed "vollständig".** "Migration KEMP-Loadbalancer zu
   VMware AVI.md" states the goal is full retirement of the central KEMP
   instance by end-2027, but explicitly excludes E-Mail/ESA: "Eine Ablösung
   des KEMP-LB für E-Mail/ESA ist derzeit nicht geplant." Fixed: dropped
   "vollständig" and added the exclusion clause.
4. **Cluster-coverage rule (controller ruling, new).** For every question
   phrased "alle …", `expected_points` must cover every major cluster in
   `must_cite_file_names` (one point per cluster, up to the 6-point cap,
   small files merged into a shared point). G05 (15 files: the 5-file CaMS
   sub-cluster was entirely absent) and G08 (14 institution files, only 4
   represented) violated it outright. Re-checked G02, G03, G04, G06, G07,
   G09, G10, G11 against the same rule and broadened all of them except G06
   (already both-events-covered at 3 points; added a 4th, a genuinely new
   fact — the ≥110-registration waitlist — rather than padding). G01 and G12
   were left at their original point counts (not in the coordinator's
   re-check list; only their flagged factual errors were fixed). Net effect:
   total points went from 45 to 67; every "alle …" row except G01/G12 grew
   from 3–5 points to 6 (the cap), and files-represented-per-row rose
   sharply (e.g. G05 5→15 of 15 files, G09 4→12 of 12 files, G04 4→12 of 12
   files) — see the corrected source-file table above.

While re-verifying G11 for cluster coverage, a fifth, incidental error
surfaced and was fixed too: the original point 1 listed five project
statuses (10/20/30/40/50); `Prozess der DigITal-Projektentwicklung...md`'s
own chunk text lists **six** — `60-pausiert` ("Projekt vorübergehend
unterbrochen") was missing. Corrected in place.

**Re-validation (loader):** a throwaway Go program
(`go-backend/cmd/evalcheck-tmp5a/main.go`, created, run, and deleted — never
committed) called `eval.LoadGoldenSet` directly on the corrected file. This
needs neither a DB connection nor the LLM backend, so it ran regardless of
the Task 6 grid's state:

```
OK: loaded 12 questions
  G01: expected_points=5   G02: expected_points=6   G03: expected_points=6
  G04: expected_points=6   G05: expected_points=6   G06: expected_points=4
  G07: expected_points=6   G08: expected_points=6   G09: expected_points=6
  G10: expected_points=6   G11: expected_points=6   G12: expected_points=4
```

**Re-validation (smoke):** `t6-grid.lock` still named a live pid at fix time
(cell `C2` running per `t6-grid-resume.log`), so per the coordinator's rule
one additional single-question, no-`--judge` smoke was run on a question
this round actually changed:

```bash
bash .superpowers/sdd/2026-09-06-rag-sota-wave4/run-eval.sh \
  --golden eval/golden/global-synthesis-de.jsonl --question-id G08 \
  --production-context --longcontext on --longcontext-mode flat \
  --output .superpowers/sdd/2026-09-06-rag-sota-wave4/t5a-fix1-smoke.json
```

Result: `errors = 0`, `orchestrator=longcontext`, `eval-exit=0`
(`mean_recall=0.214 mrr=0.250 mean_ndcg=0.540` — retrieval-only, noisy at
n=1, not the point of the smoke). The corrected file's `expected_points`
(G08 now 6 points) loaded without a validation error.

The corrected `eval/golden/global-synthesis-de.jsonl` was copied over the
main checkout's copy (`/home/steffen/git/JustRAG/eval/golden/global-synthesis-de.jsonl`,
also gitignored) so both working copies hold the same fixed fixture.

## §3 Wave 5 curation (G13–G24) — 2026-09-07

Ruling **W5-R2**: extend the set from 12 to ≥ 24 `global_synthesis` questions
so the long-context decision can be taken on the *pooled* decisive pairs of
two cross comparisons (ruling **W5-R1**, re-registered in
`eval/golden/README.md` § "Pooling two comparisons — `--pairwise-pool`"
*before* this set was authored and before any run on it).

**Result: 12 new rows, G13–G24, 72 `expected_points`** (6 per question), on
KB `83262307-3a1b-49bc-bd08-3b925a868a92` (PPM-Eval, 297 files). G01–G12 are
**byte-identical** — the rows were appended, and the Wave-5 header comment was
inserted *between* G12 and G13, so the first 27 504 bytes of the file compare
equal to the pre-Wave-5 copy (`cmp` verified).

### Method

Same discipline as Wave 4, and the same hard rule: **no point comes from a
model answer.** `t4-*.json` / `t5-*.json` reports were not opened during
authoring. Every point was read out of the ingested chunk text:

- file ids by name from `justrag-db-1` (`files`, read-only `SELECT`),
- chunk bodies from `justrag-vectordb-1`
  (`document_chunks_4096`, `node_kind='leaf'`, ordered by
  `metadata->>'chunkIndex'`),
- reassembled per file with the chunk overlap removed, then the informative
  windows of the Confluence "Steckbrief" template (`Projektziele`,
  `Projektumfang (Scope)`, `Status`, `## Änderungshistorie`) printed and the
  template boilerplate stripped.

Helper scripts (read-only, no DB writes):
`.superpowers/sdd/2026-09-06-rag-sota-wave5/t9-files.sh`,
`t9-chunks.sh`, `t9-brief.py`, `t9-collect.sh`, `t9-status.py`,
`t9-append-rows.py` (the authored rows themselves — re-runnable, refuses to
duplicate an id), `t9-validate.py`, `t9-classify-check.sh`.

Each question carries **exactly one** documented German global-synthesis
trigger verbatim (`globalSynthesisTriggersDE`, `internal/chat/longcontext.go`)
and is multi-clause so the query-type classifier lands on
`complex_reasoning` — both are the precondition for reaching `OrchLongContext`.
Trigger distribution across the new rows: `fasse alle` ×3 (G13, G17, G22),
`überblick über alle` ×3 (G14, G18, G24), `vergleiche alle` ×3 (G15, G19, G20),
`gesamtbild` ×2 (G16, G21), `gemeinsame themen` ×1 (G23).

Topics are new relative to G01–G12. Individual files do recur (a corpus of
297 files has no 24 disjoint clusters), but no question repeats another's
*subject*: G20 is defined by a status value rather than a theme, G21 is KI
*governance* as against G02's inventory of KI projects and G11's PPM
governance, G24 is the workshop's *channels and audiences* as against G06's
*contradictions* in the same orga documents.

**Four rows carry a smaller must-cite cluster than the 12–15 of G01–G12**
(G17 and G18: 8, G19: 8, G20: 9). That is the corpus, not a shortcut: the
remaining Studium/Lehre files are already G05's, and exactly seven Steckbriefe
in the whole KB carry status `60-pausiert` plus one `50-abgebrochen`, which is
the entire population G20 asks about. Each row's `notes` field says so.

### Validation

1. **Schema / caps** (`t9-validate.py`, all 24 rows): one documented DE
   trigger present verbatim per question; every `must_cite_file_names` entry
   exists verbatim in the KB's `files.name` (this caught
   `KI an der JLU bündeln auf Webseite.md`, whose real name carries a
   **NO-BREAK SPACE** (U+00A0) between `auf` and `Webseite`); ≤ 6 points per
   row; ≤ 300 runes per point (longest new point: 299); no duplicate file
   names. `FAILURES: 0`.
2. **Loader + pure preconditions** (`t9-classify-check.sh`, throwaway
   `cmd/t9check`, removed after the run): `eval.LoadGoldenSet` accepts all
   **24** questions; `ai.HeuristicComplexity` returns `ComplexityComplex` (12
   rows) or `ComplexityUnknown` (12 rows, deferred to the LLM classifier) and
   **never** `ComplexitySimple` for any row; `chat.IsGlobalSynthesisQuery` is
   `true` for all 24. `FAILURES: 0`.
   Note in passing: `eval.ParseGoldenSetContent` (the DB/admin eval path)
   rejects this file at the very first byte — it does not skip `#` comment
   lines, which only `internal/eval/loader.go`'s JSONL path does. That is
   pre-existing (the file has carried a comment header since Wave 3) and is
   another reason this set is `cmd/eval`-only.
3. **One eval smoke** (the only run in this task, single question, no
   `--judge`):

```bash
bash .superpowers/sdd/2026-09-06-rag-sota-wave5/run-eval.sh \
  --golden eval/golden/global-synthesis-de.jsonl --question-id G13 \
  --production-context --longcontext on --longcontext-mode flat \
  --output .superpowers/sdd/2026-09-06-rag-sota-wave5/t9-smoke.json
```

Result: `errors = 0`, `eval-exit=0`, wall time 10.2 s, and the report's
`agent` block reads
`{"orchestrator": "longcontext", "classified_query_type": "complex_reasoning",
"dispatch_reason": "complex_reasoning_longcontext_gate"}` — the new row
reaches the intended orchestrator. Retrieval metrics are not reported for
this route and are not the point of the smoke.

### Per-question curation record

Column *Beleg* quotes the chunk fragment the point was verified against; the
file named is the one the fragment came from.

#### G13 — trigger `fasse alle` — Unified Communication / Kollaboration

> Fasse alle Vorhaben rund um Kommunikations- und Kollaborationsdienste
> zusammen und beschreibe, welche Systeme dabei abgelöst, neu aufgebaut oder
> pausiert wurden.

Must-cite (10): JLU UC-Strategie Kommunikations- und Kollaborationslösungen ·
Betriebskonzept Rainbow als Unified Communication-Lösung · JLU-weites
Confluence · Sympa-Refresh · Greenlight-UpdateWechsel zu Pilos Verwaltungsportal
für BBB-Räume · Erneuerung Video Streaming · Neues Servicedesign für
Veranstaltungsaufzeichnungen · JLUcontact · Außerbetriebnahme altes
IMAP-E-Mail-System (Dovecot) · M365-Planung

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | UC-Strategie zielt auf Präsidiums-Entscheidung über ein UC-Konzept, abgeschlossen | *JLU UC-Strategie…*: „Ziel ist eine Präsidiums-Entscheidung über ein Unified Communications-Konzept, um nächste Schritte für die Modernisierung … der JLU-weiten Sprachkommunikations-Infrastruktur zu planen (\"Arbeitsplatz der Zukunft\")“; `Status Green40-abgeschlossen` |
| 2 | Rainbow pausiert, wartet auf UC-Strategie | *Betriebskonzept Rainbow…*: `Status trueYellow60-pausiert`; Änderungshistorie „18.02.25: Statusänderung zu \"pausiert\" (wartet auf Projekt UC-Strategie) nach ALB 17.02.25“ |
| 3 | Greenlight → Pilos statt GL3 | *Greenlight-Update…*: „Das Verwaltungsportal Greenlight (GL) wird in der aktuellen Version 2 nicht mehr unterstützt. Stattdessen soll Pilos zum Einsatz kommen (statt eines zunächst beabsichtigten Update auf GL Version 3).“ |
| 4 | Opencast löst Upload-Tool ab; Abschaltung nicht im Scope | *Erneuerung Video Streaming*: „Ablösung des alten Upload-Tools durch Opencast … das Abschalten des alten Streaming-Servers ist nicht Bestandteil des Projekts und muss neu projektiert werden.“ |
| 5 | Veranstaltungsaufzeichnungen ab SoSe 2028 standardisiert, Altformat bis WiSe 2026/27 | *Neues Servicedesign für Veranstaltungsaufzeichnungen*: „Überführung … in ein standardisiertes und automatisiertes Servicemodell ab dem Sommersemester 2028 (Weiterbetrieb des bisherigen Serviceformats bis einschließlich Wintersemester 2026/27)“ |
| 6 | Confluence auf 1.000 Lizenzen; Sympa-Server erneuert; JLUcontact aus OpenLDAP | *JLU-weites Confluence*: „Erhöhung der Lizenz-Anzahl auf 1.000“ · *Sympa-Refresh*: „Erneuerung des Servers für Mailinglisten“ · *JLUcontact*: „Bereitstellung einer zentralen webbasierten Kontaktauskunft auf Basis von Daten aus OpenLDAP.“ |

#### G14 — trigger `überblick über alle` — Web-Auftritt / Sichtbarkeit

> Gib mir einen Überblick über alle Vorhaben zum Web-Auftritt und zur
> digitalen Sichtbarkeit der JLU und erkläre, wie sie inhaltlich und zeitlich
> zusammenhängen.

Must-cite (12): Webrelaunch · HRZ-Webseiten Vereinheitlichung und Verschlankung ·
Webstatistik mit Matomo … · Bilddatenbank (JLU-weit) · Forschungsinformationssystem
(FIS) Integration in JLU-Webseite · Forschungsdatenrepositorium Vernetzung mit
FIS … · Barrierefreie IT umsetzen · Website KI an der JLU · KI an der JLU
bündeln auf&nbsp;Webseite · Projekt-Website im BfD-Bereich · Arbeitspaket
Sichtbarkeit - Website · 2025-11-13 Brainstorming Große KI Webseite

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | Web-Auftritt bis 2029 neu; Continuous Relaunch; externe Agentur | *Webrelaunch*: „Die JLU gestaltet bis 2029 ihren Web-Auftritt mit dem Fokus Exzellenzstrategie und Studierendenmarketing grundlegend neu … Implementierung eines Prozesses für den Regelbetrieb \"Continuous Relaunch\" … Umstellung des Betriebsmodells auf eine externe Agentur“ |
| 2 | HRZ-Seiten als Basis für den Relaunch, ca. 50 % weniger Seiten | *HRZ-Webseiten…*: „Ansprechender und zeitgemäßer Webauftritt als Basis für das anstehende Relaunch-Projekt … die Gesamtzahl der Seiten deutlich reduziert werden (ca. 50%) und die Navigationsebenen minimiert“ |
| 3 | Bilddatenbank nur avisiert; Rechtemanagement; Bezug zum Relaunch | *Bilddatenbank (JLU-weit)*: `Status 10-AVISIERT`; „Insbesondere mit Blick auf den Webrelaunch wird der JLU-weite Zugriff auf rechtlich einwandfreies JLU-Bildmaterial (Urheber- und Nutzungsrechte geklärt) wichtig … Ein Rechtemanagement regelt dabei den Zugriff“ |
| 4 | Matomo ausgerollt, dezentrales Nutzungsmodell, abgeschlossen | *Webstatistik mit Matomo…*: „Ausrollen des Tools (Aufsetzen der Anwendung, Konfiguration, erste Tests und Implementierung eines Modells für die dezentrale Nutzung des Dienstes)“; `Status Green40-abgeschlossen` |
| 5 | FIS-Stufe 1 = Publikationslisten; zweites Vorhaben speist JLUdocs/JLUdata ins FIS | *FIS Integration in JLU-Webseite*: „Erste Stufe der Nutzung der FIS-Daten auf der Webseite der JLU: Publikationsliste der ProfessorInnen.“ · *Forschungsdatenrepositorium…*: „Die Metainformationen über die in JLUdocs und JLUdata abgelegten Publikationen werden ins FIS übertragen“ |
| 6 | Barrierefreiheit gesetzlich vorgegeben; auch Relaunch-Anforderung | *Barrierefreie IT umsetzen*: „Gesetzliche Grundlagen dazu sind: … HessBGG … BITV HE 2019 … HHG, im Sozialgesetzbuch IX (SGB IX), in der UN-Behindertenrechtskonvention (UN-BRK) und … im Barrierefreiheitsstärkungsgesetz (BFSG)“ · *Webrelaunch*: „dies beinhaltet u.a. eine hohe Barrierefreiheit“ |

#### G15 — trigger `vergleiche alle` — Gebäude / Energie / Liegenschaften

> Vergleiche alle Vorhaben rund um Gebäude-, Energie- und
> Liegenschaftsmanagement miteinander und arbeite heraus, welche auf
> Verbrauchssenkung und welche auf Betriebssicherheit zielen.

Must-cite (10): Energiemanagement (Gesetzesvorgabe) · Digitale Thermostate … ·
Zählerstrukturen Verbrauchsmedien in Gebäuden … · Parkraummanagement … ·
Schnittstelle CAFM-SAP · Schnittstelle CAFM-IAM · Gerätedatenbank zur
Überwachung von Lebenszykluskosten · Erneuerung EMA - HRZ (Einbruchmeldeanlage) ·
Erneuerung ELA - UB Durchsageanlage der Unibibliothek · Intrakey Workflow  App …

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | Zertifizierungsfähiges Energiemanagementsystem nach DIN 50001, Ziele auf drei Ebenen | *Energiemanagement (Gesetzesvorgabe)*: „Einführung eines vollständigen und zertifizierungsfähigen Energiemanagementsystems nach DIN 50001 … definiert die JLU … konkrete Energieziele auf System-, Gebäude- und Prozessebene.“ (Quelle schreibt DIN, nicht ISO — wörtlich übernommen) |
| 2 | Thermostate: geringe Kosten, bedarfsgerechte Regelung, Wärmeeinsparung | *Digitale Thermostate…*: „mit relativ geringen Investitionskosten und geringem Installationsaufwand eine bedarfsgerechte Regelung der Heizung zu realisieren und damit Wärmeenergie einzusparen.“ |
| 3 | Flächendeckende Zählerinfrastruktur als Grundlage des Energiemonitorings | *Zählerstrukturen…*: „Installation einer flächendeckenden Zählerinfrastruktur zur Erfassung des Energie- und Wasserverbrauchs der JLU-Gebäude. Dies stellt die Grundlage für ein funktionierendes Energiemonitoring dar.“ |
| 4 | EMA (laufend) und ELA (pausiert, Dez. E / Notbeleuchtung) = Betriebssicherheit | *Erneuerung EMA - HRZ*: „Austausch Einbruchmeldeanlage (EMA) HRZ“, `Status 30-laufend` · *Erneuerung ELA - UB*: `Status 60-pausiert`; „30.06.25: Nach ALB Projekt pausiert (u.a. aufgrund ausstehender Klärungen mit Dez. E bzw. Abhängigkeiten mit weiteren technischen Anforderungen wie der Notbeleuchtung)“ |
| 5 | Zwei CAFM-Schnittstellen: Rechnungsdaten < 10.000 € aus EVER/SAP, Identitäten aus IAM | *Schnittstelle CAFM-SAP*: „Übertragung von Rechnungsdaten zu Bauaufträgen und Bestellungen unter 10.000 € aus EVER/SAP in das CAFM-System“ · *Schnittstelle CAFM-IAM*: „Für die CAFM-Module zur Schlüssel- und zur Fuhrparkverwaltung werden Daten zu den Identitäten der JLU benötigt. Diese werden aktuell wöchentlich … aus SAP exportiert, manuell in einem Excel-Tool aufbereitet“ |
| 6 | Gerätedatenbank (Lebenszykluskosten), Parkraum (bargeldlos), Intrakey pausiert | *Gerätedatenbank…*: „Softwarelösung zur Überwachung von Lebenszykluskosten … insbesondere zur Wirtschaftlichkeitsermittlung von Wartungs- und Reparaturkosten“ · *Parkraummanagement…*: „Digitale und bargeldlose Zahlungsmöglichkeiten spielen dabei eine zentrale Rolle“ · *Intrakey…*: `Status 60-pausiert` |

#### G16 — trigger `gesamtbild` — IT-Betrieb / Servicemanagement

> Zeichne das Gesamtbild des zentralen IT-Betriebs- und Servicemanagements am
> HRZ und erkläre, wie Monitoring, Ticketsystem und Servicepunkt aufeinander
> aufbauen.

Must-cite (10): Zentrales Systemmonitoring Phase 1 · … Erweiterung (Stufe 2) ·
… Erweiterung (Stufe 3) · KIX-Funktionserweiterung … · Zentraler HRZ-Servicepunkt … ·
Zentraler Log-Server · DNA-Center · Client-Management-Rollout Softwaremanagement
durch Baramundi · Verbesserung der Ausfallsicherheit in der Servervirtualisierung ·
Gerätedatenbank zur Überwachung von Lebenszykluskosten

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | Phase 1 abgeschlossen: 1500–1800 Systeme in Checkmk, Schwellwerte, Key-User geschult | *Zentrales Systemmonitoring Phase 1*: „Alle HRZ-Systeme (ca. 1500-1800) sind im einheitlichen System Checkmk mit den Basis-Checks abgebildet Die Schwellwerte sind korrekt gesetzt … Die Key-User in den Abteilungen sind geschult“; `Status Green40-abgeschlossen` |
| 2 | Stufe 2 (laufend): Plugins/aktive Checks, Dashboards, NetApp/vCenter/Appliances, E-Mail-Alarmierung | *… Erweiterung (Stufe 2)*: „Einbringung von individuellen Funktionserweiterungen … in Form von Plugins und/oder aktiven Checks. Erstellung von aussagekräftigen Dashboards … Anbindung weiterer Systeme (z.B.: NetApp, vCenter-Cluster, Appliances …) Ausbau Alarmierung (Email)“; `Status 30-laufend` |
| 3 | Stufe 3 avisiert: Abhängigkeiten, Gesamtsicht, externe Überwachung, weitere Fachbereiche | *… Erweiterung (Stufe 3)*: „Ergänzung von Abhängigkeiten und einer bereichsübergreifenden logischen Gesamtsicht Weitere Optimierungen (ext. Überwachung) Anbindung weiterer Fachbereiche.“; `Status 10-AVISIERT` |
| 4 | KIX-18: SSP, CMDB, Asset Management, Rollenkonzept, Baramundi-Integration | *KIX-Funktionserweiterung…*: „1. Self-Service-Portal (SSP) 2. Configuration Management Database (CMDB) … 3. Asset Management (HRZ-Shop) 4. Rechte- und Rollenkonzept … 5. Integration mit Baramundi“ |
| 5 | Servicepunkt (avisiert): Räume 50–55, gemeinsame Theke | *Zentraler HRZ-Servicepunkt…*: „Die Gruppen **Service** und **Arbeitsplatzbetreuung** werden räumlich im Bereich der **Räume 50 bis 55** zusammengeführt, um Servicedesk und HRZ-Shop an einer gemeinsamen Theke als zentralen Anlaufpunkt zu bündeln.“; `Status 10-AVISIERT` |
| 6 | Log-Server neu konzipiert; DNA-Center-Teststellung betriebsbereit | *Zentraler Log-Server*: „Neu-Konzeption zentraler Log-Server (Produktauswahl, Implementierung, ggf. Migration und Außerbetriebnahme Altsystem)“ · *DNA-Center*: „Teststellung DNA-Center betriebsbereit“ |

#### G17 — trigger `fasse alle` — Studium / Lehre / Prüfungen

> Fasse alle Vorhaben zusammen, die Studium, Lehre und Prüfungen betreffen,
> und nenne jeweils das eingesetzte System sowie die betroffene Zielgruppe.

Must-cite (8): AKKREDICOLLAB … · Erneuerung Scanner-Klausuren Wechsel der
Prüfungsaufgabendatenbank · ILIAS Optimierung Lade- und Bearbeitungszeiten ·
Digitales Buchungssystem für die Deutschkurse des AAA · EUPeace Joint Digital
Campus · Vorprojekt Promovierendenverwaltung · JupyterHub als zentraler
Service · Neues Servicedesign für Virtuelle Desktops
(kleinerer Cluster als G01–G12: die Campusmanagement-/LMS-Kernsysteme gehören
bereits zu G05, hier stehen nur die dort nicht genannten Vorhaben.)

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | AKKREDICOLLAB: QM-System, interne Akkreditierung | *AKKREDICOLLAB…*: „Aufbau eines Qualitätsmanagementsystem für den Bereich Studium und Lehre, sodass die JLU ihre Studiengänge zukünftig intern – und damit eigenverantwortlich, effizienter sowie zielgerichteter akkreditieren kann.“ |
| 2 | Scanner-Klausuren: Fred → Frieda wegen Oracle Java, abgeschlossen | *Erneuerung Scanner-Klausuren…*: „Wechsel auf eine neue Aufgabendatenbank (von Fred zu Frieda) … die lokale Variante benutzt noch Oracle Java, was nicht mehr eingesetzt werden soll“; `Status Green40-abgeschlossen` |
| 3 | ILIAS-Optimierung abgeschlossen: Startseite und objektreiche Seiten | *ILIAS Optimierung…*: „Verbesserung der Lade- und Bearbeitungszeiten von ILIAS-Seiten, insb. der Startseite und Seiten, die viele unterschiedliche Objekte beinhalten.“; `Status Green40-abgeschlossen` |
| 4 | Deutschkurse AAA: Automatisierung, E-Payment, AAA/Finanzdezernat entlastet | *Digitales Buchungssystem…*: „einfache Buchung … Möglichkeiten des E-Payments … automatisierte Buchungen (Entlastung für Fachabteilungen: AAA, Finanzdezernat und andere) - Möglichkeit, für internationale Studierende und Gäste, sich niedrigschwellig im System anzumelden“ |
| 5 | EUPeace: 9 Partnerhochschulen, Vorlesungsverzeichnisse, LMS-Zugang, Mobilität | *EUPeace Joint Digital Campus*: „IT-Systeme der an EUPeace beteiligten 9 Partnerhochschulen \"verbinden\" - Vorlesungsverzeichnisse der EUPeace Allianzpartner verfügbar machen - Zugang zu den Learning Management Systemen aufbauen - Vereinfachung von Zulassung und Auslandsmobilität von Studierenden und Personal“ |
| 6 | JupyterHub: CPU-Notebooks mit SSO; TUD-Alternative evaluiert | *JupyterHub als zentraler Service*: „Bereitstellung von ressourcenlimitierten Notebook-Umgebungen mit zentraler Authentifizierung (SSO), die auf reinen CPU-Ressourcen laufen“; „Alternativ wird die Nutzung eines JupyterHub einer anderen Univserität (TUD) evaluiert.“ |

#### G18 — trigger `überblick über alle` — Speicher / Datensicherung / Archivierung

> Gib einen Überblick über alle Vorhaben zu Speicher, Datensicherung und
> Archivierung und ordne sie danach, ob sie den laufenden Betrieb absichern
> oder eine langfristige Aufbewahrung ermöglichen.

Must-cite (8): DaSi Backup2disc … · Daten-Archivierung als zentraler Service ·
Günstiger Massenspeicher … · LaVaH Langzeitverfügbarkeit an hessischen
Hochschulen · Forschungsdatenrepositorium Vernetzung mit FIS … · Verbesserung
der Ausfallsicherheit in der Servervirtualisierung · Aktualisierung der
Wiederanlaufpläne inkl. Disaster Recovery · H^3 (Digitalpakt Hessen)

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | DaSi: Redundanz + Storage-Modernisierung für TSM, Must-Have, abgeschlossen | *DaSi Backup2disc…*: „Redundanzaufbau und Storage-Modernisierung für TSM-Datensicherung (Backup-to-disk)“; `RedMust-Have`; `Status Green40-abgeschlossen` |
| 2 | Archivierung auf IBM Storage Protect (TSM); Anbindung zu sichernder Systeme out of scope | *Daten-Archivierung als zentraler Service*: „Bedarfsprüfung und vom Ergebnis abhängiger Aufbau eines Angebots zur längerfristigen Archivierung von Daten auf Basis von IBM Storage Protect (TSM)“; „Die Anbindung zu sichernder IT-Systeme ist nicht Teil des Projekts.“ |
| 3 | data1 von NetApp auf STOR3 (EONstor, Infortrend, 1 PB); NetApp überprovisioniert | *Günstiger Massenspeicher…*: „Bestehender Service data1 soll vom \"teuren\" Speicher der NetApp auf den günstigen Speicher STOR3 (EONstor; Infortrend 1PB) migriert werden.“; „data1 liegt z.zT. … auf der wesentlich teureren Netapp und ist somit … nicht gegenfinanziert und … deutlich überprovisioniert.“ |
| 4 | LaVaH: zwei Phasen 2019–2021 / 2022–2025; hebis; Dauerbetrieb nötig | *LaVaH…*: „In zwei Projektphasen (2019-2021 und 2022-2025) wurde schrittweise eine Infrastruktur für die Langzeitverfügbarkeit digitaler Objekte aufgebaut … Die Verantwortung für die Validierung und Archivierung der Daten sowie für das Risikomanagement liegt beim Hessischen Bibliotheksinformationssystem hebis. Der LaVaH Dienst muss in einen Dauerbetrieb überführt werden“ |
| 5 | FIS-Vernetzung: Erstimport in „zur Validierung durch UB“, danach nächtlich | *Forschungsdatenrepositorium…*: „Einmaliger vollständiger Erstimport - Übertragung zunächst in den Status \"zur Validierung durch UB\" … Danach: Nächtliche Übertragung der neuen/geänderten Informationen“ |
| 6 | Laufender Betrieb: Standort-Redundanz (Stretched Cluster / DR), Wiederanlaufpläne | *Verbesserung der Ausfallsicherheit…*: „Verbesserung der Ausfallsicherheit in der Servervirtualisierung mittels geeigneter Maßnahmen (z.B. Aufbau Standort-Redundanz mittels Stretched Cluster oder Desaster Recovery), ggf. mit Kapazitätserweiterung“ · Titel *Aktualisierung der Wiederanlaufpläne inkl. Disaster Recovery* |

#### G19 — trigger `vergleiche alle` — Virtualisierung / Container / Cloud

> Vergleiche alle Vorhaben zu Virtualisierung, Containern und Cloud-Nutzung
> miteinander und beschreibe, welche Plattformen dabei jeweils gesetzt und
> welche erst evaluiert werden.

Must-cite (8): Evaluierung alternativer Virtualisierungs-Plattformen ·
Containerbasierte Bereitstellung von Anwendungen · Cloud Computing Ressourcen
via OCRE … · Neues Servicedesign für Virtuelle Desktops · Verbesserung der
Ausfallsicherheit in der Servervirtualisierung · vPAW-Konzept · JupyterHub als
zentraler Service · H^3 (Digitalpakt Hessen)

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | Marktsichtung VMware-Alternativen; digitale Souveränität; Ergebnisse 05/2026 | *Evaluierung alternativer Virtualisierungs-Plattformen*: „Marktsichtung technisch vergleichbarer Alternativen zu HCI-Lösungen mit VMware, Ziel: Digitale Souveränität von US-Anbietern, Kostenstabilisierung bzw. -senkung“; „Vorstellung der Ergebnisse (05/2026)“ |
| 2 | Container auf VMware Tanzu + NSX | *Containerbasierte Bereitstellung von Anwendungen*: „Implementierung einer containerbasierten Bereitstellung von Anwendungen auf Basis von VMware Tanzu unter Nutzung der vorhandenen Compute- und Storage-Ressourcen … sowie Einbindung von NSX als Netzwerk- und Security-Lösung im Containerumfeld.“ |
| 3 | OCRE: nur Governance; Betrieb, Support und Einkauf out of scope | *Cloud Computing Ressourcen via OCRE…*: „Kernbestandteile sind die Definition von technischen und organisatorischen Leitplanken, die Ausarbeitung einer Kommunikationsstrategie sowie der Entwurf eines vereinfachten Bereitstellungsprozesses. Nicht Teil des Projekts ist der anschließende operative Betrieb sowie der laufende Support … oder der eigentliche Einkauf der Cloud-Kontingente“ |
| 4 | Virtuelle Desktops: Cloud nur prüfen, Windows 11; Vollumstieg out of scope | *Neues Servicedesign für Virtuelle Desktops*: „Einsatz cloudbasierter Lösungen für virtuelle Desktops prüfen - PC-Arbeitsplätze mit Windows 11 bereitstellen“; „**Out of Scope:** - Vollständiger Umstieg auf cloudbasierte Lösungen“ |
| 5 | vPAW abgeschlossen: Hyper-V-Härtung, AD-OUs und Gruppenrichtlinien | *vPAW-Konzept*: „die notwendigen Anpassungen in der AD (neue Organisationseinheiten und Gruppenrichtlinien zur Domänen und vPAW Härtung), die Einrichtung und Härtung der Hyper-V Virtualisierungpsplattform auf der vPAW Hardware“; `Status Green40-abgeschlossen` |
| 6 | JupyterHub: CPU + SSO gesetzt, GitLab-Integration nur evaluiert | *JupyterHub als zentraler Service*: „Eine mögliche Gitlab-Integration zur Versionierung und zum Austausch von Notebooks wird als Teil des Konzepts evaluiert.“ |

#### G20 — trigger `vergleiche alle` — Status `pausiert` / `abgebrochen`

> Vergleiche alle Vorhaben, die derzeit pausiert oder abgebrochen sind,
> miteinander und nenne jeweils den dokumentierten Grund für die
> Unterbrechung.

Must-cite (9): Betriebskonzept Rainbow … · DMS  Einführung digitales
Vertragsmanagement · Erneuerung ELA - UB Durchsageanlage der Unibibliothek ·
HISinOne MoveON Schnittstelle · Intrakey Workflow  App … · MFA für Admins … ·
Passwortverwaltung in der zentralen IT Evaluation · Software Asset Management ·
Prozess der DigITal-Projektentwicklung Welche Status durchläuft ein Projekt

Cluster size is the population, not a shortcut: a status sweep over all 297
files (`t9-status.py`) found exactly **7** Steckbriefe with `60-pausiert` and
**1** with `50-abgebrochen`.

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | SAM einziges abgebrochenes Vorhaben, 13.07.2026, organisatorische Alternativen | *Software Asset Management*: „13.07.2026: Nach ALB 13.07.2026 Status \"abgebrochen\" - Umsetzung über organisatorische Alternativlösungen.“; `Status Red50-abgebrochen` |
| 2 | Rainbow pausiert 18.02.2025, wartet auf UC-Strategie | *Betriebskonzept Rainbow…*: „18.02.25: Statusänderung zu \"pausiert\" (wartet auf Projekt UC-Strategie) nach ALB 17.02.25“ |
| 3 | MFA für Admins: pausiert 18.02.2025 (ISB), im Oktober 2025 weiter wartend auf M365 | *MFA für Admins…*: „18.02.25: Statusänderung zu \"pausiert\" (weitere Klärungen u.a. mit ISB) nach ALB 17.02.25“; „07.10.2025: Datum angepasst (weiterhin wartend auf M365).“ |
| 4 | Passwortverwaltung: Personalengpässe Basisdienste, Wiederaufnahme nach Onboarding neuer GL | *Passwortverwaltung…*: „30.06.2025: … (depriorisiert aufgrund von Personalengpässen Basisdienste)“; „17.11.25: Nach ALB Projektstatus geändert auf \"pausiert\". Wiederaufnahme nach Onboarding neuer GL.“ |
| 5 | ELA UB: 30.06.2025, Klärungen mit Dez. E, Notbeleuchtung | *Erneuerung ELA - UB…*: „30.06.25: Nach ALB Projekt pausiert (u.a. aufgrund ausstehender Klärungen mit Dez. E bzw. Abhängigkeiten mit weiteren technischen Anforderungen wie der Notbeleuchtung).“ |
| 6 | Intrakey nur mit Datum, DMS-Vertragsmanagement und HISinOne-MoveON ohne Grund | *Intrakey…*: „30.06.25: Nach ALB Projektende geändert auf 31.10.25, Projekt pausiert.“ (kein Grund genannt) · *DMS  Einführung digitales Vertragsmanagement* und *HISinOne MoveON Schnittstelle*: `Status 60-pausiert`, Änderungshistorie enthält **keinen** Statuswechsel-Eintrag |

#### G21 — trigger `gesamtbild` — KI-Governance / Beschlussvorschläge

> Erkläre das Gesamtbild der strategischen Verankerung von KI an der JLU und
> beschreibe, welche Beschlussvorschläge dem Präsidium vorgelegt wurden und
> welche Zuständigkeiten daraus folgen.

Must-cite (12): Beschlussvorschläge Arbeitsbereich Strategie und
Strukturbildung · Schärfung Beschlussvorschlag 2 · P-Vorlage Projektabschluss ·
Entwurf Präsidiumsvermerk Neue Wege mit KI · Entwurf Bearbeitung des Themas KI
an der JLU · Entwurf Planungsprojekt KI an der JLU · Kommunikation Präsidium ·
Kommunikation Projektabschluss Neue Wege mit KI · 2026-01-28 Ergebnisprotokoll ·
2026-02-10 P-Vorlage weiteres Vorgehen · Geschäftsfähigkeit - was ist das ·
Präsidiumsentscheidungen

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | Auftrag aus dem Entwicklungsplan, Zielhorizont 2027 | *Entwurf Bearbeitung des Themas KI an der JLU*: „Auftrag aus der aktuellen Version des Entwicklungsplans: Die JLU verfügt bis 2027 über eine klare Zielsetzung auf dem Gebiet der KI und entwickelt wettbewerbsdifferenzierende Fähigkeiten gezielt weiter.“ |
| 2 | Commitment-Beschluss + Gremienvorstellung; geschärfte Fassung | *Beschlussvorschläge Arbeitsbereich…*: „Das Präsidium beschließt ein klares Committment zur Nutzung von KI innerhalb zu etablierender Rahmenbedingungen und fordert VPW/CIO/BfD auf, die Projektergebnisse … in folgenden Gremien vorzustellen: Senat, EP, SK und AG Fachbereichsmanagement“ · *Schärfung Beschlussvorschlag 2*: „neu: Das Präsidium beschließt ein klares Commitment zur Nutzung von KI in Forschung, Lehre und Verwaltung“ |
| 3 | „KI-Säule“ im BfD mit vier Daueraufgaben | *Beschlussvorschläge Arbeitsbereich…*: „Etablierung einer \"KI-Säule\" im BfD … - KI wird analog zu / als Teilgebiet von Digitalisierung betrachtet - inhaltliche Anfragen koordinieren (nicht technisch) - Pflege und Aktualisierung zentrale KI-Website - Etablierung und Organisation von bestimmten Austauschformaten - KI Austausch im hessischen Hochschulverbund“ |
| 4 | Leitplanken aus der Zielvereinbarung; 31.12.2026 Lehre, 31.03.2027 Forschung | *Beschlussvorschläge Arbeitsbereich…*: „\"Bis 2028 sind strategische Leitplanken für den verantwortungsvollen Einsatz von Künstlicher Intelligenz in Forschung und Lehre verabschiedet und intern kommuniziert\". Es fordert die entsprechenden Abteilungen (VPF, StF; VPL, StL) auf, diese Leitplanken bis zum 31.12.2026 für den Bereich Lehre und bis zum 31.3.2027 für den Bereich Forschung zu erarbeiten.“ |
| 5 | Compliance-Beschluss am 10.02.2026 aus der P-Vorlage herausgelöst | *2026-02-10 P-Vorlage weiteres Vorgehen*: „Der Beschlussvorschlag zu Comliance wird aus der P-Vorlage herausgelöst - die Thematik soll außerhalb dieser Vorlage angegangen werden“ |
| 6 | Start September 2025 unter CIO/BfD; Ende mit Präsidiumsvorstellung am 24.02.2026 | *P-Vorlage Projektabschluss*: „Im September 2025 wurde das Projekt \"Neue Wege mit KI\" unter Leitung von CIO und BfD gestartet“; „Mit der Vorstellung der Projektergebnisse im Präsidium am 24.02.2026 wird das Planungsprojekt \"Neue Wege mit KI\" beendet, alle weiteren Maßnahmen und Aufgaben werden mit entsprechenden Zuständigkeiten versehen.“ |

#### G22 — trigger `fasse alle` — Besprechungsnotizen / Projektverlauf

> Fasse alle Besprechungsnotizen des Projekts "Neue Wege mit KI"
> chronologisch zusammen und beschreibe, wie sich Projektauftrag,
> Workshop-Planung und Abschlussvorbereitung über die Termine hinweg
> entwickelt haben.

Must-cite (15): die 13 datierten Protokollseiten 2025-07-14 … 2026-04-21 plus
Übersicht Besprechungsnotizen und Aufgaben und Projektstrukturplan. These 13
minutes pages are addressed by no other question in the set.

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | 14.07.2025: Scope / Organisation / Arbeitspakete; Rollen benannt | *2025-07-14 Workshop 14.07.2025*: „14.00 – 14.50 Uhr Teil 1: Projekt Scope 15.00 – 15.50 Uhr Teil 2: Projektorganisation 16.00 – 17.00 Uhr Teil 3: Arbeitspakete“; „Projektleitung: Marcus Enger & Eberhard Kurz - Projektkoordination: Johanna Daus - Projektmitarbeit: Sten Seegel, Christina Koch“ |
| 2 | 22.08.2025: P-Vorlage, Ziel 16.9. im Präsidium | *2025-08-22 Projekttreffen Vorbereitung P-Vorlage*: „Vorbereitung der P-Vorlage - Ziel: 16.9. im Präsidium“ |
| 3 | 19.09.2025: Struktur-/Ablaufplan, Aula gebucht, Moderation angefragt | *2025-09-19 Projekttreffen Projektstrukturplan*: „Vorstellung Projektstrukturplan und Projektablaufplan“; „Aula buchen 25.11.2025“; „Anfrage Moderation Human Digitals oder andere 25. November“ |
| 4 | 02.12.2025: Projekt endet mit Präsidiumsvorstellung; Leitplanken als neues Teilprojekt | *2025-12-02 Projekt-Jour Fixe*: „Projekt endet mit Vorstellung der Projektergebnisse im Präsidium 28.02.2026 - Die dort vorgeschlagenen Maßnahmen sind nicht Teil des Planungsprojekt“; „TOP 3: Strategische Leitplanken entwickeln - neues Teilprojekt“ |
| 5 | 17.12.2025: Zusatzaufgabe Verwaltungsworkshop; Aufteilung CIO-/BfD-Team | *2025-12-17 Projektbesprechung und Zusatzaufgabe…*: „Zusatzaufgabe Workshop KI in der Verwaltung“; „Nächste Schritte CIO-Team 1. Zwei Seiten \"Summary\" zu Best Practices und Bedarfe … 2. Entwurf von Entscheidungsvorschlägen … Nächste Schritte BfD-Team 1. Summary Workshopergebnisse … 2. Fertigstellung Website 3. Entwurf von Entscheidungsvorschlägen“ |
| 6 | 21.04.2026: Gremien, Projektwebsite, Roadmap-Veröffentlichung, Mail an TN | *2026-04-21 Projektabschluss*: „Vorstellung Projektergebnisse in Gremien … Aktualisierung Projektwebsite: Projektabschluss … Veröffentlichung der Roadmap auf der Projektseite … Anschließend (Wunsch von VPW): Mail zu Ergebnissen an WS-Teilnehmende mit Verweis auf Website“ |

Deliberately **not** asserted: a single project start/end date. The corpus
disagrees with itself (`2025-12-02` says the Präsidium date is 28.02.2026,
*P-Vorlage Projektabschluss* says 24.02.2026, *Kommunikation Projektabschluss*
says the project "wurde im März beendet"), so each dated claim is attributed
to the document it comes from (point 4 and G21 point 6) instead of merged.

#### G23 — trigger `gemeinsame themen` — KI-Bedarfserhebung / Anwendungsfälle

> Nenne gemeinsame Themen, die sich durch die Bedarfserhebung zu KI und die
> Best-Practice-Recherche ziehen, und ordne jedem Thema die vorgeschlagene
> Maßnahme zu.

Phrased without an article so the trigger appears verbatim *and* the German
is grammatical — same device as G07.

Must-cite (14): Zusammenfassung Bedarfe und Best Practice · KI Top-Bedarfe,
Bewertung und Priorisierung · Arbeitsbereich Bedarfe und Best Practice ·
Sammlung User Stories  Anwendungsfälle KI an der JLU · User Stories
Anwendungsfälle Bedarfsbündelung und wer macht was - Entwurf · Infos zu User
Stories · Anwendungsfälle · Fragen zum KI-Einsatz · Was tun wir schon an der
JLU · Sammlung Best Practices KI in der Hochschulverwaltung · die drei
KI-Austausch-Protokolle (2025-07-23, 2025-09-22, 2025-11-10) · Ideen
Quellensammlung

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | Fünf Erhebungsquellen | *Zusammenfassung Bedarfe und Best Practice*: „Stakeholder-Meetings: … zwischen Juli und November 2025 drei Stakeholder-Austausche … - KI-Workshop-Ergebnisse … - Studierendenbefragung 2025 … - ZAD-Arbeitstreffen-Ergebnisse: Erkenntnisse aus zwei World Cafés … - KI-Team HRZ … - Umfeld und Best Practices“ |
| 2 | „Make or Buy“ mit fünf Kriterien | *ebd.*: „Für den gesamten Technologie-Stack gilt die \"Make or Buy\"-Frage: Eigenbetrieb, wo nötig und sinnvoll, andernfalls externe (Cloud-)Lösung. Bekannte Kriterien … sind Fachlichkeit (inkl. Sicherheit), Wirtschaftlichkeit, Nachhaltigkeit, Digitale Souveränität, Zeit (Time to Market).“ |
| 3 | Infrastruktur fragmentiert; GPU-as-a-Service; CIO-Steuerung + OCRE bis Q3/2026 | *ebd.*: „Die aktuelle IT-Basis ist oft fragmentiert und nicht optimal ausgelastet.“; „Ein zentral verwalteter GPU-Pool (\"GPU-as-a-Service\") kann die Auslastung optimieren“; „Es beauftragt den CIO dazu, diese … beginnend in Q2/2026 abzustimmen. Ein zugehöriges Konzeptionsprojekt soll bis Ende Q3/2026 abgeschlossen sein. - Außerdem werden CIO/HRZ ein Projekt zur Konzeption zur Nutzung von Cloud Computing Ressourcen via Open Clouds for Research Environments (OCRE-Rahmenverträge des DFN) initiieren“ |
| 4 | Drei „Treppenstufen“ der Anwendungsfälle; Support-Chatbot als Maßnahme | *ebd.*: „##### KI am eigenen Arbeitsplatz (1. Treppenstufe) … ##### Arbeitsplatznahe KI-Integration (2. Treppenstufe) … ##### KI in Fachanwendungen (3. Treppenstufe) … KI-Komponenten in bestehender oder neuer Fach-Software (z.B. Bewerbermanagementsystem, CAFM), KI-Agenten“; „Das HRZ initiiert ein \"Support-Chatbot\"-Projekt zur Bereitstellung eines Systems für Website-Widgets“ |
| 5 | Compliance: „Schatten-KI“; Ampelsystem; erste Iteration Sommer 2026 | *ebd.*: „Lange und aufwändige Prüfungsprozesse riskieren allerdings das Entstehen von \"Schatten-KI\" … Vorgeschlagen wird daher ein Optimierungskonzept, in dem … eine vereinfachte Entscheidungsbasis nach Risikobewertung (z.B. Ampelsystem) entwickelt werden sollen … Ziel ist es, eine erste Iteration zum Sommer 2026 zu erarbeiten“ |
| 6 | KI-Hub als Toolbox mit SSO; Website bündelt Strategisches | *ebd.*: „Geplant ist ein \"KI-Hub\" ist als zentrale, technisch-anwendungsorientierte Toolbox für KI-Angebote der JLU … Der KI-Hub zielt … auf ein zentrales Portal für alle Anwendungen ab (Single Sign-On …), während die zentrale KI-Website primär strategische und vernetzende Aktivitäten bündelt“ |

#### G24 — trigger `überblick über alle` — Workshop-Kommunikation

> Gib mir einen Überblick über alle Kommunikations- und Einladungsmaßnahmen
> rund um den KI-Workshop und erkläre, welche Zielgruppen über welchen Kanal
> angesprochen wurden.

Distinct from G06: that question asks for *contradictions* in the orga
documents, this one for *channels and audiences*.

Must-cite (13): Kommunikation Workshop · Save the Date und E-Mailverteiler für
Einladung · Einladungstext · Verteiler · Anmeldung über Eveeno · Programm-Flyer ·
Mail zur Versendung an TN nach dem WS · Kommunikation Präsidium · Kommunikation
Projektabschluss Neue Wege mit KI · Entwurf Kommunikation C1 · Kommunikation
Personalentwicklung (PE) oder auch C5 · Arbeitspaket Sichtbarkeit - Website ·
Einführung von Eveeno - digitales Teilnehmendenmanagement

| # | Punkt (gekürzt) | Beleg (Chunk-Fragment) |
|---|---|---|
| 1 | Feste Reihenfolge: Save the Date → Einladung mit Agenda → DB-Bericht → Eveeno | *Kommunikation Workshop*: „Save the Date versenden … Text verfassen und abstimmen … breiten Mailverteiler erstellen … Einladung mit Agenda versenden … Bericht zu Workshop in der DB … Eveeno für Anmeldung“ |
| 2 | Save the Date am 07.10.2025 über ki@uni-giessen.de; Termin/Ort | *Verteiler*: „Save the Date Versendung am 07.10.2025 ki@uni-giessen.de“ · *Save the Date und E-Mailverteiler…*: „wann: 25. November 2025 09:00 - 16:00 Uhr wo: Aula im Hauptgebäude der JLU“ |
| 3 | Verteiler für persönliche Ansprache; Statusgruppen einzeln; AG FBM; ILIAS-Plugin | *Einladungstext*: „über verschiedene Verteiler versenden um \"persönliche Ansprache\" zu gewährleisten … Statusgruppen einzeln anschreiben … AG Fachbereichsmanagement über Jessica … ILIAS-Plugin über Mirco Hilbert“ |
| 4 | Eveeno erhebt die Gruppenzuordnung (fünf Gruppen) | *Anmeldung über Eveeno*: „Welchen Bereichen ordnen Sie sich zu? O Professorinnen und Professoren aller Fachbereiche O Mitarbeitende aus der Lehre O Mitarbeitende aus der Forschung O Mitarbeitende aus Verwaltung und Technik O Studierende“ |
| 5 | Dankes-Mail nach dem WS mit Website, Keynotes, 7 Einblicken, World Café | *Mail zur Versendung an TN nach dem WS*: „Die inspirierenden Keynotes von Christine Serrette (ITZBund) und Prof. Dr. Irene Bertschek sowie die sieben praxisnahen Einblicke in KI-Projekte an der JLU haben wichtige Impulse gesetzt. Besonders gewinnbringend waren die intensiven und konstruktiven Diskussionen an den Thementischen des World Cafés“ |
| 6 | Gremienkommunikation: Präsidiumsvermerk/-berichtspunkt, Senat, EP, AG FBM, SK Studiengänge | *Kommunikation Präsidium*: „# Präsidiumsvermerk für … # Präsidiumsberichtspunkt“ · *Kommunikation Projektabschluss Neue Wege mit KI*: „- Senat - EP TOP-Anmeldung … - AG Fachbereichsmanagement - Senatskommission Studiengänge … Senatsberichtspunkt erstellen für VPW“ |

### Points deliberately left out (not verifiable within scope)

- **Project start/end dates as a single fact** — see the G22 note above; the
  corpus contradicts itself, so dates are only stated with their source.
- **Whether Christine Serrette actually spoke** was almost stated from
  `2025-10-08 Projektbesprechung` ("Absage Frau Serrette"), which the later
  `2025-10-24` note ("Eberhard übernimmt die Kommunikation mit Frau Serette")
  and the post-workshop mail contradict. Only the post-workshop mail's
  statement is used (G24 point 5), and the cancellation is not asserted.
- **`Projektende` dates from the Steckbrief template** — same reason as
  Wave 4: the field is revised through changelog entries rather than holding
  a stable value (Containerbasierte Bereitstellung alone moves twice in 2026).
- **Named `Projektleitung` persons** outside the "Neue Wege mit KI" project,
  where they are stated in prose rather than in an `@mention` template field.
- **`Funktionspostfach.md`, `Raumbuchung.md`, `Programm-Flyer.md`,
  `Übersicht Besprechungsnotizen und Aufgaben.md`, `Projektorganisation.md`,
  `Arbeitsbereich Bedarfe und Best Practice.md`** are near-empty task lists,
  screenshots or Confluence macros; they appear in `must_cite_file_names`
  where a curator would expect them but carry no point of their own.
- **Status for the non-Steckbrief pages** (`Barrierefreie IT umsetzen`,
  `Website KI an der JLU`, …) — those pages use a different template with no
  status field; no status was asserted for them.

### Artifacts

- `eval/golden/global-synthesis-de.jsonl` — 24 rows (gitignored, never
  `git add`ed); copied to the main checkout at
  `/home/steffen/git/JustRAG/eval/golden/global-synthesis-de.jsonl` so both
  working copies hold the same fixture.
- `.superpowers/sdd/2026-09-06-rag-sota-wave5/t9-smoke.json`, `t9-smoke.log` —
  the single G13 smoke.
- `.superpowers/sdd/2026-09-06-rag-sota-wave5/t9-files.txt`, `t9-status.txt` —
  the read-only corpus listings the curation was done against.
- `.superpowers/sdd/2026-09-06-rag-sota-wave5/t9-append-rows.py` — the authored
  rows in source form, so the gitignored jsonl is reproducible from a tracked
  worktree artifact.

## §4 Wave 5 re-measurement under the pooled rule (W5-R1, pre-registered 2026-09-06)

### The rule, first

**Ruling W5-R1** (registered 2026-09-06, *before* the extended set existed and
*before* any run on it — see `eval/golden/README.md` § "Pooling two
comparisons — `--pairwise-pool`" and `rulings.md`). Over the extended
global-synthesis set (N ≥ 24 questions), two cross pairs are run
(flat1 vs mr1, flat2 vs mr2) and the decisive pairs of the two are **pooled**
(ties excluded from every rate). All four sub-criteria must pass:

1. pooled `map_reduce` (B) win rate ≥ 0.60
2. pooled Wilson lower bound (z = 1.96) > 0.50
3. pooled mean coverage of `map_reduce` not below flat's mean coverage by
   more than the flat1-vs-flat2 coverage band
   (`band = |mean_cov(flat1) − mean_cov(flat2)|`)
4. the control pair (flat1 vs flat2) win rate lands inside `[0.35, 0.65]`
   (otherwise the judge is unstable on this set and the run is inconclusive)

All four → `chat_longcontext_mode` default flips to `map_reduce` in Task 11
(the route stays gated by `chat_longcontext_enabled`). Any failure → `flat`
stays. Cost is reported, not a veto. This replaces the per-pair W4-R7 rule
(§2) for all future runs. Nothing below changes the verdict after the fact;
anything not in the four numbered criteria above is labelled **post hoc**.

### Setup

Same KB (`83262307-3a1b-49bc-bd08-3b925a868a92`, PPM-Eval) and the same
unchanged site-config baseline as §1/§2 — no `site_configs` row was written;
`chat_longcontext_enabled`/`chat_longcontext_mode` are supplied per run
through the `--longcontext`/`--longcontext-mode` overlays, confirmed in each
run's first log line, e.g. `t10-flat1.log`:
`{"msg":"eval: applying chat site_config overlays for this
run","overlays":{"chat_longcontext_enabled":"true","chat_longcontext_mode":"flat"}}`
(and `"map_reduce"` for mr1/mr2). Fixture: `eval/golden/global-synthesis-de.jsonl`,
**24 questions** (G01–G24, §3). Binary built once from commit `15c383a`
(`.superpowers/sdd/2026-09-06-rag-sota-wave5/eval-wave5`). Model stack
unchanged from §1/§2 (`jlu/gemma-4-26b-it` answer + all judges,
`jlu-internal/gemma-4-26b-it-bulk` map-stage extractor, `jlu/jina-rerank`
reranker, `jlu/qwen3-embedding` 4096-dim embedder). All four judged runs and
three pairwise comparisons ran back-to-back on the dev stack with nothing
else scheduled against it, per the runner's lock check.

### Commands (exact, `t10-run.sh`)

```bash
export DB_HOST=localhost DB_PORT=5432 DB_USER=postgres DB_PASSWORD=postgres DB_NAME=rag_db
export VECTOR_DB_HOST=localhost VECTOR_DB_PORT=5433 VECTOR_DB_USER=postgres VECTOR_DB_PASSWORD=postgres VECTOR_DB_NAME=rag_vector_db
export JWT_SECRET=local-eval-acceptance-secret-0123456789abcdef
export REDIS_HOST=localhost REDIS_PORT=6379 REDIS_PASSWORD=redis
export S3_ENDPOINT=http://localhost:9000 S3_ACCESS_KEY=minioadmin S3_SECRET_KEY=minioadmin S3_BUCKET=rag-files S3_REGION=us-east-1

# four judged runs, alternating flat / map_reduce, on the extended 24-question set
eval-wave5 --golden eval/golden/global-synthesis-de.jsonl --production-context \
  --orchestrator-dispatch=true --judge --longcontext on --longcontext-mode flat \
  --output t10-flat1.json        # then mr1 (map_reduce), flat2, mr2, same shape

# three pairwise comparisons (win rate is A's; A is always the first path named)
eval-wave5 --pairwise-a t10-flat1.json --pairwise-b t10-mr1.json   --pairwise-out t10-pw-1.json
eval-wave5 --pairwise-a t10-flat2.json --pairwise-b t10-mr2.json   --pairwise-out t10-pw-2.json
eval-wave5 --pairwise-a t10-flat1.json --pairwise-b t10-flat2.json --pairwise-out t10-pw-ctrl.json

# W5-R1's actual decision statistic: pool the two cross pairs
eval-wave5 --pairwise-out t10-pw-pooled.json \
  --pairwise-pool t10-pw-1.json t10-pw-2.json
```

Full driver: `.superpowers/sdd/2026-09-06-rag-sota-wave5/t10-run.sh`. Wall
clock (`t10-run.log`): flat1 1558 s, mr1 1797 s, flat2 1244 s, mr2 1797 s —
total ≈ 1 h 43 min for the four judged runs, plus the near-instant pairwise
and pooling steps (file-only, no retrieval/judge calls).

### Per-run validity table (n=24, k=10 per run)

All four runs pass every validity check from the hand-off: `errors == 0`,
every one of the 24 questions has `agent.orchestrator == "longcontext"`
(`classified_query_type == "complex_reasoning"` throughout), and
`judge.coverage` is present for all 24 (`coverage_n = 24`).

| Run | errors | orchestrator=longcontext (24/24) | coverage n | mean coverage | mean faithfulness (n) | mean context precision | mean answer relevance | mean answer length (runes) | judge warnings | judge errors | wall time |
|---|---|---|---|---|---|---|---|---|---|---|---|
| flat1 | 0 | 24/24 | 24/24 | 0.5403 | 0.4642 (24) | 0.4500 | 1.0000 | 3031 | 3 | 0 | 1558 s |
| mr1 | 0 | 24/24 | 24/24 | 0.5972 | 0.4615 (23) | 0.5458 | 1.0000 | 4082 | 1 | 1 | 1797 s |
| flat2 | 0 | 24/24 | 24/24 | 0.5264 | 0.5694 (24) | 0.4500 | 1.0000 | 3207 | 3 | 0 | 1244 s |
| mr2 | 0 | 24/24 | 24/24 | 0.5701 | 0.5327 (23) | 0.5250 | 1.0000 | 3938 | 1 | 1 | 1797 s |

Answer relevance is fully saturated (24/24 at 1.000 in every run), consistent
with §1/§2's finding that the judge has no discriminative power on this
route — expected, not a criterion. Faithfulness and context precision remain
diagnostic, not decision inputs, per W5-R1 (only coverage and the pairwise
judge feed the decision).

**Judge instrument faults, pre-existing and independent of mode (post hoc):**
mr1 G05 and mr2 G09 each hit a faithfulness judge call whose response the
strict decoder rejected (`judge_errors`; that question's faithfulness is
simply absent from its run's mean, computed over the other 23).

> **Diagnosis correction (Wave-5 final fix wave).** The `` ```json `` fence
> visible in the recorded preview is **not** the cause. The binary these runs
> used already carried the Wave-4 balanced-object scanner, under which a
> fenced complete object parses (verified by replay) and a truncated one
> reports the distinct `truncated JSON` error. Both of these reported the
> plain `response is not valid JSON`, which leaves exactly one class: a
> **brace-balanced object the JSON decoder still rejects** — a raw newline
> inside a string, an unescaped `"` inside a string, or a trailing comma. The
> recorded 120-byte preview cannot say which, because the offending byte sits
> past it and the decoder's own error was dropped. That is fixed: the error
> now carries the decoder's message (which names the character and its
> offset) and a 400-rune, rune-safe preview, so one further occurrence
> identifies the shape. No parser tolerance was added on a guess. The
recurring `context_precision: judge returned 11 booleans, expected 10 —
truncated/padded` warning fired 8 times across the four runs (flat1
G02/G07/G21; flat2 G02/G07/G23; mr1 G08; mr2 G22) — same pre-existing
boolean-count quirk as §1/§2, zero effect beyond dropping that one question's
context-precision score.

**No degenerate-answer anomaly this wave** (unlike §2's flat1 G01, a 17337-rune
answer with a ~15400-character garbage run): the longest answer in any of the
four runs is mr1 G02 at 6638 runes, a normal length for a synthesis answer
over 200 chunks. **`answer_degenerate_guard` (W5-R4) trajectory-event count:
0 across all four runs** (`t10-analyse.py` §1b greps every run's JSON and log
for the marker; none found). Note this eval mode does not run `--trajectory`,
so there is no per-question trajectory array to scan — the grep is over the
raw report/log text, which is where the marker would appear if the guard had
fired via its own log line. The unrelated `rag.reranker.degenerate` WARN
lines seen 18 times per run's log are a **pre-existing reranker-calibration**
signal (score-distribution stddev below threshold) and have nothing to do
with the W5-R4 answer guard — flagged here only because the substring match
is easy to confuse.

### Coverage — noise band + per-question table (all 24 questions)

Noise band = `|mean_coverage(flat1) − mean_coverage(flat2)| = |0.5403 −
0.5264| = 0.0139` (**1.39 pp**).

| Q | flat1 | mr1 | flat2 | mr2 |
|---|---|---|---|---|
| G01 | 0.800 | 1.000 | 0.800 | 0.600 |
| G02 | 0.667 | 0.833 | 0.667 | 0.833 |
| G03 | 0.667 | 0.500 | 0.667 | 0.500 |
| G04 | 0.333 | 0.333 | 0.500 | 0.500 |
| G05 | 0.333 | 0.833 | 0.500 | 0.833 |
| G06 | 1.000 | 0.500 | 0.500 | 0.750 |
| G07 | 0.667 | 0.500 | 0.333 | 0.000 |
| G08 | 0.667 | 0.500 | 0.500 | 0.667 |
| G09 | 0.667 | 0.833 | 0.667 | 0.667 |
| G10 | 0.667 | 0.667 | 0.500 | 0.667 |
| G11 | 0.500 | 0.833 | 0.500 | 0.833 |
| G12 | 0.500 | 0.500 | 0.500 | 0.500 |
| G13 | 0.500 | 0.333 | 0.333 | 0.500 |
| G14 | 0.500 | 0.500 | 0.667 | 0.833 |
| G15 | 0.500 | 0.500 | 0.500 | 0.500 |
| G16 | 0.500 | 0.500 | 0.500 | 0.500 |
| G17 | 0.333 | 0.833 | 0.167 | 0.500 |
| G18 | 0.333 | 0.833 | 0.500 | 0.500 |
| G19 | 0.500 | 0.500 | 0.500 | 0.500 |
| G20 | 0.500 | 0.667 | 0.667 | 0.667 |
| G21 | 0.500 | 0.500 | 0.667 | 0.667 |
| G22 | 0.500 | 0.500 | 0.333 | 0.167 |
| G23 | 0.500 | 0.333 | 0.500 | 0.333 |
| G24 | 0.333 | 0.500 | 0.667 | 0.667 |

Cross-pair coverage deltas (map_reduce − flat, same run pair): pw1
`mr1 − flat1 = 0.5972 − 0.5403 = +0.0569` (**+5.69 pp**, beyond the 1.39 pp
band); pw2 `mr2 − flat2 = 0.5701 − 0.5264 = +0.0438` (**+4.38 pp**, also
beyond the band). **Pooled** coverage: `mean(mr1, mr2) = 0.5837`,
`mean(flat1, flat2) = 0.5333`, pooled delta `= +0.0503` (**+5.03 pp**) — this
is the number W5-R1 sub-criterion 3 actually tests, and it clears the 1.39 pp
band with room to spare in both cross pairs and pooled.

### Map/reduce trajectory stats (mr1, mr2 logs)

Both runs: 24 questions × 25 groups/question (`chat_longcontext_map_group_size`
default 8, 200-chunk pool ÷ 8 = 25) = 600 groups/run, `dropped_findings = 0`
throughout (no reduce-stage truncation, the W3-R7 fallback never needed to
spill).

| Run | groups | failed groups | findings | dropped findings |
|---|---|---|---|---|
| mr1 | 600 | 3 | 1867 | 0 |
| mr2 | 600 | 1 | 1907 | 0 |

The 4 failed groups (`longcontext.map_group_failed`, `error: "context
deadline exceeded"`) landed on mr1 groups 19/1/14 and mr2 group 1 — the
W3-R7 fallback (raw first-600-rune chunk text instead of an extracted
finding) covered them; no question errored.

### Pairwise comparisons (winner is from A's perspective)

| Pair | A | B | wins(A) | ties | losses(A)=wins(B) | decisive | A win rate | A Wilson [lo,hi] | B (map_reduce) win rate | B Wilson [lo,hi] | tie rate | skipped/errors |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| pw1 | flat1 | mr1 | 1 | 3 | 20 | 21 | 0.0476 | [0.008, 0.227] | **0.9524** | **[0.773, 0.992]** | 0.125 | 0/0 |
| pw2 | flat2 | mr2 | 1 | 8 | 14 | 15 | 0.0667 | [0.012, 0.298] | **0.9333** | **[0.702, 0.988]** | 0.348 | 1/1 |
| ctrl | flat1 | flat2 | 4 | 13 | 7 | 11 | 0.3636 | [0.152, 0.646] | 0.6364 | [0.354, 0.848] | 0.542 | 0/0 |

(B's Wilson interval is the exact complement of A's, `[1−hi(A), 1−lo(A)]`,
confirmed by direct Wilson computation in `t10-analyse.py` §5.)

**pw2's one skipped/errored pair:** G06's `(B,A)`-order judge call returned a
response the strict decoder rejected
(`"note":"judge (B,A) failed: response is not valid JSON: ..."`); the pair
contributes to neither wins/ties/losses nor the win-rate denominator. The
`` ```json `` fence in the preview is not the cause — see the diagnosis
correction under "Judge instrument faults" above; the same brace-balanced-but-
invalid class as mr1 G05 / mr2 G09.

**Control pair (flat1 vs flat2, sub-criterion 4):** win rate **0.3636**,
inside `[0.35, 0.65]` — but only just: the margin to the lower floor is
**0.3636 − 0.35 = 0.014**, i.e. a single verdict's worth on an 11-decisive-pair
denominator (one flipped decisive pair moves the rate by ~0.09). The control
therefore passes, and it passes *narrowly*; a re-run of this design should not
treat "control inside the band" as a comfortable result. The judge is not
systematically preferring one flat run over the identically-configured other,
so the two cross-pair results below are not an artifact of judge instability. Widest Wilson interval of
the three pairs (`[0.152, 0.646]`) and the highest tie rate (0.542, 13 of 24
pairs) — a same-configuration comparison should be close to indistinguishable,
and it is.

### Per-question verdict table

| Q | pw1 (flat1/mr1) | pw2 (flat2/mr2) | ctrl (flat1/flat2) |
|---|---|---|---|
| G01 | B | B | B |
| G02 | B | B | A |
| G03 | B | tie | B |
| G04 | tie | tie | tie |
| G05 | B | tie | tie |
| G06 | A | skipped | tie |
| G07 | tie | A | tie |
| G08 | B | B | tie |
| G09 | B | B | tie |
| G10 | B | tie | B |
| G11 | B | B | A |
| G12 | B | B | tie |
| G13 | B | B | tie |
| G14 | B | B | B |
| G15 | B | B | tie |
| G16 | tie | tie | tie |
| G17 | B | B | tie |
| G18 | B | B | A |
| G19 | B | B | B |
| G20 | B | tie | B |
| G21 | B | tie | B |
| G22 | B | B | A |
| G23 | B | tie | tie |
| G24 | B | B | tie |

map_reduce (B) wins the large majority of decisive pairs in both cross
comparisons (20/21 in pw1, 14/15 in pw2); the control pair splits close to
even with a heavy tie rate, exactly the expected signature of a judge that
discriminates real mode differences but not noise between identical runs.

### Three disagreement excerpts (both orders' reasoning, `agree_both_orders = false`)

**pw1 G04** (winner recorded as `tie`):
> **A-then-B order:** "Antwort A bietet eine bessere Übersicht über das
> 'Gesamtbild', da sie die Modernisierung in zwei klare technologische
> Säulen (RZ und Netzwerk) unterteilt und zusätzlich die strategische
> Komponente (KI) als Abhängigkeit hervorhebt. Zudem ist die zeitliche
> Einordnung in einer Tabelle deutlich übersichtlicher..."
>
> **B-then-A order:** "Antwort A ist präziser bei der Beschreibung der
> zeitlichen Abhängigkeiten und nennt konkrete Daten (30.09.2026,
> 01.07.2026), während Antwort B eher allgemeine Zeiträume (2024, 2025 etc.)
> verwendet. Zudem geht Antwort A besser auf die spezifischen technischen
> Abhängigkeiten ein..."

Both orders pick "the answer shown first" — a textbook position-bias flip,
correctly neutralised into a tie by the swap-and-require-agreement design
rather than credited to either mode.

**pw2 G03** (winner recorded as `tie`):
> **A-then-B order:** "Antwort A erfüllt die Aufgabenstellung besser, da sie
> im ersten Schritt die explizite Anforderung 'Vergleiche alle Vorhaben'
> durch eine tabellarische Übersicht der einzelnen Vorhaben mit deren Fokus
> umsetzt. Antwort B springt direkt zur Kategorisierung der Maßnahmen..."
>
> **B-then-A order:** "Antwort A ist besser, da sie die Anforderung
> 'Vergleiche alle Vorhaben' umfassender erfüllt. Während Antwort B die
> Vorhaben in einer Tabelle auflistet, geht Antwort A tiefer in die
> inhaltliche Strukturierung der Maßnahmen ein..."

Same signature as pw1 G04 — both orders prefer whichever answer is shown
first, correctly resolved to a tie.

**ctrl G04** (winner recorded as `tie`, between the two flat runs):
> **A-then-B order:** "Antwort A bietet eine bessere Übersicht über die
> zeitliche Einordnung durch die tabellarische Darstellung der Meilensteine
> (2024-2028). Antwort B konzentriert sich eher auf die logische Abfolge der
> Phasen..."
>
> **B-then-A order:** "Antwort A bietet eine etwas bessere Strukturierung
> des 'Gesamtbildes', indem sie die drei Ebenen (Baulich, Netzwerk, Digitale
> Dienste) klarer voneinander trennt... Antwort B ist zwar durch die Tabelle
> übersichtlicher bei den Daten, aber Antwort A geht tiefer auf die logische
> Kette der Abhängigkeiten ein."

Both orders in the control pair also favour "first shown", consistent with
the pw1/pw2 pattern above — position bias is a property of the judge on this
question shape, resolved the same conservative way regardless of which two
runs are being compared.

### The pooled statistic (W5-R1's decision input)

From `t10-pw-pooled.json` (B = map_reduce's view), and independently
recomputed from `t10-pw-1.json` + `t10-pw-2.json` in `t10-analyse.py` §8
(counts match exactly):

- decisive pairs pooled = **36** (pw1: 21, pw2: 15), ties pooled = 11
- **A (flat) wins = 2, B (map_reduce) wins = 34**
- **pooled map_reduce win rate = 34/36 = 0.9444**
- **pooled Wilson interval (z = 1.96) = [0.8186, 0.9846]**

### Control check

flat1-vs-flat2 win rate = **0.3636**, inside `[0.35, 0.65]` (see the pairwise
table above) — the control passes, so the judge is not simply biased toward
one of the two "flat" reports; the 34/36 pooled result is read as a real
map_reduce preference rather than judge noise.

### Cost (reported, not a veto)

| | flat1 | flat2 | mean | mr1 | mr2 | mean | ratio (mr/flat) |
|---|---|---|---|---|---|---|---|
| wall time | 1558 s | 1244 s | 1401 s | 1797 s | 1797 s | 1797 s | **1.28×** |

map_reduce's mean wall time (1797 s) is **1.28×** flat's (1401 s) — markedly
cheaper, relatively, than §2's Wave-4 measurement (1.65×) at half the
question count and the same 25-groups-per-question map stage; the absolute
per-question map-stage cost (25 fast-tier calls) is unchanged, so the lower
ratio here reflects flat1/flat2's wall time varying more between the two
Wave-5 runs (1558 s vs 1244 s) than a genuine map_reduce speed-up — flat2 is
simply the fastest of the four runs. This is reported per the rule; it does
not veto the decision below.

### Decision (W5-R1)

| # | Sub-criterion | Value | Threshold | Pass? |
|---|---|---|---|---|
| 1 | pooled map_reduce win rate | 0.9444 | ≥ 0.60 | **yes** |
| 2 | pooled Wilson lower bound | 0.8186 | > 0.50 | **yes** |
| 3 | pooled coverage delta (mr − flat) | +5.03 pp | ≥ −1.39 pp (band) | **yes** |
| 4 | control (flat1 vs flat2) win rate | 0.3636 | ∈ [0.35, 0.65] | **yes** |

**All four sub-criteria pass. `chat_longcontext_mode` default flips to
`map_reduce`** (Task 11 makes the site_config default change; the route
stays gated behind `chat_longcontext_enabled`, unchanged). Applying W5-R1
literally, exactly as pre-registered on 2026-09-06 before this set or any of
these runs existed — no criterion was relaxed or reinterpreted after seeing
the data.

**Post hoc, supporting evidence (not part of the rule):** the per-question
verdict table shows map_reduce winning the overwhelming majority of decisive
pairs in *both* cross comparisons individually (20/21 and 14/15), not merely
in the pooled tally — unlike §2's Wave-4 measurement, where pw1 (11
decisive, 8/11 wins) missed the *per-pair* Wilson bar by one win while pw2
(9 decisive, 8/9) cleared it; here both pairs clear the pooled bar and would
also individually clear a per-pair 0.60/Wilson-low>0.50 bar (pw1: 20/21 =
0.952, Wilson low 0.773; pw2: 14/15 = 0.933, Wilson low 0.702) — the larger
set (24 vs 12 questions) resolved the under-powering §2 flagged, and the
pooled and per-pair reads now agree.

### Artifacts

Under `.superpowers/sdd/2026-09-06-rag-sota-wave5/` (gitignored workspace):
`t10-flat1.json`/`.log`, `t10-mr1.json`/`.log`, `t10-flat2.json`/`.log`,
`t10-mr2.json`/`.log` (the four judged runs, n=24 each), `t10-pw-1.json`/`.log`,
`t10-pw-2.json`/`.log`, `t10-pw-ctrl.json`/`.log` (the three pairwise
comparisons), `t10-pw-pooled.json`/`.log` (the W5-R1 pooled statistic),
`t10-run.sh`/`t10-run.log` (the driver + wall-time log), `t10-summary.py`
(controller's quick summary) and `t10-analyse.py` (this record's source of
truth — every number above is reproducible by running
`python3 t10-analyse.py` from that directory).

## Judge retry (Wave 6)

Task 4 (W6-R4 / W6-R17): judge system prompts gained a JSON-hygiene line
("escape newlines inside strings; no trailing commas"), and
`eval.Judge.completeJSON` now re-asks exactly once, with the decoder's own
diagnostic appended, on a decoder failure — never on a transport error, and
never a second time. One `--judge` run on the 24-question set, standard path
(dispatch off, no long-context overlay — a plain judge-retry measurement, not
a re-run of §4's flat/map_reduce A/B).

**Comparability caveat.** The same hygiene-line change edited all four judge
system prompts (`internal/prompts/eval_judge.go`). Judge **scores** (not just
retry/failure counts) from Wave 6 onward are therefore not strictly
byte-comparable to any pre-Wave-6 judge run on this fixture — including §2's
and §4's flat/map_reduce judged runs and the pairwise comparisons that
decided the `chat_longcontext_mode = map_reduce` default. Only the
retry/failure tallies in this section are a like-for-like measurement across
the change.

**Tally definitions** (fixed for this section and any future re-run of it):

- **retries** = `judge_warnings` entries starting with `retry:` (one per
  metric per question where the FIRST decode attempt failed and a retry was
  attempted — counted regardless of whether that retry then succeeded or
  failed, per ruling N7 from the code review's fix round).
- **remaining failures** = `judge_errors` entries containing the string
  `after retry` (a retry was attempted and its own response also failed to
  decode — the only way a metric can still error after this change, aside
  from a transport error on the very first call, which never retries).

### Command (exact)

```bash
.superpowers/sdd/2026-09-07-rag-sota-wave6/run-eval.sh \
  --golden eval/golden/global-synthesis-de.jsonl \
  --production-context --orchestrator-dispatch=false --judge \
  --output .superpowers/sdd/2026-09-07-rag-sota-wave6/t4-out/gs-judge.json
```

Run from the worktree root; binary `eval-wave6`, freshly built by
`run-eval.sh`. `errors = 0` (all 24 questions answered and judged
successfully). Wall time 20 m 38 s. Model stack unchanged from §1–§4
(`jlu/gemma-4-26b-it` answer + all four judges).

### Results

| Metric | Retries (attempted) | Remaining failures (`after retry`) |
|---|---|---|
| faithfulness | 0 | 0 |
| answer_relevance | 0 | 0 |
| context_precision | 0 | 0 |
| coverage | 0 | 0 |
| **Total** | **0** | **0** |

`*_n` this run (24 questions): `faithfulness_n = 24`, `answer_relevance_n =
24`, `context_precision_n = 23`, `coverage_n = 24`.

**No decoder failure occurred on this run at all**, so the retry path was
never exercised (0 retries is a true zero, not a bounded-but-untriggered
count — every one of the **95** judge calls that reached `unmarshalStrict`
(24 questions × 4 metrics = 96 attempted, minus G17's context_precision
call, which failed in transport and never reached the decoder — see below)
parsed on the first attempt). This is consistent with, though not proof of,
the hygiene-line prompt change (W6-R4a) working: Wave 5 §4's four judged
runs on this exact set (before the hygiene line existed) hit **2 decoder
failures in 384 judge calls** (four runs × 24 questions × 4 metrics; mr1
G05, mr2 G09 — see §4's "Judge instrument faults" note and its results
table above), a **≈0.5%** baseline rate across all metrics — equivalently
2 failures in the **96 faithfulness calls** specifically (both failures
were faithfulness; ≈2% *for that one metric*, per §4's `mean faithfulness
(n)` column showing `flat1 (24) / mr1 (23) / flat2 (24) / mr2 (23)`). This
run's 0/95 (all metrics) or 0/24 (faithfulness alone) is within sampling
noise of either baseline and is not itself a significant result — recorded
as the observed count, not claimed as a rate change.

The one `context_precision_n = 23` (not 24) is **not** a retry-path
casualty: the single `judge_errors` entry this run recorded is a
**transport** error (`ai: all 3 attempts failed: ... context deadline
exceeded`, question G17) — the sketch's `if err != nil { return false, err
}` branch, which fires before `unmarshalStrict` ever runs and therefore
never retries. That the transport-error path stayed a single call, with no
`retry:context_precision` warning attached, is itself evidence the "a
transport error must not retry" guarantee (task-4 review N2) holds in
production, not only in the mutation-tested unit test. The 12
`judge_warnings` entries this run recorded are all **pre-existing**
`context_precision` boolean-count tolerance warnings (W4-R1, e.g.
`context_precision: judge returned 9 booleans, expected 10 —
truncated/padded`; the same tolerance also applies to `coverage`, but none
of the 12 are `coverage` this run) — unrelated to W6-R4, present before
this task and unchanged by it.

### Comparison to the Wave-5 baseline

Wave 5 §4 (`t10-flat1/mr1/flat2/mr2`, same 24-question fixture, `--judge`,
pre-W6-R4 binary) recorded, per the table above: flat1 3 warnings/0 errors,
mr1 1 warning/1 error, flat2 3 warnings/0 errors, mr2 1 warning/1 error — 2
of 4 runs each carried exactly one unretried decoder-failure `judge_errors`
entry (mr1 G05, mr2 G09). This Wave-6 run's `judge_errors` count (1, and a
transport error, not a decoder failure) and `judge_warnings` count (12, all
pre-existing boolean-count warnings, 0 retry warnings) are not directly
comparable rates to §4's, since §4 ran `--orchestrator-dispatch=true` with a
`chat_longcontext_mode` overlay (the long-context orchestrator's map-reduce
stage adds 25 extra fast-tier calls per question, none of them judge calls,
so it does not change the judge call count, but the retrieved-context
shape it feeds the judges does differ from the standard-path pool this run
used) — recorded here as the two comparable data points (judge decoder
failure counts, both pre- and post-hygiene-line), not as a controlled A/B.

### Artifacts

`.superpowers/sdd/2026-09-07-rag-sota-wave6/t4-out/gs-judge.json` (full
report, gitignored workspace) and its accompanying run log (background task
`bhzrnaqys` in the controller's session; the earlier attempt in the same
session, task `bebsydnd8`, failed on an unrelated transient build break in
`internal/chat` from another implementer's concurrent, uncommitted work —
not this task's code, and not present in the run recorded above).
