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
