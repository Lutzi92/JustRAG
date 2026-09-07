# `cert-recency-de.jsonl` acceptance record

- **Date:** 2026-09-06
- **Commit:** `6e1e274` (`feat/rag-sota-wave2`, tip at seeding time — Task 8's
  own commit lands on top of this record)
- **Model stack:** `jlu/gemma-4-26b-it` (answer / CRAG-grader / plan-execute
  model, visible in the run logs), `jlu/jina-rerank` (reranker, visible in
  the run logs), qwen3-embedding-8b / 4096-dim embedder (deployment default
  per `project_model_stack.md`; the KB itself has no per-KB model
  overrides — `chatModel`/`embeddingModel`/`rerankModel` are all `null` on
  `GET /api/kb/{id}`, and embedding calls don't emit a `model` field in the
  run logs the way completions do, so it isn't separately confirmed there).
- **Fixture:** `eval/golden/cert-recency-de.jsonl`, 25 questions, KB
  `cccdee72-7489-4867-a51d-d0894b2e4d78` ("CERT Fixtures"), seeded via
  `eval/fixtures/seed-cert.sh` against the local dev compose stack
  (`http://localhost:3001` via nginx). Corpus and question design:
  `eval/golden/README.md` §"CERT recency set (Wave 2 Task 8)".
- **Site config at run time:** `chat_recency_listing_enabled` and
  `chat_date_awareness_enabled` are both default-ON in this deployment (no
  override needed); `recency_boost_enabled` was flipped per run via the
  new `--recency-boost on|off` overlay flag (`go-backend/cmd/eval/main.go`).
  `--orchestrator-dispatch` was left at its default (`true`) for all three
  runs, i.e. every question routes through the real production
  orchestrator-selection predicate, not just the standard path.

## A correction made during seeding (verified against live behavior)

The task brief's stated fact — `files.name = SafeNameSegment(title) +
".txt"` for `POST /api/kb/{id}/text` sources — does **not** hold in this
codebase. Reading `internal/files/http_ingest.go`'s `AddTextSource`
confirms `sanitizeTitle`/`SafeNameSegment` is used only to build the
on-disk **storage path** (step 3); the `files.name` DB column written in
step 5 is `req.Title`, verbatim, no extension appended. A first seeding
attempt with `.txt`-suffixed expected names failed the seed script's own
name-verification step for all 40 rows (loudly, as designed — see "Verify
the file names landed as expected" in the brief), which is what surfaced
the discrepancy before anything got silently backdated against the wrong
rows. `eval/fixtures/seed-cert.sh` and `eval/golden/cert-recency-de.jsonl`
were both corrected to expect the raw title (no `.txt`) as `files.name`,
re-verified, and the acceptance run below is against the corrected fixture.

## Commands run

```bash
# Seed (idempotent; --restamp re-runs only the backdating UPDATEs)
JUSTRAG_URL=http://localhost:3001 JUSTRAG_ADMIN_USER=admin \
JUSTRAG_ADMIN_PASSWORD=<ADMIN_PASSWORD> ./eval/fixtures/seed-cert.sh

# (a) recency boost off — live default
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl --production-context \
  --recency-boost off \
  --output .superpowers/sdd/2026-09-05-rag-sota-wave2/task8-out/cert-off1.json

# (b) recency boost on
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl --production-context \
  --recency-boost on \
  --output .superpowers/sdd/2026-09-05-rag-sota-wave2/task8-out/cert-on.json

# (c) repeat of (a) — same flag value, to measure run-to-run noise
bash .superpowers/sdd/2026-09-05-rag-sota-wave2/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl --production-context \
  --recency-boost off \
  --output .superpowers/sdd/2026-09-05-rag-sota-wave2/task8-out/cert-off2.json
```

All three runs: `questions = 25`, `errors = 0` — the fixture and loader
work cleanly end to end.

## Results — overall and per-route (k=10)

| Run | overall recall | overall MRR | overall nDCG | lookup recall | lookup MRR | enumeration recall | enumeration MRR |
|---|---|---|---|---|---|---|---|
| (a) off1 | 0.626 | 0.701 | 0.714 | 0.602 | 0.661 | 0.800 | 1.000 |
| (b) on   | 0.704 | 0.801 | 0.807 | 0.691 | 0.774 | 0.800 | 1.000 |
| (c) off2 | 0.542 | 0.648 | 0.651 | 0.507 | 0.600 | 0.800 | 1.000 |

The six recency-listing questions (`cert-r01`..`cert-r06`) carry up to 26
must-cite files each against a k=10 retrieval cap, so their own recall is
structurally bounded well below 1.0 regardless of retrieval quality — this
caps the overall-recall figures above too, since those six questions are
part of the n=25 overall average.

`enumeration` (n=3, the cross-advisory questions) is identical across all
three runs — those don't involve a NEU/UPDATE tie so the recency prior has
nothing to break there, and the run-to-run noise happens to land on
exactly the same 3 questions all three times (small-n coincidence, not a
claim of zero variance).

**Noise band:** the two nominally-identical "off" runs differ by 0.084
overall recall / 0.053 MRR (lookup: 0.095 recall / 0.061 MRR) — driven
almost entirely by the LLM query-type classifier's non-deterministic calls
under `--orchestrator-dispatch` (see "Orchestrator-dispatch interaction"
below), not by anything specific to `recency_boost_enabled`. The "on" run
sits **above both** off runs on every recall/MRR/nDCG column (overall and
lookup), i.e. outside the observed noise band in the expected direction,
but with n=25 questions and a single noise sample this is directional
evidence, not a statistically tight bound.

## NEU/UPDATE pair ranking (`cert-p01`..`cert-p08`) — does UPDATE outrank NEU?

For each pair, which of the NEU/UPDATE files (if either) is the
top-ranked (`retrieved[0]`) result for that pair's lookup question:

| Question | WID id | Product | off1 | on | off2 |
|---|---|---|---|---|---|
| cert-p01 | WID-SEC-2026-0104 | Fortinet FortiOS | **UPDATE** (NEU absent from top-10) | **UPDATE** (NEU absent) | NEU |
| cert-p02 | WID-SEC-2026-0106 | Ivanti Connect Secure | **UPDATE** (NEU absent) | **UPDATE** (NEU absent) | NEU |
| cert-p03 | WID-SEC-2026-0107 | Cisco IOS XE | NEU | **UPDATE** (NEU absent) | NEU |
| cert-p04 | WID-SEC-2026-0108 | VMware ESXi | NEU | **UPDATE** (NEU absent) | NEU |
| cert-p05 | WID-SEC-2026-0109 | OpenSSL | NEU | NEU | NEU |
| cert-p06 | WID-SEC-2026-0113 | Citrix NetScaler | NEU | **UPDATE** (NEU absent) | NEU |
| cert-p07 | WID-SEC-2026-0119 | Atlassian Confluence | **UPDATE** (NEU absent) | **UPDATE** (NEU absent) | **UPDATE** (NEU absent) |
| cert-p08 | WID-SEC-2026-0123 | GitLab | **UPDATE** (NEU absent) | **UPDATE** (NEU absent) | **UPDATE** (NEU absent) |
| **UPDATE-outranks-NEU count** | | | **4/8** | **7/8** | **2/8** |

In every case where both files appeared in the same top-10, only one of
the pair ever did — MMR diversity plus the near-duplicate content of a
NEU/UPDATE pair means the reranker+MMR stage keeps at most one; this
fixture therefore measures **which one survives**, not a within-pair rank
gap. `recency_boost_enabled=on` raises the UPDATE win rate from 4/8 (off1)
to 7/8 — the only pair that stays NEU-only "on" is WID-SEC-2026-0109
(OpenSSL, NEU 100 days old vs. UPDATE 20 days old — both are old enough
that the exponential-decay recency prior has mostly decayed for both, so
it has little differential effect on that particular pair). 7/8 is above
both off runs (4/8, 2/8), consistent with the recall/MRR delta above.

## Recency-listing questions (`cert-r01`..`cert-r06`)

The listing addendum itself is not a retrieval-recall metric (per the
brief) — recorded here is (a) whether `rag.recency_listing.fired` appears
in the log for that question at all, and (b) how many of the 9
default-7-day-window files (5 NEU + 4 UPDATE) reached `FinalChunks`
(top-10, post-rerank/MMR) when it did fire.

| Question | Window (days) | Marker arm | off1 fired? | on fired? | off2 fired? | window-file coverage in FinalChunks (of 9) |
|---|---|---|---|---|---|---|
| cert-r01 | 7 (default) | yes ("neu") | — (log not captured) | yes | yes | on: 4/9 (2 NEU); off2: 3/9 (2 NEU) |
| cert-r02 | 3 (explicit) | yes ("neu") | — | **no** (misrouted to `plan_execute`) | **no** (misrouted to `plan_execute`) | n/a when not fired |
| cert-r03 | 7 (default) | yes ("Neues") | — | yes | yes | on: 3/9 (2 NEU); off2: 3/9 (2 NEU) |
| cert-r04 | 3 (explicit) | no | — | yes | yes | on: 3/9 (1 NEU); off2: 3/9 (1 NEU) |
| cert-r05 | 6 (explicit) | no | — | yes | yes | on: 6/9 (2 NEU); off2: 6/9 (2 NEU) |
| cert-r06 | 14 (explicit) | no | — | **no** (misrouted to `plan_execute`) | **no** (misrouted to `plan_execute`) | n/a when not fired |

(off1's log stream wasn't redirected to a file — only the last ~2KB were
captured in the terminal tail — so its per-question fired/not-fired list
isn't reconstructable after the fact; off1's per-question `FinalChunks`
membership was captured from the JSON report regardless, since that part
doesn't depend on the log. `on` and `off2` logs were both captured in
full.)

### Orchestrator-dispatch interaction (real finding, not a bug)

`cert-r02` and `cert-r06` were classified `complex_reasoning` by the LLM
query-type classifier in both the `on` and `off2` runs and dispatched to
`plan_execute` instead of the standard path
(`eval.orchestrator_dispatch` log entries, e.g. `"question_id":"cert-r06",
"query_type":"complex_reasoning","orchestrator":"plan_execute",
"dispatch_reason":"complex_reasoning_plan_execute_gate"`). The recency
lister (`chat.RecencyLister`) is wired only into the standard
`PrepareChatContext` params in this eval's adapter, exactly mirroring
production (`chat_recency_listing_*` is documented as "Standard
`PrepareChatContext` path only" in `CLAUDE.md`) — so when
orchestrator-dispatch routes a recency-listing-shaped question to
`plan_execute`/`supervisor`/`agentic`, the listing mechanism does not fire
for that turn at all, in production exactly as in this eval. This is the
single largest source of the "noise band" above: which questions get
reclassified as `complex_reasoning` varies from run to run (an LLM call,
not deterministic), and a misrouted recency-listing question loses both
its window-scoped retrieval and its listing addendum for that turn. This
is a pre-existing characteristic of the production dispatch predicate, not
something introduced by this task's fixture or wiring.

### Log line proving the recency listing fired under `cmd/eval`

```
{"time":"2026-09-06T00:29:11.782354795+02:00","level":"INFO","msg":"rag.recency_listing.fired","window_days":7,"since":"2026-08-30","files_listed":29,"name_marker_extra":21,"truncated":false}
```

(from the `on` run's log, `cert-r01`; `files_listed=29` = the 9-file
7-day window plus 21 marker-arm NEU files outside it, one short of the
theoretical 30 (9 window + 21 marker) — see "Off-by-one window-boundary
note" below.)

### Off-by-one window-boundary note

The manifest's `days_ago` integers are exact-hour offsets applied at seed
time (`now() - make_interval(days => N)`), while the runtime's window
cutoff is a **midnight-aligned** calendar-day boundary in
`chat_date_timezone` (`Europe/Berlin` by default). A file backdated to
"exactly 7 days ago" at the seeding timestamp can land on either side of
the "last 7 calendar days" cutoff depending on what time of day seeding
ran relative to local midnight. Observed effect: `files_listed=29`
instead of the naively-expected 30 (9 window + 21 marker) for the
default-window questions — one file sits right at that boundary. This is
expected/documented in the classifier's own contract (`recency_listing.go`
computes the cutoff at local midnight, not "exactly N×24h ago") and not a
bug in the fixture or the mechanism; it does not affect the qualitative
conclusions above (the window set is off by at most one file at the edge).

## Tabular router / routing accuracy (context, not this task's subject)

`tabular_router_fire_rate = 0.000` on all three runs, as expected (no
spreadsheet data in this KB). Query-type routing accuracy vs. the golden
`query_type` label was 0.760 (on) / 0.720 (off2) — driven by the same
LLM-classifier non-determinism noted above (a `lookup`-labeled question
occasionally gets classified `complex_reasoning`, or `enumeration`
depending on phrasing); this is a pre-existing characteristic of
`ClassifyQueryTypeForEval`/`ai.ClassifyQueryComplexity`, unrelated to
`recency_boost_enabled`.

## Controller-requested rerun: orchestrator dispatch forced off (standard path)

The dispatch-on table above is confounded: `--orchestrator-dispatch`
defaults `true`, so each question's route (standard vs.
`plan_execute`/`supervisor`/`agentic`) depends on an LLM query-complexity
classification call that is not deterministic run-to-run, and
recency-listing fires only on the standard path. Rerun with
`--orchestrator-dispatch=false` to force every question onto the standard
path deterministically, isolating both recency mechanisms from that
confound. Re-stamped first (`--restamp`, since seeding happened on
2026-09-05 and it was 2026-09-06 by rerun time — see `manifest.tsv`
`days_ago` values, which are relative to `now()` at stamping time, not a
fixed calendar date).

**A wiring gap found and fixed during this rerun:** the first attempt at
this table showed **zero** `rag.recency_listing.fired` log lines across
all three `--orchestrator-dispatch=false` runs. Cause:
`cmd/eval`'s `--orchestrator-dispatch=false` branch (`main.go`) was built
as a "router-free **and lister-free** by design" byte-stable comparison
branch — it never called `eval.WithRecencyLister` at all, so
`ProductionContextAdapter.recencyLister` was `nil` and
`applyRecencyListing` no-opped on every question, silently. Since
`TabularRouter` predates this task and is deliberately excluded there to
preserve byte-stable diffs against pre-existing reports, `RecencyLister`
had been added to the same exclusion by analogy — but `RecencyLister` was
introduced by *this* task, so there is no pre-existing byte-stable report
for it to preserve compatibility with, and excluding it defeated the only
realistic reason to combine `--orchestrator-dispatch=false` with this
fixture in the first place. Fixed by wiring `eval.WithRecencyLister` into
that branch too (`TabularRouter` stays excluded — that exclusion is
unrelated to this task and unaffected). All three runs below are from the
fixed binary; `rag.recency_listing.fired` appears 6/6 times (once per
`cert-r01`..`cert-r06`) in every run.

### Results — overall and per-route (k=10), standard path forced

| Run | overall recall | overall MRR | overall nDCG | lookup recall | lookup MRR | enumeration recall | enumeration MRR |
|---|---|---|---|---|---|---|---|
| (a) nd-off1 | 0.651 | 0.680 | 0.702 | 0.631 | 0.636 | 0.800 | 1.000 |
| (b) nd-on   | 0.696 | 0.767 | 0.778 | 0.682 | 0.735 | 0.800 | 1.000 |
| (c) nd-off2 | 0.611 | 0.667 | 0.682 | 0.585 | 0.621 | 0.800 | 1.000 |

**Noise band** (nd-off1 vs. nd-off2, both `recency_boost_enabled=false`,
standard path forced): 0.040 overall recall / 0.013 MRR (lookup: 0.046
recall / 0.015 MRR). This is smaller than the dispatch-on noise band above
(0.084 / 0.053) but **not zero** — some residual non-determinism remains
even with orchestrator-dispatch and the LLM query-type classifier taken
out of the loop, most plausibly the CRAG grader's own LLM call (still
part of the standard path) rather than anything specific to
`recency_boost_enabled`. `nd-on` sits above both `nd-off` runs on every
column, outside this smaller noise band.

### NEU/UPDATE pair ranking, standard path forced

| Question | WID id | nd-off1 | nd-on | nd-off2 |
|---|---|---|---|---|
| cert-p01 | 0104 | NEU | NEU | NEU |
| cert-p02 | 0106 | **UPDATE** | **UPDATE** | NEU |
| cert-p03 | 0107 | NEU | **UPDATE** | NEU |
| cert-p04 | 0108 | NEU | NEU | NEU |
| cert-p05 | 0109 | NEU | NEU | NEU |
| cert-p06 | 0113 | NEU | **UPDATE** | NEU |
| cert-p07 | 0119 | **UPDATE** | **UPDATE** | **UPDATE** |
| cert-p08 | 0123 | **UPDATE** | **UPDATE** | **UPDATE** |
| **UPDATE-outranks-NEU count** | | **3/8** | **5/8** | **2/8** |

`nd-on` (5/8) is above both off runs (3/8, 2/8) — the same direction as
the dispatch-on table, a smaller but still-real effect once the
dispatch confound is removed.

### Recency-listing questions, standard path forced (window coverage of 9)

| Question | Window (days) | Marker | nd-off1 window-file coverage | nd-on | nd-off2 |
|---|---|---|---|---|---|
| cert-r01 | 7 | yes | 3/9 (2 NEU) | 4/9 (2 NEU) | 3/9 (2 NEU) |
| cert-r02 | 3 | yes | 1/9 (1 NEU) | 1/9 (0 NEU) | 1/9 (1 NEU) |
| cert-r03 | 7 | yes | 3/9 (2 NEU) | 3/9 (2 NEU) | 3/9 (2 NEU) |
| cert-r04 | 3 | no | 3/9 (1 NEU) | 3/9 (1 NEU) | 3/9 (1 NEU) |
| cert-r05 | 6 | no | 6/9 (2 NEU) | 6/9 (2 NEU) | 6/9 (2 NEU) |
| cert-r06 | 14 | no | 8/9 (4 NEU) | 8/9 (5 NEU) | 8/9 (4 NEU) |

All 6 fired in all 3 runs (log line below). `cert-r04`/`r05`/`r06`
(window-only, no marker arm) are identical or near-identical across all
three runs — expected, since `recency_boost_enabled` only reorders an
already-small, window-scoped candidate set and MMR/rerank mostly land the
same files regardless. `cert-r06`'s `must_cite` hit count (not shown,
see JSON reports) actually improves under `recency_boost=on` (5/5 NEU
window files reached `FinalChunks` vs. 4/5 for both off runs) — the boost
pulling one additional window file above the MMR/top-10 cutoff.

Log line confirming firing under the fixed binary:

```
{"time":"2026-09-06T00:55:06.18958403+02:00","level":"INFO","msg":"rag.recency_listing.fired","window_days":7,"since":"2026-08-30","files_listed":29,"name_marker_extra":21,"truncated":false}
```

## Conclusion

**The standard-path-forced table above is the one the conclusions below
rest on** — it isolates both mechanisms from the orchestrator-dispatch
confound the controller flagged; the dispatch-on table earlier in this
document is kept as the "production-like" view (what the deployment
actually does today, dispatch noise included) but is not used for causal
claims about either flag.

1. **Recency-listing** (`chat_recency_listing_enabled`): fires
   deterministically for every recency-listing question that reaches the
   standard path, correctly resolves explicit windows (3/6/7/14 days) and
   the name-marker arm ("neu"/"Neues" → all NEU files, window-only
   phrasings → just the window's NEU files), logs `rag.recency_listing.fired`
   with the expected window/marker counts, and window-scopes retrieval
   (`SearchOptions.CreatedAfter`/`FileIDs`) as documented. Under
   production-like dispatch, it does **not** fire when orchestrator-dispatch
   routes the same question elsewhere — a real, pre-existing production
   interaction this fixture surfaces rather than one it introduces (see
   the dispatch-on table's "Orchestrator-dispatch interaction" note).
2. **Recency boost** (`recency_boost_enabled`), standard path forced: raises
   the UPDATE-over-NEU win rate from 3/8 (off1) and 2/8 (off2) to 5/8 (on),
   and raises overall/lookup recall+MRR above both off runs (0.696/0.767 vs.
   0.651/0.680 and 0.611/0.667) — directionally as intended, and outside the
   0.040/0.013 noise band measured between the two off runs, though still a
   modest effect on n=25 questions. This is a **weaker** effect than the
   dispatch-on table suggested (4/8→7/8, +0.078 recall) — some of that
   larger apparent effect was the dispatch confound itself (which orchestrator
   a question lands on affects its recall independently of
   `recency_boost_enabled`), not the recency prior alone. The standard-path-
   forced numbers are the more trustworthy estimate of the recency prior's
   isolated effect on this fixture. Both `.local.jsonl` runs and the JSON
   reports live under `.superpowers/sdd/2026-09-05-rag-sota-wave2/task8-out/`
   (gitignored, not committed).

---

# Conflict surfacing (Wave 5)

- **Date:** 2026-09-06
- **Branch/base:** `feat/rag-sota-wave5` (Task 3 shipped the mechanism; this
  section is the W5-R7 measurement it still owed)
- **Feature:** `chat_conflict_surfacing_enabled` (default **OFF**, per-KB).
  One structured fast-tier call per turn over the turn's own numbered
  sources, capped at `chat_conflict_max_chunks` (12), returning
  `{claim, sourceA, sourceB, kind, newer}`; direction (`newer`) is decided
  from each source's date line only.
- **Model:** `jlu-internal/gemma-4-26b-it-bulk` (the resolved fast tier —
  `chat_conflict_model` unset, `model_tier_fast` in force), timeout 6000 ms,
  max chunks 12. No `site_configs` row exists for any `chat_conflict_*` key
  (verified: `select * from site_configs where key like 'chat_conflict%'`
  returns 0 rows), so the flag was at its code default and the per-run
  overlay is the only thing that turned it on.
- **Dates:** the pair files carry backdated `files.created_at` and NULL
  `published_at`; the conflict date line falls back to `created_at`.

## Setup — exact commands

```bash
# 1. Re-stamp the fixture (the backdating is relative to now(); W2-R8) and
#    regenerate the runnable golden set. Sources the main checkout's .env and
#    maps ADMIN_PASSWORD -> JUSTRAG_ADMIN_PASSWORD; never echoes it.
bash .superpowers/sdd/2026-09-06-rag-sota-wave5/t5-cert.sh

# 2. The four runs (standard path only, no judge).
bash .superpowers/sdd/2026-09-06-rag-sota-wave5/t5-runs.sh
#   == run-eval.sh --golden eval/golden/cert-recency-de.local.jsonl \
#        --production-context --orchestrator-dispatch=false \
#        --conflict-surfacing on   --output .../t5-out/t5-cert-on.json
#   == ... --conflict-surfacing off --output .../t5-out/t5-cert-off.json
#   == ... --conflict-surfacing on  --output .../t5-out/t5-cert-on2.json   (repeat, noise band)
#   == run-eval.sh --golden eval/golden/production-ppm-2026-08.jsonl \
#        --production-context --orchestrator-dispatch=false \
#        --conflict-surfacing on   --output .../t5-out/t5-ppm-on.json

# 3. Every number below is reproduced by:
python3 .superpowers/sdd/2026-09-06-rag-sota-wave5/t5-analyse.py
```

`--orchestrator-dispatch=false` per W5-R12: conflict surfacing is a
standard-`PrepareChatContext` feature, and dispatch-on runs on this fixture
are dominated by the LLM query-type classifier's run-to-run routing noise
(documented in the Wave-2 section above).

### Re-stamp verification (read-only, before the runs)

All 8 golden pairs carry two distinct `created_at` values with the UPDATE
newer, and `published_at` is NULL throughout:

```
 pairs | update_newer | same_timestamp
-------+--------------+----------------
     8 |            8 |              0
```

e.g. `WID-SEC-2026-0104`: NEU `2026-08-30 20:43:36`, UPDATE
`2026-09-05 20:43:36`.

## Rules (stated before the numbers)

- **assembled set** — the question's `retrieved` array in the JSON report:
  the final chunk set sorted by score and cut at k=10. The detector's own
  window is the turn's `sources` list capped at 12, which the report does
  not carry, so this view slightly **under-counts** what the detector saw.
  Membership is judged on file NAMES.
- **pair** — `cert-p01`..`cert-p08` each target one WID id; the pair's two
  files are `NEU <wid> …` and `UPDATE <wid> …`.
- **flagged (pair)** — the question's `conflicts` array contains an entry
  whose `{fileA,fileB}` is exactly that pair.
- **newer correct** — such an entry has `kind == "superseded"` and the side
  `newer` points at is the UPDATE file.
- **flag rate** — questions with ≥ 1 conflict entry of any shape, over ALL
  questions in the report (errored ones included; there were none).
- **latency** — mean of per-question `latency_ms`.
- **opportunity** — a (question, corpus pair) where BOTH halves are in that
  question's top-10. **hit** — an opportunity the detector reported;
  **directed** — a hit with `kind=superseded` and the UPDATE marked newer.

## Result 1 — the eight pair questions: 0/8, and not because of the detector

| Question | WID | Product | pair in assembled set (`on`) | pair in assembled set (`on2`) | pair flagged | newer=UPDATE |
|---|---|---|---|---|---|---|
| cert-p01 | WID-SEC-2026-0104 | Fortinet FortiOS | NEU only | NEU only | no | – |
| cert-p02 | WID-SEC-2026-0106 | Ivanti Connect Secure | NEU only | UPDATE only | no | – |
| cert-p03 | WID-SEC-2026-0107 | Cisco IOS XE | NEU only | NEU only | no | – |
| cert-p04 | WID-SEC-2026-0108 | VMware ESXi | NEU only | NEU only | no | – |
| cert-p05 | WID-SEC-2026-0109 | OpenSSL | NEU only | NEU only | no | – |
| cert-p06 | WID-SEC-2026-0113 | Citrix NetScaler | NEU only | NEU only | no | – |
| cert-p07 | WID-SEC-2026-0119 | Atlassian Confluence | UPDATE only | UPDATE only | no | – |
| cert-p08 | WID-SEC-2026-0123 | GitLab | UPDATE only | UPDATE only | no | – |
| **totals** | | | **0/8 both present** | **0/8 both present** | **0/8** | **0/8** |

**Both halves of the queried pair are never in the assembled set**, in either
`on` run — so the detector is never given the chance to flag the pair the
question is about. This is not a new observation: the Wave-2 section above
records the same thing ("In every case where both files appeared in the same
top-10, only one of the pair ever did — MMR diversity plus the near-duplicate
content of a NEU/UPDATE pair means the reranker+MMR stage keeps at most one").
The eight pair questions were authored to measure *which half survives*, and
that property makes them structurally unable to measure conflict surfacing.
The literal brief gate (≥ 6/8 pairs flagged with the newer marked) therefore
reads **0/8** — a fact about retrieval on this fixture, not about the pass.

## Result 2 — what the pass actually does on the same 25 questions

The corpus carries **14** NEU/UPDATE pairs (from
`eval/fixtures/cert-advisories/manifest.tsv`); the eight golden questions
target eight of them, but every question's top-10 tends to contain *other*
complete pairs.

| Run | opportunities | hits | directed (UPDATE marked newer) | detection rate |
|---|---|---|---|---|
| cert `on`  | 37 | 12 | 12 | 0.324 |
| cert `on2` | 35 | 10 | 10 | 0.286 |
| cert `off` | 35 | 0 | 0 | 0.000 |

- **Every hit had the direction right** in both `on` runs (12/12 and 10/10).
- **Entry-level precision**: `on` produced 17 conflict entries, `on2` 14.
  All 31 name a genuine NEU/UPDATE pair of the corpus — **zero** entries
  pair two files that are not a real supersession pair. One entry (of 17,
  `on`, `cert-n01`) is a *duplicate* of another entry with the two sources
  swapped and the direction inverted, i.e. the only wrong-direction entry in
  the whole measurement: 16/17 and 14/14 correct.
- **Pairs surfaced anywhere in the run**: 8/14 (`on`) and 9/14 (`on2`)
  distinct corpus pairs were flagged at least once with the UPDATE marked
  newer.
- **`off` is a clean control**: 0 entries, 0 flagged questions, and the
  `conflicts` key absent from every question in the JSON — the override does
  nothing else.

Per-question flag rates:

| Run | flagged | flag rate | with a directed supersession |
|---|---|---|---|
| cert `on`  | 10/25 | 0.400 | 10 |
| cert `on2` |  8/25 | 0.320 |  8 |
| cert `off` |  0/25 | 0.000 |  0 |

The two same-flag runs differ by 2 questions (0.400 vs 0.320), which is the
noise band for this rate at n=25: the pass depends on which chunks CRAG and
MMR happened to assemble that run, and on one non-deterministic fast-tier
call.

## Result 3 — cost and retrieval neutrality

Wall time per run (25 questions each), from the runs' own `Total wall time`:

| Run | wall time | mean per-question latency |
|---|---|---|
| cert `on`  | 1m14.4s | 2974.0 ms |
| cert `on2` | 1m05.3s | 2611.4 ms |
| cert `off` | 0m58.6s | 2343.2 ms |

Mean latency delta `on` − `off` = **+630.8 ms (+26.9 %)**; `on2` − `off` =
**+268.2 ms (+11.4 %)**. Averaging the two `on` runs against the single
`off` run: **+449.5 ms (+19.2 %) per turn** — one extra fast-tier call over
≤ 12 sources, as designed.

Retrieval metrics (k=10) — the addendum is computed **after** the final
chunk set, so it must not move them:

| Run | mean_recall | mean_precision | mrr | mean_ndcg |
|---|---|---|---|---|
| cert `on`  | 0.630 | 0.234 | 0.637 | 0.666 |
| cert `on2` | 0.668 | 0.234 | 0.677 | 0.704 |
| cert `off` | 0.668 | 0.234 | 0.677 | 0.704 |

`on2` and `off` are **identical to three decimals on every column**, which
is the direct demonstration that the flag does not touch retrieval. The
`on` run's −0.038 recall / −0.040 MRR versus `off` is therefore entirely
inside the same-flag noise band (`on` vs `on2` is exactly those same
0.038/0.040), and is the ordinary CRAG-grader non-determinism this fixture
has shown since Wave 2 — not an effect of the flag.

## Result 4 — false positives on the PPM set (89 questions, no known conflicts)

`eval/golden/production-ppm-2026-08.jsonl`, same standard-path run with
`--conflict-surfacing on` (17m58s wall, 0 errors, mean per-question latency
12 114 ms — this KB is much slower per turn than the CERT fixture, and there
is no `off` companion run for it, so no latency delta is claimed here).

| Metric | Value |
|---|---|
| questions flagged (≥ 1 entry) | **11/89 = 0.124** |
| total entries | 13 |
| entries with `kind = superseded` | 0 |
| questions with a directed supersession | 0 |

Everything the pass reported on this corpus is a `contradiction`, never a
supersession — which is the right shape for a project-documentation KB with
no NEU/UPDATE convention, but it means the badge would appear on ~1 turn in
8 with nothing actionable behind most of them.

Two of the thirteen, quoted in full:

> `[Q071] kind=contradiction newer=unknown`
> claim: *"Das Projekt 'Neue Wege mit KI' wurde im März 2026 abgeschlossen."*
> A: `Projektabschlussbericht Neue Wege mit KI.md`
> B: `Kommunikation Projektabschluss Neue Wege mit KI.md`

— an announcement and the closing report of the *same* project; they agree,
they merely phrase the date differently. The same question also produced the
mirror-image entry with A and B swapped ("… wurde im März beendet"), i.e. the
duplicate-pair behaviour also seen once on CERT.

> `[Q095] kind=contradiction newer=unknown`
> claim: *"Das Projekt 'Verlängerung Adobe Softwarelizenzverträge' ist als
> 'Must-Have' eingestuft."*
> A: `Verlängerung Adobe Softwarelizenzverträge.md`
> B: `JLU-weites Confluence.md`

— a project sheet and an unrelated Confluence page that happens to carry a
different priority list; the "disagreement" is between two documents that
were never talking about the same thing.

**A real defect this run surfaced:** two of the 13 entries (`Q018`, `Q096`)
pair a file **with itself** (`fileA == fileB`) — two chunks of the same
document given different `[N]` numbers. `ai.DetectSourceConflicts` rejects a
self-pair only when the two *indices* are equal, and the ≥ 2-distinct-files
gate is applied to the whole set, not per entry, so "one document contradicts
itself" survives validation. That is exactly the category error the gate's
own comment says it exists to prevent. **Fixed in this release** (Wave-5
final fix wave): `buildConflictReport` now drops any entry whose two sources
resolve to the same `FileID`, and collapses mirrored `(a, b)` / `(b, a)`
entries into one whose `newer` is re-decided from the file dates.

**Therefore the 0.124 PPM flag rate above is an UPPER BOUND, not a point
estimate.** It was measured BEFORE the detector fix. 2 of the 13 entries were
same-file pairs and one CERT entry (`cert-n01`) was a mirrored duplicate with
the wrong direction; both are **fixed in this release** (Wave-5 final fix
wave — same-file pairs dropped, mirrored entries collapsed with the direction
re-decided from the file dates), so the corrected rate can only be lower —
never higher. It is reported as measured because that is what this run
produced, but **the rate must be re-measured on the fixed detector before it
is used to recommend, or to keep rejecting, this flag**; that re-measurement
is a roadmap item, not part of this release. The decision below stands
on its own regardless: 0.124 exceeds the 0.10 bar, and the CERT criterion
fails independently of it.

## Decision

Gate from the brief (W5-R7): recommend the flag in the recipe **for RSS KBs
only if** ≥ 6/8 pairs are flagged with the newer marked **AND** the PPM flag
rate is ≤ 10 %.

| Criterion | Threshold | Measured | Met? |
|---|---|---|---|
| pairs flagged with newer=UPDATE | ≥ 6/8 | **0/8** | no |
| PPM flag rate | ≤ 0.10 | **0.124** (an upper bound — see the same-file-pair defect above) | no |

**Decision: no recipe recommendation, and no default flip.**
`chat_conflict_surfacing_enabled` stays **OFF**, exactly as Task 3 shipped it.

Both criteria fail, but for different reasons, and the distinction matters
for whoever picks this up next:

1. The **0/8** is a property of the fixture, not evidence against the
   mechanism. The eight pair questions cannot put both halves of their own
   pair into the assembled set (MMR near-duplicate suppression), so they
   cannot test conflict surfacing at all. On the same 25 questions the pass
   flagged 12 of 37 co-occurring pairs with the direction right **12/12**,
   surfaced 8–9 of the corpus's 14 pairs at least once, and produced **zero**
   entries naming a non-pair. On a supersession-shaped corpus the mechanism
   works and its direction reasoning is reliable; what it lacks is retrieval
   that puts both halves in front of it.
2. The **0.124** is evidence against enabling it broadly. On a normal
   project-documentation KB it fires on ~1 turn in 8, always as
   `contradiction`, and the inspected examples are two documents about the
   same project agreeing in different words, or two documents that are not
   about the same thing at all.

Follow-ups this measurement earns (not done here — W5-R9 forbids further
answer-time work in this wave):

- Drop entries whose two sources share a `FileID` (the self-pair defect
  above) and de-duplicate mirrored pairs (A/B swapped) before building the
  report. Both were observed live.
- A supersession-shaped fixture whose questions are authored to put *both*
  halves of a pair in the set (e.g. "Was hat sich an WID-SEC-2026-0104
  geändert?") — the current pair questions were designed for a different
  measurement and are not reusable for this one.
- Re-measure the false-positive rate after those two fixes; the ~1-in-8
  rate is the number that has to come down before an RSS-KB recommendation
  is defensible.

## Artifacts

Gitignored (not committed), under
`.superpowers/sdd/2026-09-06-rag-sota-wave5/`:

| Path | Contents |
|---|---|
| `t5-cert.sh` | restamp + read-only date verification |
| `t5-runs.sh` | the four runs |
| `t5-analyse.py` | reproduces every number above |
| `t5-out/t5-cert-on.json` / `.log` | CERT, flag on |
| `t5-out/t5-cert-off.json` / `.log` | CERT, flag off (control) |
| `t5-out/t5-cert-on2.json` / `.log` | CERT, flag on (repeat) |
| `t5-out/t5-ppm-on.json` / `.log` | PPM, flag on |
| `t5-analysis.txt` | the analysis script's full output |
| `eval/golden/cert-recency-de.local.jsonl` | the runnable golden set (KB id resolved) |

---

# Conflict surfacing re-measured (Wave 6)

- **Date:** 2026-09-07
- **Branch/base:** `feat/rag-sota-wave6` (Task 1; base `ce0b972`, local main
  after the Wave-5 merge)
- **Ruling:** W6-R1 (pre-registered 2026-09-07, `.superpowers/sdd/2026-09-07-rag-sota-wave6/rulings.md`) —
  the eight CERT `cert-p01`..`cert-p08` pair questions were rewritten to
  **name the advisory and ask for the delta** ("Was hat sich an der Meldung
  WID-SEC-2026-NNNN zu &lt;Produkt&gt; gegenüber der ersten Fassung
  geändert?"), with `must_cite_file_names` set to **both** halves and an
  `expected_points` array per row (see `eval/golden/README.md` §"NEU/UPDATE
  pair questions (Wave 6 rewrite)" for the full rationale and the fixture
  text each `expected_points` entry was verified against). This section is
  the re-measurement that rewrite exists to feed.

## RULE (stated before the numbers, pre-registered 2026-09-07)

```
RULE (W6-R1, pre-registered 2026-09-07): PASS iff (a) >= 6/8 pairs have
both_assembled AND flagged AND newer_is_update, and (b) PPM flag rate <= 0.10
on BOTH runs. Only a PASS makes the recipe recommend
chat_conflict_surfacing_enabled for RSS/advisory KBs; the default stays OFF
either way.
```

Definitions (unchanged from the Wave-5 rules, restated in
`.superpowers/sdd/2026-09-07-rag-sota-wave6/t1-analyse.py`):

- **assembled set** — the question's `retrieved` array in the JSON report,
  the final chunk set sorted by score and cut at `k=10` (the eval CLI
  default). The detector's own window is the turn's `sources` list capped
  at `chat_conflict_max_chunks` (12), so this view can slightly
  **under-count** what the detector saw; both-files-present is judged on
  file NAMES.
- **both_assembled** — both halves of the pair are present, by name, in the
  question's `retrieved` (top-10).
- **flagged** — the question's `conflicts` array contains an entry whose
  `{fileA,fileB}` is exactly that pair.
- **newer_is_update** — such an entry has `kind == "superseded"` and the
  side `newer` points at is the UPDATE file.
- **PPM flag rate** — questions with ≥ 1 conflict entry of any shape, over
  ALL questions in the report.

## Setup — exact commands

```bash
# 1. Re-stamp the fixture KB (relative to now()) and regenerate the runnable
#    golden set (sources the main checkout's .env; never echoes the password).
bash .superpowers/sdd/2026-09-07-rag-sota-wave6/t1-cert.sh

# 2. CERT measurement, standard path, dispatch OFF, flag on then off:
bash .superpowers/sdd/2026-09-07-rag-sota-wave6/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl --production-context \
  --orchestrator-dispatch=false --conflict-surfacing on \
  --output .superpowers/sdd/2026-09-07-rag-sota-wave6/t1-out/cert-on.json
bash .superpowers/sdd/2026-09-07-rag-sota-wave6/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl --production-context \
  --orchestrator-dispatch=false --conflict-surfacing off \
  --output .superpowers/sdd/2026-09-07-rag-sota-wave6/t1-out/cert-off.json

# 3. PPM false-positive measurement, two independent runs (flag on both):
bash .superpowers/sdd/2026-09-07-rag-sota-wave6/run-eval.sh \
  --golden eval/golden/production-ppm-2026-08.jsonl --production-context \
  --orchestrator-dispatch=false --conflict-surfacing on \
  --output .superpowers/sdd/2026-09-07-rag-sota-wave6/t1-out/ppm-on-1.json
bash .superpowers/sdd/2026-09-07-rag-sota-wave6/run-eval.sh \
  --golden eval/golden/production-ppm-2026-08.jsonl --production-context \
  --orchestrator-dispatch=false --conflict-surfacing on \
  --output .superpowers/sdd/2026-09-07-rag-sota-wave6/t1-out/ppm-on-2.json

# 4. Retrieval-reason isolation for each of the 8 pairs (single-question
#    runs; --top-k 30 to see the whole `final_docs` pool, not just the
#    report's default k=10 cut):
bash .superpowers/sdd/2026-09-07-rag-sota-wave6/run-eval.sh \
  --golden eval/golden/cert-recency-de.local.jsonl --production-context \
  --orchestrator-dispatch=false --conflict-surfacing on \
  --question-id cert-p01 --top-k 30 \
  --output .superpowers/sdd/2026-09-07-rag-sota-wave6/t1-out/pair-cert-p01-k30.json
# (repeated for cert-p02..cert-p08)

# 5. Every number below is reproduced by:
python3 .superpowers/sdd/2026-09-07-rag-sota-wave6/t1-analyse.py
```

`--orchestrator-dispatch=false` per W6-R12 / W5-R12: conflict surfacing is a
standard-`PrepareChatContext` feature.

### Re-stamp verification (read-only, before the runs)

All 8 golden pairs carry two distinct `created_at` values with the UPDATE
newer, and `published_at` is NULL throughout:

```
 pairs | update_newer | same_timestamp
-------+--------------+----------------
     8 |            8 |              0
```

e.g. `WID-SEC-2026-0104`: NEU `2026-08-31 04:53:56`, UPDATE
`2026-09-06 04:53:56`.

## Result 1 — the eight rewritten pair questions: still 0/8, and still not the detector's fault

| Question | WID | Product | both_assembled (top-10, `cert-on`) | flagged | newer_is_update |
|---|---|---|---|---|---|
| cert-p01 | WID-SEC-2026-0104 | Fortinet FortiOS | UPDATE only | no | no |
| cert-p02 | WID-SEC-2026-0106 | Ivanti Connect Secure | NEU only | no | no |
| cert-p03 | WID-SEC-2026-0107 | Cisco IOS XE | NEU only | no | no |
| cert-p04 | WID-SEC-2026-0108 | VMware ESXi | NEU only | no | no |
| cert-p05 | WID-SEC-2026-0109 | OpenSSL | UPDATE only | no | no |
| cert-p06 | WID-SEC-2026-0113 | Citrix NetScaler | NEU only | no | no |
| cert-p07 | WID-SEC-2026-0119 | Atlassian Confluence | UPDATE only | no | no |
| cert-p08 | WID-SEC-2026-0123 | GitLab | NEU only | no | no |
| **totals** | | | **0/8 both present** | **0/8** | **0/8** |

Rewriting the question to name the advisory and ask for the delta did
**not** change this: at the report's default `k=10` view (the production
turn shape), none of the eight pair questions assemble both halves. That
is shown directly by `cert-on.json`'s `retrieved` arrays (the table
above). It is **not** evidence that "the conflict detector never gets the
chance to flag a NEU/UPDATE pair on this corpus" — see Result 1a below,
which shows the same `cert-on` run flagging five of these eight pairs
correctly through other questions.

### Result 1a — what the detector actually does on this corpus (`cert-on`, same run)

Every `kind=superseded` entry the detector produced in the same `cert-on`
run, across ALL 25 questions (not just the 8 pair questions) — reproduced
by `t1-analyse.py`'s `supersession_summary()`:

| Question | WID | `newer` points at | Direction correct |
|---|---|---|---|
| cert-r02 | WID-SEC-2026-0107 | UPDATE | yes |
| cert-r05 | WID-SEC-2026-0106 | UPDATE | yes |
| cert-r06 | WID-SEC-2026-0104 | UPDATE | yes |
| cert-r06 | WID-SEC-2026-0106 | UPDATE | yes |
| cert-c04 | WID-SEC-2026-0121 | UPDATE | yes |
| cert-c04 | WID-SEC-2026-0115 | UPDATE | yes |
| cert-e01 | WID-SEC-2026-0115 | UPDATE | yes |
| cert-e02 | WID-SEC-2026-0109 | UPDATE | yes |
| cert-e03 | WID-SEC-2026-0123 | UPDATE | yes |
| cert-n01 | WID-SEC-2026-0111 | UPDATE | yes |
| **totals** | **10 entries, 8 questions** | | **10/10 correct** |

Of the 8 WIDs covered (0104, 0106, 0107, 0109, 0111, 0115, 0121, 0123),
**five are `cert-p01`..`cert-p08` target pairs** (0104, 0106, 0107, 0109,
0123) — the detector correctly identified and directed those exact
NEU/UPDATE supersessions, just through the recency-listing (`cert-r02`,
`cert-r05`, `cert-r06`), CVE-lookup (`cert-c04`, `cert-n01`) and
enumeration (`cert-e01`, `cert-e02`, `cert-e03`) questions, whose top-10
(or top-30, for the enumeration/listing routes) assembled both halves
where the pair questions' top-10 did not. This is the same shape of result
Wave 5 reported for its own pair questions ("12 of 37 opportunities …
direction correct 12/12, zero invented pairs") — the mechanism keeps
working; what fails specifically is the pair questions' own retrieval
shape.

### Retrieval-reason isolation: what is shown vs. what is inferred

Each pair question was re-run in isolation (`--question-id cert-p0N
--top-k 30`, standard path, dispatch off) to see the whole `final_docs`
pool (30 chunks; the eval report's default `k=10` view only shows the
first third of it). **Shown, directly, from the JSON reports:** both
halves of every pair ARE present somewhere in that 30-doc pool, in all 8
cases — the `retrieved` array for `pair-cert-p0N-k30.json` contains both
the NEU and UPDATE file names for every `N`. What differs is their rank:

| Question | Product | NEU rank | UPDATE rank |
|---|---|---|---|
| cert-p01 | Fortinet FortiOS | 15 | **1** |
| cert-p02 | Ivanti Connect Secure | **1** | 15 |
| cert-p03 | Cisco IOS XE | **1** | 15 |
| cert-p04 | VMware ESXi | **1** | 15 |
| cert-p05 | OpenSSL | 15 | **1** |
| cert-p06 | Citrix NetScaler | **1** | 15 |
| cert-p07 | Atlassian Confluence | 15 | **1** |
| cert-p08 | GitLab | **1** | 15 |

The `rag.search.stages` line for `cert-p01` (all 8 stages lines in
`t1-out/pairs-isolated.log` share this shape — `vector_docs`/
`keyword_docs` around 36–40, `rrf_docs`/`rerank_docs` 40, `mmr_docs`/
`final_docs` 30):

```
{"msg":"rag.search.stages","stage":"search_stages","vector_docs":40,"vector_files":40,
 "keyword_docs":36,"keyword_files":36,"rrf_docs":40,"rrf_files":40,"rerank_docs":40,
 "rerank_files":40,"dedup_docs":40,"dedup_files":40,"mmr_docs":30,"mmr_files":30,
 "bm25_floor_reinserted":1,"final_docs":30,"final_files":30,"rerank_used":true,
 "mmr_lambda":0.7,"rerank_depth":120,"hnsw_ef_search":151}
```

**What this log line does NOT show**: it carries only stage
*cardinalities* (`rerank_docs:40 → mmr_docs:30`), never a per-chunk rank.
The rank-1-vs-15 table above comes entirely from the `--top-k 30` report's
`retrieved` array — the *post-everything* order — not from anything the
stages log records. An earlier draft of this record attributed the split
to "MMR pushes the near-duplicate to the midpoint of the pool", stated as
an observation ("logs show"). That attribution is corrected here: it was
an inference the stages log cannot support, and a simpler, directly
verifiable mechanism accounts for the exact pattern instead.

**What IS shown, from the `retrieved` array's `score` field**: ranks 1–15
in every one of the 8 isolated `--top-k 30` reports carry the identical
score (`1.0` in 7 of 8 pairs; `0.9996426…` in `cert-p07`), i.e. a 15-wide
score tie, confirmed for all 8 pairs by `t1-analyse.py`'s
`tie_width_table()` (`t1-out/t1-analysis.txt`, "BM25-floor tie-block width
at k=30" section). That width, `15`, is exactly
`BM25FloorMaxFilesFor(limit=30)` — `limit/2` —
(`go-backend/internal/vector/rrf.go:270-276`). `ApplyBM25Floor`
(`rrf.go:309-`) walks the top-`maxFiles` BM25-ranked distinct files and
clamps each floor-protected chunk's score **up to the current top score**
(so it survives token-budget trimming and lands at a sandwich-order
boundary rather than the lost-in-the-middle region) — that clamp is what
produces the tie, and the demoted half of each pair sits at the *last*
slot of that tie block (rank 15) in every single case. The `bm25_floor_reinserted:1`
field in the (top-10) stages log confirms the floor mechanism fired on
these queries; it is a plausible and directly-supported explanation for
"the half is in the pool at all" and for "the tie block is exactly 15
wide" — it says nothing, by itself, about which half is at rank 1 vs.
rank 15 within the tie (the tie-breaking order inside a 15-way score tie
was not isolated in this task and is not claimed here).

**What is NOT shown, and is explicitly left open:** which stage puts the
demoted half specifically at the *last* position of the tie block rather
than elsewhere within it, and — separately — whether the same split
happens at the production `k=10` shape. The `k=30` rank table above
**cannot be carried over** to the `cert-on` (`k=10`) turn: the BM25-floor
budget (`limit/2`), the MMR pool size and the final trim all scale with
`limit`, so `BM25FloorMaxFilesFor(10) = 4`, not `15`, and the tie
structure at `k=10` was **not isolated** in this task (no per-rank stages
data exists for the production-shaped run — only the `retrieved`
top-10 list, which is what the Result 1 table above already reads
directly). What the `k=10` `cert-on` run **does** show directly, without
any inference, is the conclusion that matters for the pass/fail rule:
across all 8 pair questions, only one half of the pair is in the
production-shaped assembled set (`cert-on.json`'s `retrieved`, k=10, per
question), in every case. That is a direct read of the report, not a
carry-over from the `k=30` diagnostic. No `site_config` was changed to
produce or investigate any of this.

## Result 2 — cost and retrieval neutrality (25 CERT questions)

Wall time:

| Run | wall time | mean per-question latency |
|---|---|---|
| cert `on`  | 1m14.4s | 2976.5 ms |
| cert `off` | 0m59.8s | 2391.4 ms |

Delta `on` − `off` = **+585.1 ms (+24.5 %)** per turn — one extra fast-tier
call over ≤ 12 sources, consistent with Wave 5's +19–27 % range.

Retrieval metrics (k=10):

| Run | mean_recall | mean_precision | mrr | mean_ndcg |
|---|---|---|---|---|
| cert `on`  | 0.700 | 0.250 | 0.873 | 0.901 |
| cert `off` | 0.708 | 0.254 | 0.873 | 0.902 |
| delta | −0.008 | −0.004 | +0.000 | −0.002 |

MRR is identical; recall/precision/ndcg differ in the third decimal. This
is the same CRAG-grader run-to-run non-determinism the Wave-2 and Wave-5
sections of this document already document on this fixture (not a re-run
noise-band pair here — only one `off` run was taken — so this delta is
reported as-measured, consistent in direction and magnitude with the prior
waves' noise band, not claimed as a new same-flag control).

## Result 3 — PPM false-positive rate (89 questions, no known conflicts), two runs

`eval/golden/production-ppm-2026-08.jsonl`, standard path, dispatch off,
`--conflict-surfacing on`, run twice (no code changes between runs — this
is the pre-registered two-run PPM check):

| Run | wall time | mean per-question latency | flagged | flag rate |
|---|---|---|---|---|
| ppm on-1 | 18m52.4s | 12723.5 ms | 10/89 | **0.112** |
| ppm on-2 | 18m52.1s | 12719.5 ms |  9/89 | **0.101** |

Both runs exceed the ≤ 0.10 gate (run 1 by 1.2 pp, run 2 by 0.1 pp — the
smallest possible margin above the threshold, since 9/89 = 0.1011...). Nine
of the ten flagged question ids overlap between the two runs (`Q012`,
`Q013`, `Q030`, `Q038`, `Q062`, `Q084`, `Q085`, `Q091`, `Q095` all recur);
run 1 additionally flags `Q071` (`Entwurf Grußwort VPW.md` vs.
`Planungsprojekt Neue Wege mit KI.md`, `kind=contradiction`), which is
exactly the one question that accounts for the 10-vs-9 gap between the
two runs — it did **not** fail to fire in Wave 6 (an earlier draft of this
record said the opposite; corrected here against `t1-out/t1-analysis.txt`,
which lists `Q071` under "ppm on-1 flagged question ids" and under "ids in
run 1 only"). Separately, `Q091`'s claim text differs between the two runs
even though the file pair is the same (run 1: "Die Priorisierung erfolgt
über die Kategorien strategische Relevanz und Nutzen…"; run 2: "Die
Bewertung der Priorisierung erfolgt über die Kategorien Wichtigkeit und
Dringlichkeit…") — the *pair* recurs, the wording the fast-tier call
produced for it does not. All 20 entries across both runs are
`kind=contradiction`, `newer=unknown` — same shape as Wave 5: no
superseded pair was found on this KB, which has no NEU/UPDATE convention.
Two representative entries (seen in both runs):

> `[Q012]` claim: *"Der Zeitraum des Planungsprojekts 'Neue Wege mit KI'"*
> A: `Planungsprojekt Neue Wege mit KI.md`
> B: `Projektabschlussbericht Neue Wege mit KI.md`

> `[Q095]` claim: *"Das Projekt 'Verlängerung Adobe Softwarelizenzverträge'
> ist als 'entscheidbar' eingestuft."*
> A: `Verlängerung Adobe Softwarelizenzverträge.md`
> B: `JLU-weites Confluence.md`

No same-file pair and no mirrored-duplicate entry was observed in either
run (the Wave-5 final fix wave's `buildConflictReport` de-dup/self-pair
fixes hold on this corpus). The **0.124** Wave-5 number was explicitly
flagged there as an upper bound measured before that fix; this Wave-6
re-measurement (0.112 / 0.101) is the corrected number, on the same
fixture, and it is lower — but still above the 0.10 gate on both runs.

## Decision

| Criterion | Threshold | Measured | Met? |
|---|---|---|---|
| pairs with both_assembled AND flagged AND newer_is_update | ≥ 6/8 | **0/8** | no |
| PPM flag rate, run 1 | ≤ 0.10 | **0.112** | no |
| PPM flag rate, run 2 | ≤ 0.10 | **0.101** | no |

**Verdict: FAIL.** Per W6-R1, the recipe does **not** gain a recommendation
for RSS/advisory KBs, and `chat_conflict_surfacing_enabled` stays default
**OFF** — unchanged from Wave 5.

Both criteria fail, and the Wave-6 rewrite narrows down exactly why each
one does:

1. **0/8 is a retrieval-shape finding specific to how the pair questions
   retrieve, not evidence the detector cannot handle this corpus.** The
   Wave-5 hypothesis ("the pair questions were authored to test promotion,
   not co-retrieval, so of course both halves aren't retrieved together")
   predicted that naming the advisory and asking for the delta would fix
   it. It did not: at the production `k=10` shape, every one of the 8 pair
   questions' `retrieved` list contains only one half of its pair (shown
   directly, Result 1's table). What is **not** established is the
   mechanism inside retrieval that produces this — the `k=30` diagnostic
   shows both halves present in the wider 30-doc pool for all 8 pairs,
   split by a BM25-floor score-boost tie block whose width
   (`BM25FloorMaxFilesFor(30)=15`) is shown, but that tie's internal
   ordering (why one half lands at rank 1 and the other at exactly rank
   15, rather than the reverse or some other position within the tie) was
   not isolated, and the `k=30` structure does not carry over arithmetically
   to the `k=10` production shape (floor budget, MMR pool and trim all
   scale with `limit`). And — importantly — **Result 1a shows the detector
   is not structurally blind to this corpus's NEU/UPDATE pairs**: on the
   very same `cert-on` run, it correctly flagged and directed 5 of these 8
   exact pairs through other questions (recency-listing, CVE-lookup,
   enumeration) whose retrieval shape happened to assemble both halves —
   10 `superseded` entries, direction correct 10/10, zero invented pairs.
   So the finding is specifically that **these 8 pair questions' own
   retrieval shape** does not put both halves of their own target pair
   into the assembled set, not that conflict surfacing cannot work on
   this KB. Investigating why (an MMR/BM25-floor tuning change, a
   fixture/detector design that does not depend on the *queried* pair's
   own top-k, e.g. widening `chat_conflict_max_chunks` past 12) is a
   retrieval-tuning question, explicitly out of scope for this task per
   the brief ("do NOT tune anything, do NOT change any site_config") —
   flagged here as a roadmap item, not attempted or measured.
2. **0.112 / 0.101 confirms the Wave-5 upper-bound claim was directionally
   right**: the corrected detector (self-pair dropped, mirrored duplicates
   collapsed) does score lower than the pre-fix 0.124, but it remains above
   the 0.10 gate on both independent runs, with the second run landing only
   0.1 pp over the line. On a normal project-documentation KB with no
   NEU/UPDATE convention, the pass still fires on roughly 1 turn in 9,
   always as an undirected `contradiction`, on document pairs that mostly
   turn out to be either genuinely different (a project sheet vs. an
   unrelated Confluence page) or agreeing in different words (an
   announcement vs. the closing report of the same project) — the same
   two failure patterns Wave 5 already catalogued.

No `site_config` was mutated to produce any number in this section (every
override is a `cmd/eval` overlay flag, `--conflict-surfacing on|off`).

## Artifacts

Gitignored (not committed), under
`.superpowers/sdd/2026-09-07-rag-sota-wave6/`:

| Path | Contents |
|---|---|
| `t1-cert.sh` | restamp + read-only date verification |
| `t1-analyse.py` | reproduces every number above |
| `run-eval.sh` | shared eval wrapper (fresh binary, dev env) |
| `t1-out/cert-on.json` / `.log` | CERT, flag on |
| `t1-out/cert-off.json` / `.log` | CERT, flag off (control) |
| `t1-out/ppm-on-1.json` / `.log` | PPM, flag on, run 1 |
| `t1-out/ppm-on-2.json` / `.log` | PPM, flag on, run 2 |
| `t1-out/pair-cert-p0N.json` | isolated single-question runs (top-10) |
| `t1-out/pair-cert-p0N-k30.json` | isolated single-question runs (top-30, rank table above) |
| `t1-out/pairs-isolated.log` | stderr for the top-10 isolation runs |
| `t1-out/t1-analysis.txt` | the analysis script's full output |
| `eval/golden/cert-recency-de.local.jsonl` | the runnable golden set (KB id resolved) |
