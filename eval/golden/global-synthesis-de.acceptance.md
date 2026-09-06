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
