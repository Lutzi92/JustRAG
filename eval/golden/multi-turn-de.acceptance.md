# `multi-turn-de.jsonl` acceptance record

- **Date:** 2026-09-05
- **Commit:** `1cd19c8` (`feat/rag-sota-wave2`, tip of Task 3 — Task 4's own
  commit lands on top of this record)
- **Model stack:** `jlu/gemma-4-26b-it` (answer/CRAG-grader/enumeration
  model, visible in the run logs), `jlu/jina-rerank` (reranker, visible in
  the run logs), qwen3-embedding-8b / 4096-dim embedder (KB `PPM-Eval`'s
  configured embedder per `docs/retrieval.md` and
  `project_model_stack.md` — not separately logged by `cmd/eval`, since
  embedding calls don't emit a `model` field the same way completions do)
- **Fixture:** `eval/golden/multi-turn-de.jsonl`, 18 conversations
  (`MT01`–`MT18`), 45 turns, KB `83262307-3a1b-49bc-bd08-3b925a868a92`
  ("PPM-Eval"). Composition and provenance: `eval/golden/README.md`
  §"Multi-turn set".

## Commands run

```bash
# (a) baseline — chat_condense_keep_raw_enabled forced OFF
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/multi-turn-de.jsonl --production-context \
  --keep-raw off --output .superpowers/sdd/2026-09-05-rag-sota-wave2/mt-off.json

# (b) candidate — forced ON, diffed against (a)
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/multi-turn-de.jsonl --production-context \
  --keep-raw on --output .superpowers/sdd/2026-09-05-rag-sota-wave2/mt-on.json \
  --baseline .superpowers/sdd/2026-09-05-rag-sota-wave2/mt-off.json

# (c) repeat of (a) — same flag value, to measure run-to-run noise
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/multi-turn-de.jsonl --production-context \
  --keep-raw off --output .superpowers/sdd/2026-09-05-rag-sota-wave2/mt-off2.json \
  --baseline .superpowers/sdd/2026-09-05-rag-sota-wave2/mt-off.json
```

**Note on the literal command in the task-4 brief:** the brief's validation
command uses a `../eval/golden/...` golden path (correct when `cmd/eval` is
invoked with cwd `go-backend/`, e.g. `go run ./cmd/eval --golden
../eval/golden/...`). `run-eval.sh` builds the binary via `(cd go-backend
&& go build -o "../$W/eval-wave2" ./cmd/eval)` but then runs
`"$W/eval-wave2" "$@"` **without** changing directory, so the binary's cwd
stays the worktree root when the script itself is invoked from there. The
golden path was adjusted to `eval/golden/multi-turn-de.jsonl` (relative to
the worktree root, matching `$W`'s own convention) for all three runs
above — the fixture and loader are unaffected, this is purely a path
convention observed empirically (`../eval/golden/...` from the worktree
root 404s: `stat ../eval/golden/multi-turn-de.jsonl: no such file or
directory`, and `eval/golden/...` from the same cwd reaches the file and
loads all 18 rows / 45 turns cleanly).

## Loader validation

All three runs report `questions = 45`, `errors = 0` — the fixture loads
cleanly (18 conversations × `ExpandTurns` → 45 per-turn `Question`s), no
loader/fixture bug.

## Results — `turn_kind_aggregates` (k=10)

| kind | n | recall off | recall on | recall off2 | MRR off | MRR on | MRR off2 |
|---|---|---|---|---|---|---|---|
| `corpus` | 18 | 0.889 | 0.889 | 0.889 | 0.889 | 0.889 | 0.889 |
| `pronoun_ref` | 12 | 0.833 | 0.806 | 0.806 | 0.833 | 0.767 | 0.833 |
| `topic_shift` | 6 | 0.748 | 0.748 | 0.748 | 1.000 | 1.000 | 1.000 |
| `answer_ref` | 6 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 |
| `post_abstain` | 3 | 0.833 | 0.833 | 0.833 | 1.000 | 1.000 | 1.000 |
| **overall** | 45 | 0.866 | 0.859 | 0.859 | 0.911 | 0.893 | 0.911 |

(nDCG, for completeness: overall off 0.904 / on 0.890 / off2 0.909;
`pronoun_ref` off 0.815 / on 0.765 / off2 0.833; every other kind matches
its recall pattern above — identical across all three runs.)

Every kind except `pronoun_ref` is **byte-for-byte identical across all
three runs**, on and off. That is expected for `corpus` (the opening turn
of every conversation has no history — nothing to condense, so
`--keep-raw` cannot affect it) and is the noise-band signal for the
follow-up kinds: `topic_shift`, `answer_ref`, and `post_abstain#t2` all
build their retrieval/lookup query the same way regardless of
`--keep-raw` (`answer_ref` bypasses condensation entirely per ruling
W2-R3 — it returns the prior turn's `answer_sources` directly, hence
`condensed_query = null` on every `answer_ref` row in both reports, see
below), and evidently the LLM-condensed rewrite for `topic_shift`/
`post_abstain` follow-ups is either identical or immaterial to their
already-distinct/well-formed retrieval query across sampling draws.
`pronoun_ref` is the only kind whose condensed rewrite text plausibly
varies between draws (short, ambiguous follow-ups where the raw-vs-rewrite
lane genuinely changes what reaches retrieval) — and that is exactly the
kind the decision rule below is testing.

## Decision rule — `pronoun_ref` recall

Deltas below are computed from the raw JSON floats
(`turn_kind_aggregates.pronoun_ref` in each report: off `mean_recall =
0.8333333333333334`, on `= 0.8055555555555557`, off2 `=
0.8055555555555557`; `mrr` off `= 0.8333333333333334`, on `=
0.7666666666666666`, off2 `= 0.8333333333333334`), not from the rounded
0.833/0.806 table cells above — rounding first and subtracting after
would silently drop the last significant digit on a 12-question bucket.

- `on − off` = 0.8055555555555557 − 0.8333333333333334 = **−0.0278**
  (−2.8 pp; keep-raw ON is *worse*, not better, on this run pair)
- noise band `|off2 − off|` = |0.8055555555555557 − 0.8333333333333334|
  = **0.0278** (2.8 pp)
- rule: flip `chat_condense_keep_raw_enabled` ON in Task 9 only if
  `(on − off) > |off2 − off|` on `pronoun_ref` recall **and** no kind
  loses more than the noise band.

`(on − off) = −0.0278` is not merely below the noise band, it is negative
— keep-raw ON underperforms keep-raw OFF on the one kind it should help,
by (to four decimal places) exactly the same magnitude as the OFF-vs-OFF
noise. The first half of the rule fails outright, so the second half (no
kind losing more than the noise band) doesn't change the verdict, though
it's worth recording: every kind's `on`-vs-`off` loss is ≤ the noise band
already (`pronoun_ref` loses exactly 2.8 pp = the noise band itself;
every other kind loses 0.0 pp).

**Verdict: do NOT flip `chat_condense_keep_raw_enabled` to ON in Task 9.**
Keep the default OFF. On this 12-question `pronoun_ref` sample, raw-query
retrieval (the current default — `RawQueryForRetrieval` folds the raw
follow-up text back in alongside the condensed rewrite) performs at least
as well as condensed-only retrieval, and the observed "improvement" bar
the rule sets is not cleared in either direction with recall alone —
`on` is strictly worse. MRR tells the same story more sharply: `pronoun_ref`
MRR off 0.8333333333333334 → on 0.7666666666666666 (**−0.0667**, −6.7 pp)
vs. off2 0.8333333333333334 (0.0 pp noise) — a real, noise-exceeding
*regression* under keep-raw ON, reinforcing the recall verdict rather than
complicating it.

## `condensed_query` — all six `answer_ref` turns

Identical in both the `off` and `on` reports (`condensed_query: null` in
both — confirms ruling W2-R3's "answer_ref bypasses condensation, returns
the prior turn's sources verbatim" path fired for all six, in both
configurations):

| id | question | condensed_query |
|---|---|---|
| `MT01#t3` | "Kannst du das als Tabelle darstellen?" | `null` |
| `MT02#t3` | "Fass das kürzer zusammen." | `null` |
| `MT03#t3` | "Als Stichpunkte bitte." | `null` |
| `MT04#t3` | "Kannst du das übersetzen?" | `null` |
| `MT05#t3` | "Das als Tabelle bitte." | `null` |
| `MT06#t3` | "Fass das kürzer zusammen." | `null` |

All six scored `recall_at_k = 1.000` in every run (their ground truth is
the prior turn's `answer_sources`, and the adapter returns exactly that
set as a synthetic 1.0-scored hit — a tautological but correct check that
the bypass path is wired).

## `condensed_query` — every turn with recall 0

**Baseline (`mt-off.json`):**

| id | kind | question | condensed_query | must_cite |
|---|---|---|---|---|
| `MT02#t2` | `pronoun_ref` | "Seit wann läuft es?" | "Seit wann läuft das Planungsprojekt 'Neue Wege mit KI'?" | `Planungsprojekt Neue Wege mit KI.md` |
| `MT10#t1` | `corpus` | "Wann fand der erste dokumentierte Workshop des Projekts 'Neue Wege mit KI' statt?" | (same — no history, no condensation) | `2025-07-14 Workshop 14.07.2025.md` |
| `MT10#t2` | `pronoun_ref` | "Wer war daran beteiligt?" | "Wer war am ersten dokumentierten Workshop des Projekts 'Neue Wege mit KI' beteiligt?" | `2025-07-14 Workshop 14.07.2025.md` |
| `MT18#t1` | `corpus` | "Welches Gesamtbudget wurde für das Planungsprojekt 'Neue Wege mit KI' bis zum Projektende bewilligt?" | (same — no history) | `Planungsprojekt Neue Wege mit KI.md` |

**Candidate (`mt-on.json`):**

| id | kind | question | condensed_query | must_cite |
|---|---|---|---|---|
| `MT10#t1` | `corpus` | "Wann fand der erste dokumentierte Workshop des Projekts 'Neue Wege mit KI' statt?" | (same — no history) | `2025-07-14 Workshop 14.07.2025.md` |
| `MT10#t2` | `pronoun_ref` | "Wer war daran beteiligt?" | "Wer war am ersten dokumentierten Workshop des Projekts 'Neue Wege mit KI' beteiligt?" | `2025-07-14 Workshop 14.07.2025.md` |
| `MT11#t2` | `pronoun_ref` | "In welchem Zeitraum läuft es?" | "In welchem Zeitraum läuft das Planungsprojekt 'Neue Wege mit KI'?" | `Planungsprojekt Neue Wege mit KI.md` |
| `MT18#t1` | `corpus` | "Welches Gesamtbudget wurde für das Planungsprojekt 'Neue Wege mit KI' bis zum Projektende bewilligt?" | (same — no history) | `Planungsprojekt Neue Wege mit KI.md` |

Observations on the recall-0 set:

- **The condensation itself reads correctly** in every case: "Seit wann
  läuft es?" / "Wer war daran beteiligt?" / "In welchem Zeitraum läuft
  es?" all resolve their pronoun/ellipsis to the right subject
  ("Planungsprojekt 'Neue Wege mit KI'" / "der erste dokumentierte
  Workshop des Projekts 'Neue Wege mit KI'"). The recall-0 result is a
  **retrieval-depth miss** (the correctly-condensed query's target file
  didn't land in the top-10), not a condensation failure — consistent
  with `2025-07-14 Workshop 14.07.2025.md` being a narrow, low-traffic
  page (a single dated workshop note) competing against much larger
  "Neue Wege mit KI" project pages for the same rewritten query terms.
- **`MT10#t1` and `MT18#t1` are opening `corpus` turns with no history**
  — they score 0 independent of `--keep-raw` (no condensation runs on the
  first turn), which is why they persist across every run and every kind
  reads as retrieval difficulty inherent to those two source questions,
  not the multi-turn machinery. `MT18#t1` is a `post_abstain` opener by
  design (deliberately unanswerable; low recall there is somewhat expected
  since the "correct" file doesn't actually satisfy the query, it's just
  the page that *should* have been consulted) — `MT10#t1`, however, is an
  ordinary `corpus` question the standard single-turn set doesn't cover
  (it isn't drawn verbatim from `production-ppm-2026-08.jsonl`), so it's
  a genuine retrieval-depth data point worth a look outside this task's
  scope, not something to patch inside a golden-set fixture.
- **`MT02#t2` recovers under keep-raw ON/off2** (baseline-only miss): 0
  in `off`, but not listed as a miss in `on` or `off2` — one example of
  the run-to-run sampling variance discussed above landing in the miss
  set rather than just moving the aggregate.
- **`MT11#t2` is a keep-raw-ON-only regression**: it recalls correctly
  under `off` and `off2` but misses under `on` — direct evidence
  supporting the decision-rule verdict (raw-lane retrieval helping, not
  hurting, this pronoun_ref case).

## Anomaly: `--baseline` regression gate on `complex_reasoning`

Both diffed runs ((b) `on` and (c) `off2`, same baseline (a)) trip
`run-eval.sh`'s regression gate on the `complex_reasoning` **route**
(`query_type`, not `turn_kind`): recall 0.917 → 0.861 (−5.6 pp),
`eval-exit=3`. Because this identical drop appears in **both** the
keep-raw-ON run and the keep-raw-OFF (noise) rerun, it is not caused by
`--keep-raw` — it's run-to-run non-determinism in the CRAG grader /
enumeration-extraction LLM calls (temperature > 0 on those calls, visible
in the run logs: `rag.crag.decide`, `rag.enumeration.extracted`) that
happens to land the same way in both non-baseline draws for this small
6-question `complex_reasoning` bucket. Not a fixture defect and not
actionable for this task; flagging as a general observation that
`--baseline`'s regression gate is noise-sensitive on small per-route
buckets, same phenomenon the `pronoun_ref` noise-band analysis above
exists to guard against for `turn_kind`.

## Reachability

The dev stack (`docker compose`, `justrag-*` containers) was up and
reachable throughout; `JWT_SECRET` was set via `run-eval.sh`'s exported
env. All three runs completed with `errors = 0`.
