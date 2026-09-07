# Changelog

All notable changes to JustRAG are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project uses
SemVer `0.x`, where a **minor** bump covers features *and* breaking changes
and a **patch** bump is fixes only.

Releases may carry a hand-written **⚠ Upgrade notes** block recording
migrations, changed `site_config` defaults, and re-ingest requirements.
Those are not generated — a release whose notes list a migration has **no
one-step rollback** (`cmd/migrate` is up-only).

## Unreleased

<!-- Not a git-cliff section (every other heading below is a released, tagged
     version). This one exists because the night-sync-scheduling work landed
     its hand-written upgrade notes before a release was cut. When cutting the
     next release, `git cliff --unreleased --tag vX.Y.Z --prepend` will insert
     the generated "## vX.Y.Z — <date>" section ABOVE this one — fold this
     block's content into that new section's "### ⚠ Upgrade notes" and delete
     this heading rather than leaving both. -->

### ⚠ Upgrade notes

- **Migration 0068 required.** Adds `sync_schedule` + `next_sync_at` to
  `rss_feeds`, `confluence_sources` and `git_repo_sources`.
- **Automatic syncs move to a night window.** Every RSS feed that polled on an
  interval (15 min – 24 h) becomes `daily` and now runs once per night;
  Confluence sources with any interval — including weekly ones — likewise
  become `daily`. CERT-Bund advisory feeds therefore surface up to ~24 h later
  than before. Feeds that were already paused (not `status = 'active'`) stay
  `manual` rather than being flipped onto a nightly schedule; sources that were
  already manual stay manual. Git repositories stay manual until an admin opts
  them in — they had no scheduler before this release.
- The window defaults to 01:00–05:00 `Europe/Berlin` and is configurable in
  the admin Agent panel (`sync_window_start_hour`, `sync_window_end_hour`,
  `sync_window_timezone`).
- This release contains a migration, so it cannot be rolled back by
  re-pointing the image tag alone.

- **Migration 0071 required** (RAG Wave 3, freshness surface). Adds
  `files.published_at` and `last_success_at` on `rss_feeds`,
  `confluence_sources` and `git_repo_sources`. Compose applies it via the
  `migrate` one-shot service; **Kubernetes does not** — run `/app/migrate` out
  of the release image before `kubectl apply`, per `docs/runbooks/release.md`.
  As with 0068, a release carrying a migration has **no one-step rollback**.
  **Symptom of skipping it** (new image, old schema — the k8s case): *every*
  file creation fails, because both `CreateFile` INSERTs name `published_at` —
  uploads, RSS polls, Confluence and git syncs and the crawler all error out —
  and, since `chat_recency_listing_enabled` defaults ON, a recency-listing chat
  turn ("Welche neuen Meldungen gibt es?") returns a 500 as the window-scoped
  search selects the missing column.
- **No `published_at` backfill.** Every file ingested before 0071 keeps
  `published_at = NULL` and therefore keeps being aged by `created_at` (ingest
  time); RSS files pick the real publication date up on their next poll or
  re-ingest. Likewise, every source shows its last *attempt* with
  `syncSucceeded = false` until its next successful sync stamps
  `last_success_at`. Both are surfaced in the UI rather than hidden, and both
  heal on their own — do not hand-write either column.
- **`rag_longcontext_route_total` changed shape.** It gained a `mode` label,
  and `outcome` gained `considered` (gate on, turn eligible, classifier did not
  fire) and `map_empty`. Dashboards and alerts keyed on the previous label set
  break and must be updated. An orchestrator error that falls back to
  `PrepareChatContext` can count the same turn twice.
- **Long-context routing is now an orchestrator** (`chat_longcontext_enabled`,
  still default off). It previously lived only inside `PrepareChatContext`,
  which streaming `complex_reasoning` turns never reach, so the route was
  unreachable for the query class it targets. Deployments with the flag **on**
  will now actually see it fire — and it sits above the Supervisor in the
  ladder, below DRIFT. New key `chat_longcontext_mode` (`flat` | `map_reduce`)
  defaults to `flat`, whose prompt is byte-identical to the previous
  behaviour; `map_reduce` is opt-in and costs ~25 extra fast-tier calls per
  turn (set `AI_MAX_CONCURRENT_REQUESTS` first).
- **New optional `site_config` keys, all default-off or default-unchanged:**
  `chat_citation_spans_enabled` (+ `_max_sources`, `_timeout_ms`, `_model`),
  `chat_longcontext_mode` (+ `_map_group_size`, `_map_concurrency`,
  `_map_model`), and the global-only integer `kb_stale_days` (default 180).
- **RAG Wave 4 adds no migration.** Nothing in it changes the schema; 0071
  (Wave 3, above) is still the highest migration in this Unreleased block.
- **`published_at` now also comes from Confluence and git.** Confluence
  **pages** are stamped with the page's current version timestamp
  (attachments stay NULL — the REST shape carries no attachment date), and
  every file of a git sync is stamped with the HEAD commit's *committer* time
  (the clone is shallow, so there is no per-file history to read). All three
  origins clamp a future date to `now`. As with the RSS case there is **no
  backfill**, and Confluence/git dates are written on file *creation*: a
  Confluence page changes by delete-and-recreate, and a git sync whose HEAD
  has not moved creates nothing — so an existing KB keeps `published_at =
  NULL` on those files until its next real sync. Nothing to run; do not
  hand-write the column.
- **Source dates on two more surfaces (additive).** OpenAI-compat's Azure-
  shaped `message.context.citations[]` entries gain `created_at` /
  `published_at`, and the KB-as-MCP `ask_kb` tool's `Source` gains `createdAt`
  / `publishedAt` — RFC 3339 in UTC, omitted when unset, so a client that does
  not read them is unaffected. The OpenAI `file_citation` **annotation** shape
  deliberately stays dateless (the spec has no slot for it).
- **`GET /api/admin/kb-overview` rows gain `syncByKind`, and the row-level
  `syncSucceeded` changed meaning.** Each row now carries one
  `{kind, lastSyncAt, syncSucceeded, syncFailing, sourceCount}` entry per
  source kind, and the aggregate `syncSucceeded` means "**every** kind with
  sources has a verified success" instead of "at least one has". A dashboard
  or alert reading the old field will see rows flip from `true` to `false`
  where one healthy source kind had been masking a dead one — that is the
  point of the change, not a regression.
- **`cmd/eval` gains a pairwise mode and a coverage judge.**
  `--pairwise-a A.json --pairwise-b B.json [--pairwise-out out.json]` compares
  two finished `--judge` reports offline: each question pair is judged in both
  orders and counts only when both agree, printing wins/ties/losses, a win
  rate with a 95 % Wilson interval, and per-route/per-question tables. It
  always exits 0 on a completed comparison (a measurement, not a gate). The
  optional golden field `expected_points` (2–6 short statements, loader caps
  ≤ 12 points / ≤ 300 runes) adds a fourth judge, `coverage`, reported as
  `mean_coverage` + `coverage_n`; rows without it skip the judge entirely.
- **Judge parsing is tolerant, and the aggregate reports per-metric counts.**
  A score emitted as a numeric string is accepted and clamped to 1–5; a
  boolean list of the wrong length is truncated or padded with a
  `judge_warnings` entry; only unparseable JSON still drops a sample. New
  aggregate fields `faithfulness_n` / `answer_relevance_n` /
  `context_precision_n` / `coverage_n`, printed as `(n=…)`. Old judge numbers
  stay comparable for well-formed responses, but `n` may be higher than before
  because fewer samples are dropped. The extractor also survives two shapes it
  used to reject outright: a reply containing **two** JSON objects (the first
  parseable one wins) and one wrapped in a ```json fence around an object that
  is complete. A reply cut off before its object closes is still an error, now
  reported as a distinct `truncated JSON` (a completion-token limit on the
  judge model is the usual cause) instead of a generic "not valid JSON". A
  `"score": null` — the judge declining to rate — is now an error too, so the
  sample is dropped: it used to unmarshal to 0 and clamp **up** to 1, silently
  recording a real "barely relevant" rating. **This is not eval-only.** The
  same `internal/eval.Judge` runs at runtime in the RAGAS background sampler
  (`ragas_sampling_enabled`, `internal/worker/ragas_sample.go`) and in the
  in-app / scheduled eval runner, so those surfaces get the same tolerance:
  expect fewer `error`-outcome samples and the `rag_ragas_*` distributions to
  shift accordingly (more samples, and no more `null` scores landing on the
  Likert floor).
- **The eval ladder now mirrors DRIFT.** `cmd/eval --production-context
  --orchestrator-dispatch=true` dispatches global-synthesis questions through
  the real DRIFT orchestrator at production's ladder position (above
  long-context) and reports `agent.orchestrator = "drift"`. A deployment with
  `chat_drift_enabled` on was previously evaluating those questions through
  long-context instead.
- **Wave 4 flipped no `site_config` default**, required no re-ingest and left
  `queryCacheSchemaVersion` unchanged (Wave 5 does flip one — see below). Both
  Wave-4 measurement tasks concluded "keep the default": `chat_longcontext_mode` stays `flat`
  (map_reduce raised coverage in both cross pairs and won 16 of 20 pooled
  decisive pairs, but the pre-registered per-pair rule missed on one pair at
  n=12) and `bm25_scoring_mode` stays `ts_rank` (on the plan-execute path with
  dispatch on, `complex_reasoning` MRR is −4.7 pp against a 2.6 pp band over 3
  repeats).

- **Migration 0072 required** (RAG Wave 5, trust surfaces) — now the highest
  migration in this Unreleased block. One file, idempotent, **no backfill**:
  adds the `ragas_samples` table (plus a `(kb_id, sampled_at DESC)` index on
  the table it creates empty, so the README's `CONCURRENTLY` rule for
  already-large tables does not apply), `messages.conflicts jsonb`,
  `files.injection_flag boolean NOT NULL DEFAULT FALSE` (metadata-only on
  PG 11+, so no rewrite of a large `files` table) and `files.injection_detail
  jsonb`. Compose applies it via the `migrate` one-shot service; **Kubernetes
  does not** — run `/app/migrate` out of the release image before
  `kubectl apply`, per `docs/runbooks/release.md`. A release carrying a
  migration has **no one-step rollback**.
- **DEFAULT CHANGED: `chat_longcontext_mode` flips from `flat` to
  `map_reduce`.** This is the one default this wave moves. The rule was
  pre-registered as **W5-R1 on 2026-09-06**, before the measurement set was
  extended and before any of the runs existed, and all four of its criteria
  passed on 24 questions: pooled `map_reduce` win rate **0.9444** (34 of 36
  decisive judge pairs) with a pooled Wilson lower bound of **0.8186**
  (> 0.50), pooled coverage **+5.03 pp** (0.5837 vs 0.5333) against a 1.39 pp
  same-mode band, and a flat-vs-flat control at **0.3636**, inside the
  required [0.35, 0.65] window. Cost, reported and never a veto: **1.28× wall
  time**, i.e. the ~25 extra fast-tier calls per turn are unchanged. Record:
  `eval/golden/global-synthesis-de.acceptance.md` §4.
  - **Who is affected:** only deployments with `chat_longcontext_enabled` on
    (still default off) *and* a `chat_longcontext_mode` row that was never
    written. The route is otherwise unreachable, so most deployments see no
    behaviour change at all.
  - **The fallback rule is deliberately asymmetric.** An **unset** key now
    reads `map_reduce`; an **unrecognised** value (a typo) still normalises to
    `flat` and logs a warning — the safe fallback must never be the mode that
    fans out a fast-tier call per chunk group.
  - **To keep the previous behaviour, set the key explicitly:**
    `chat_longcontext_mode = flat` (globally, or as a per-KB override).
  - Before leaving the new default in place on a busy deployment, set
    `AI_MAX_CONCURRENT_REQUESTS` to the backend's safe ceiling: the per-turn
    map fan-out is bounded, the deployment-wide product of fan-outs is not.
  - Two diagnostics, neither a decision input: faithfulness came out
    marginally *lower* for `map_reduce` (0.461 / 0.533 vs 0.464 / 0.569 — a
    findings block is a lossy intermediate), and answer relevance is saturated
    at 1.000 on this route and unusable as a signal.
- **New `site_config` keys (Wave 5).** `ragas_samples_retention_days` (90,
  range 1–3650, global-only); `chat_conflict_surfacing_enabled` (**false**,
  per-KB) + `chat_conflict_model` (fast-tier chain) + `chat_conflict_max_chunks`
  (12, 2–30) + `chat_conflict_timeout_ms` (6000, 1000–30000);
  `ingest_screening_enabled` (**true** — it is a flag, not a filter, and the
  key is its kill switch) + `ingest_screening_window_runes` (600, 100–5000);
  `chat_answer_degenerate_run_limit` (400 runes, `0` disables, otherwise
  clamped to 50–100000, global-only). Only `ingest_screening_enabled` is
  on by default, and it changes nothing about the corpus.
- **The RAGAS sampler now persists what it scores.** With
  `ragas_sampling_enabled` on, each sample writes one `ragas_samples` row
  (nullable scores, `judge_model`, judge errors) so a bad score can be
  attributed to a turn instead of only alerted on. A nightly `ragas_daily`
  maintenance pass (24 h, `WORKER_MAINTENANCE`) publishes the new gauges
  `rag_ragas_daily_mean{kb,metric}` and `rag_ragas_daily_n{kb}` over the
  trailing 24 h and prunes past the retention. **Both gauges share one 500-KB
  cardinality budget with an `overflow` series whose value is meaningless**
  (last-write-wins across every KB past the cap) — alert on its *presence*,
  never on its number. Nothing to run; the table starts empty and fills at the
  existing sampling rate.
- **Ingest prompt-injection screening ships ON.** Every newly ingested file
  from an external source (`rss`, `confluence`, `git`, `crawl`) is screened
  once before chunking and the verdict recorded on the `files` row. Uploads and
  spreadsheets are never screened. **It is a flag, not a filter**: chunking,
  embedding, retrieval and answer-time behaviour are byte-for-byte unchanged,
  and there is no quarantine. Expect **badges on documents that legitimately
  quote instructions** (prompt-engineering docs, incident reports) — a badge is
  all that happens. New metric `rag_ingest_injection_flag_total{origin}`; kill
  switch `ingest_screening_enabled=false` (checked before any store call, so
  off is genuinely free). No backfill: files ingested before 0072 read as
  "never screened" (`injection_detail IS NULL`) until their next re-ingest,
  which is a third state distinct from "screened and clean".
- **Degenerate-answer guard ships ON, on every answer surface.** When a
  streaming answer collapses into a repeated character or a repeated ≤ 4-rune
  pattern longer than `chat_answer_degenerate_run_limit` (400 runes), the
  completion is aborted, the run is stripped, and a one-line notice is
  appended in the answer language; non-streaming surfaces strip post hoc. New
  metric `rag_answer_degenerate_total{surface}` (`web|api_v1|openai_compat|mcp`)
  — **alert on any non-zero rate**: the guard contains the symptom, it does not
  fix the model. 400 sits well above any realistic Markdown table rule (~300),
  so normal answers cannot trip it; `0` disables it everywhere.
- **Additive API fields (no client breaks; every one is omitted when unset).**
  `GET /api/admin/kb-overview` rows gain `ragas: {n24h, faithfulness,
  answerRelevance, contextPrecision}` (24 h window, key absent when the KB has
  no sample) and `injectionFlagged` (an int, always present).
  `GET /api/kb/{id}/files` rows gain `injectionFlag` (bool, always present) and
  `injectionDetail` (object, omitted when NULL). Chat gains `conflicts` — a
  **bare array** of `{claim, sourceA, sourceB, kind, newer, fileA, fileB}` on
  the SSE frame right after `sources`, on the non-streaming body, in
  `messages.conflicts` and on reload; the key is omitted entirely when there is
  nothing to report, so a turn without conflicts streams exactly the frames it
  streamed before. `conflicts` only ever appears with
  `chat_conflict_surfacing_enabled` on, which is off by default.
- **Conflict / supersession surfacing ships OFF and the measurement says leave
  it off.** Both pre-stated gates failed on the Wave-5 fixture run: 0 of 8 CERT
  NEU/UPDATE pairs flagged (a fixture property — MMR never assembles both
  halves of the queried pair; on pairs the detector did see, 12 of 37
  opportunities hit with direction correct 12/12 and zero invented pairs), and
  a 0.124 false-positive flag rate on the PPM set against a ≤ 0.10 bar. **That
  0.124 was measured BEFORE the detector fix in this release and is an upper
  bound** — two of the thirteen entries paired a file with itself (duplicate
  chunks of one document), and a third reported the same pair twice with
  opposite `newer` directions; both defects are fixed in this release (Wave-5
  final fix wave: a conflict whose two sources resolve to the same file id is
  dropped, and mirrored duplicates collapse into one entry whose direction is
  re-decided from the file dates). Re-measuring the fixed detector is a
  roadmap item; until that happens the rate must not be used in either
  direction. Cost when on: one extra fast-tier call and
  +631 / +268 ms per turn; retrieval is untouched (identical to three decimals
  on/off). Record: `eval/golden/cert-recency-de.acceptance.md`.
- **Reminder: `internal/eval.Judge` is shared with the RAGAS sampler.** Any
  judge-parsing change in this block (see the Wave-4 tolerance entry above)
  affects the runtime RAGAS sampler and the in-app / scheduled eval runner as
  well as `cmd/eval`, and now shows up in `ragas_samples` rows too — a judge
  that fails to parse is persisted as a row with nil scores and its
  `judge_errors`, not dropped.
- **`cmd/eval` gains two flags.** `--conflict-surfacing on|off` overlays
  `chat_conflict_surfacing_enabled` for one run (no `site_configs` mutation)
  and records each question's `conflicts` array in the JSON report.
  `[--pairwise-out pooled.json] --pairwise-pool a.json b.json` pools two
  finished pairwise results over their decisive pairs, recomputing (never
  averaging) the win rate and printing Wilson bounds from both perspectives.
  **Flag ordering is load-bearing:** Go's flag parser stops at the first
  positional argument, so other flags must precede the two paths; a trailing
  flag is rejected with an explanation. Both inputs must put the same
  configuration on side A — the command warns but cannot verify it.

- **Migration 0073 required** (RAG Wave 6) — now the highest migration in
  this Unreleased block. One column, idempotent, **no backfill**: adds
  `agent_decisions.policy_rule smallint` (nullable — which
  `chat_orchestrator_policy` rule, if any, pinned a turn's orchestrator).
  Compose applies it via the `migrate` one-shot service; **Kubernetes does
  not** — run `/app/migrate` out of the release image before
  `kubectl apply`, per `docs/runbooks/release.md`. A release carrying a
  migration has **no one-step rollback**.
- **New `site_config` keys (Wave 6), both GLOBAL-ONLY and both default to
  the empty/no-op value, so no deployment's behaviour changes until an
  operator writes one:**
  - `chat_orchestrator_policy` (default `[]`) — an ordered table of routing
    rules `{when: {...}, orchestrator: drift|longcontext|supervisor|
    plan_execute|plan_execute_dag|agentic|standard, mode: force|prefer}`,
    evaluated after the comparison/team/corpus-table arms and before the
    flag ladder, so a rule can route ANY query type (not only
    `complex_reasoning`, which is all the ladder itself ever dispatches).
    `force` ignores the named orchestrator's feature flag; `prefer` only
    applies when that flag is already on. An empty policy leaves the ladder
    byte-for-byte unchanged (pinned by a frozen-ladder test). Validated at
    save time (`internal/siteconfig.ValidateGlobalValues`); the admin Agent
    panel gained a JSON editor with a rule-preview table. Trajectory event
    `orchestrator_policy`; recorded per turn in the new
    `agent_decisions.policy_rule` column when a rule actually applied.
    `cmd/eval --policy '<json>'` measures a candidate policy against a
    golden set without touching `site_configs`.
  - `chat_answer_tools_by_route` (default `{}`) — maps a route (`lookup` /
    `enumeration` / `complex_reasoning` / `global_synthesis`) to the
    answer-time tool names (14 built-ins only) the catalog is filtered to
    on that route, enforced at both the catalog projection and the
    dispatch boundary (a prompt-injected model can still emit a call for a
    tool hidden from its catalog). Wraps the `ToolDispatcher` interface, so
    it composes structurally — not on any production path today — with a
    per-agent allowlist into the intersection of the two, most-restrictive-
    wins. **A turn with no classified query type (today: a
    transform/reformat follow-up) gets NO answer tools once ANY route is
    configured** — it cannot match a route key by name, so it is treated
    as fully restricted rather than unrestricted. Trajectory event
    `answer_tools_route` (`Decision: "unknown"` for that case).
    **`rag.completion`'s `answer_tools_path` log field changes meaning**:
    it now means "the tool loop actually ran," not merely "tools were
    configured/enabled" — a route restriction or the unclassified-turn
    case can leave `chat_answer_tools_enabled` true while this field reads
    false. Update any dashboard that reads it as a simple flag mirror.
- **`cmd/eval` gains two more flags.** `--policy '<json>'` (above) and
  `--chat-overlay key=value` (repeatable) — a generic per-run overlay for
  any OTHER chat-layer `site_config` key the same reader serves (used to
  re-test `chat_conflict_max_chunks` without a dedicated flag);
  `--chat-overlay chat_orchestrator_policy=…` is rejected in favour of
  `--policy`, which validates.
- **Judge decoder failures now get one bounded retry, not a dropped
  sample.** A JSON decoder failure (a brace-balanced-but-invalid object —
  a raw newline, an unescaped quote, or a trailing comma inside a string;
  not truncation, not fences) re-asks the judge exactly once with the
  decoder's own error appended, localized to the question's language. The
  parser itself is unchanged — this is a retry, not a new tolerance. Every
  attempted retry, success or failure, is recorded in `judge_warnings` as
  `retry:<metric>` and increments the new metric
  `rag_judge_retry_total{judge}`. **Not eval-only:** the same
  `internal/eval.Judge` backs the runtime RAGAS sampler and the in-app /
  scheduled eval runner, so both inherit the retry and the metric too.
  Measured on the 24-question global-synthesis set: 0 retries, 0 remaining
  failures (the baseline being replaced is 2 decoder failures in 384 judge
  calls across the Wave 4/5 measurement runs).
- **`internal/eval.ParseGoldenSetContent` (the admin UI / DB-backed golden
  set path) now accepts JSONL, not only a JSON array.** The shape is
  auto-detected from the first non-whitespace byte (`[` = array, else
  JSONL with `#`/blank-line comments skipped, sharing the same line parser
  `cmd/eval`'s file loader uses), so a set authored as JSONL can be pasted
  or uploaded through the admin UI directly. A row carrying `turns`
  (multi-turn conversations) is still rejected on both shapes — only
  `cmd/eval` can replay one.
- **`FileDates` now threads through `publicapi` and `openaicompat`, not
  only `mcpserver`.** Both surfaces' `ChatContextParams` carry a real
  `FileDateLookup`, but neither is load-bearing yet for conflict
  surfacing: both surfaces deliberately run `PrepareChatContext` with a
  **nil site-config reader** (they read `site_config` for exactly one
  other thing, the degenerate-run-guard limit), so
  `chat_conflict_surfacing_enabled` always evaluates false there and the
  gate can never fire on those two surfaces regardless of the per-KB
  setting. `mcpserver` passes a real reader, so its `FileDates` is
  load-bearing: with the flag on for a KB, `ask_kb`'s supersession
  direction now resolves from real dates instead of always `unknown`.
- **Conflict surfacing re-measured on the fixed detector — still no
  recommendation to enable it, at any cap.** The 8 CERT NEU/UPDATE pair
  questions were rewritten to name the advisory and ask for the delta
  since the first version, with both halves in `must_cite_file_names`. At
  the default cap (`chat_conflict_max_chunks = 12`) both halves ARE in the
  assembled retrieval pool, but the non-cited half sits at score rank ≈15
  — outside the detector's 12-source window — so 0 of 8 pairs flag (a
  **detector-window** finding, not a retrieval finding: the earlier
  hypothesis that MMR discards a half does not hold once measured
  directly). Forcing the cap to its clamp maximum (30) makes all 8 of 8
  flag with the correct direction, but the PPM false-positive rate roughly
  triples (0.281 / 0.303 vs 0.112 / 0.101 at cap 12, two runs each).
  Neither cap passes both pre-registered criteria at once.
  `chat_conflict_surfacing_enabled` stays default OFF and
  `chat_conflict_max_chunks` stays default 12 — **no default change**.
  Record: `eval/golden/cert-recency-de.acceptance.md`.
- **Admin eval-run table gains sortable Team and Score columns, no
  migration.** Both are read out of each run's existing `report` JSONB
  (`eval_runs`, migration 0038) rather than a new column; the team
  selector also shows the last completed run's score next to each team
  name.
- **CI's integration-test package list gained `internal/adminagentmetrics`.**
  The step enumerates packages explicitly rather than globbing, and the
  new `policy_rule` integration test needed adding.
- **Migration 0074 required; `bm25_tiered_boost_enabled` is removed.** The
  key, its per-KB registry row, the keyword-arm CASE it rendered, the
  `--bm25-tiered-boost` eval override and the admin checkbox are all gone.
  0074 deletes any stored row from `site_configs` and `kb_site_configs`; its
  Down is deliberately a no-op. The key shipped default **off** and was
  deprecated in 2026-09 after the Wave-2 A/B measured it net negative on
  every route under `ts_rank` and neutral under `bm25` (the grid in
  `docs/retrieval.md` §"Keyword arm scoring: ts_rank vs BM25 (2026-09)",
  cells B and D — the Wave-3 retune record ran with the boost off
  throughout and is not the retiring measurement), so a deployment that left it
  unset sees no ranking change at all — a deployment that had it **on**
  loses that boost and its ranking changes on upgrade. As with every
  migration-carrying release there is no one-step rollback.
- **`cmd/eval --print-keyword-sql`'s JSON lost its `tiered_boost` field.**
  A documented diagnostic output shape change; the rendered statements also
  no longer carry the `* <boost>` factor (it was the constant `1` with the
  boost off, so scores are unchanged). `eval/fixtures/bm25-scale/time-keyword-sql.sh`
  reads only `executable_sql` and is unaffected.

### Removed

- **`bm25_tiered_boost_enabled` (deprecated 2026-09, Wave 3).** Removed end
  to end: `siteconfig.kbConfigRegistry`, `vector.KBVectorConfig.BM25TieredBoost`
  and its site-config parser, `buildBoostExpr` plus the CASE in both keyword
  scoring modes, the `keyword_arm`/`keywordSQLInput` plumbing,
  `keyword_sql_print.go`'s `tiered_boost` JSON field,
  `cmd/eval --bm25-tiered-boost`, `admineval.snapshotConfigKeys`,
  `pipeline/nodes.go`, the AdminAgentTab checkbox and its two translation
  keys. Migration 0074 deletes the stored rows. The measurement that retired
  it stays in `docs/retrieval.md` §"Keyword arm scoring: ts_rank vs BM25
  (2026-09)" (the Wave-2 grid, cells B and D).

### Fixes

- **Confluence `isPageUpdated` routes through `VersionWhen()`; dead fallback
  layout removed.** No behaviour change for well-formed timestamps (the
  second literal-layout parse was unreachable — `time.RFC3339` already
  accepts the fractional-second component it was trying to catch); a page
  whose `version.when` is unparseable is now logged once per sync and treated
  as unchanged (previously silent, same "unchanged" outcome).
- **Non-streaming chat turns now record an `agent_decisions` row.** The
  non-streaming JSON response path (`writeJSONResponse`) previously recorded
  nothing, leaving every `stream=false` standard-path turn invisible to the
  admin agent-metrics panel. It now shares `recordStandardPathDecision` with
  the streaming standard path, so the mode/outcome/latency computation cannot
  drift between the two. No migration.
- **Admin eval-run table's Score sort moved server-side.** Both list
  endpoints (`GET /api/admin/eval/runs`, `GET /api/kb/{id}/eval/runs`) now
  accept `sort` (`created_at` default | `recall` | `mrr`) and `order`
  (`desc` default | `asc`) query params — validated against a fixed set,
  400 on an unknown value — and order by the run's `report` aggregate
  metrics with `NULLS LAST` (a run with no report, e.g. still queued or
  failed, always sorts last) plus `created_at DESC` as the tiebreak. The
  Score column header now refetches with these params (desc → asc → none,
  resetting to the first page each time) instead of reordering only the
  currently loaded page, which is what the previous client-side sort and
  its "sort applies to the current page only" tooltip were mitigating. No
  migration; the tooltip translation key is removed as unused.

## v0.10.0 — 2026-08-19

### ⚠ Upgrade notes

- **Migration required: `0067_kb_invite_links.sql`** (highest migration in this
  release). Compose applies it automatically via the `migrate` one-shot
  service. **Kubernetes does not** — run `/app/migrate` out of the release
  image as a one-shot pod *before* `kubectl apply`, per
  `docs/runbooks/release.md`. As always, a release carrying a migration has no
  one-step rollback: re-pointing the image tag alone does not undo it.

- **Per-KB workspace toggles are gone.** The `studio_config` mechanism — which
  let an operator switch individual workspace functions off for a single
  public KB — has been removed. Every tile is now available on every KB and is
  gated only by real feature flags (`chat_compare_enabled` for the document
  comparison, and so on). If you had switched tiles off for a KB through that
  mechanism, they reappear after this upgrade. There is no replacement and no
  action to take; the flags are the only gate now.

- **The `knowledge_bases.studio_config` column survives this release unread.**
  No Go code selects or writes it as of v0.10.0. Its `DROP COLUMN` is
  deliberately held back for the *next* release, so that a rollback from
  v0.10.0 to v0.9.0 — whose code still selects the column — keeps working. Do
  not drop it by hand.

- **Two new optional `site_config` keys**, both in the new **Workspace** group
  and both empty by default: `workspace_analysis_presets` and
  `workspace_comparison_presets`. They carry a JSON list of
  `{"label": …, "prompt": …}` entries that pre-fill the prompt field in the
  "Neue Analyse" and "Dokumentenvergleich" dialogs. Empty means the built-in
  presets apply, so no action is required.

- **No `site_config` default was flipped, no re-ingest is required, and no new
  environment variable or database grant is needed.**


### Documentation
- Note the invite-link redemption shadowing gotcha for public KBs (a96ca94)
- Describe KB invite links in the permission-model section (f5526aa)
- Clarify that the owner conjunct is redundant under the current rank order (bfefd22)
- Clarify invite-link cascade test's actual coverage (0e19d87)
- Update the privacy policy to the HRZ version of 2026-07-13 (77475a1)
- Rename is owner-only within the KB permission model (3c5569a)
- Re-point the golden set at the rebuilt KB, record the qwen3-8b baseline (78c1a2c)


### Features
- Offer your own agents' persona prompts as templates (d0b39c1)
- Dokumentenvergleich als Workspace-Kachel, Composer aufgeräumt (0a97867)
- Neue Analyse mit Preset- und Agent-Auswahl (80d953b)
- Gemeinsamer Prompt-Dialog mit Presets und Agent-Auswahl (d6af6c0)
- Comparison summary via selected team/agent, attribution updated (6b7cb25)
- Analyse über gewählten Agent/Team, fail-soft mit sichtbarem Grund (1485959)
- Prompt-Presets mit KB-Override und Preset-Endpoint (0d00a00)
- Mobile Tab-Leiste auf Verlauf/Chat/Workspace/Quellen (27259d6)
- Vereinter Verlauf links, Studio-Hälfte der Seitenleiste entfällt (a269a43)
- Workspace-Reiter statt Studio-View, Quellenleiste berechnet ausblenden (bb4547e)
- Show lastUsedAt on each invite-link row (ffbbb13)
- Include the invite-link id in create/redeem audit entries (1a0fac2)
- Redeem invite links after sign-in and open the KB (0ea56e9)
- Capture /join/<token> links across the login round trip (d7e9da5)
- Invite-link tab in the members modal (d51c44c)
- Wire invite-link routes with a dedicated rate limiter (ba3fd0d)
- HTTP handlers for invite-link create/list/revoke/redeem (8e016f5)
- Redeem invite links, raising membership without ever downgrading (c195baa)
- Invite-link store with token minting, create/list/revoke (a3b2e4e)
- Add kb_invite_links table and cascade wiring (1fc1303)
- Add terms of use to the footer legal pages (d689b3c)
- Rename KBs from the Home card and the workspace header (9a12c50)
- Owner-only rename gate on PATCH /api/kb/{id} (f368bed)


### Fixes
- Bueroklammer statt Waage fuer den Dokumentenvergleich (51e4dab)
- Zurueckknopf von den Quellen in die KB-Kopfleiste (d68111e)
- Regenerate an answer as a sibling, not a second question (904c25e)
- Date awareness, empty output, and the unguarded arms (13854e9)
- Give the dialog buttons the house shape (49869e8)
- Open generated content once it is ready (bc18416)
- Refresh the history when a research stream ends (45fdc84)
- Stop truncating research findings in the messages API (acd268b)
- Wire onStartComparison's return value through ChatView (0770a42)
- Keep the comparison attachment alive for follow-up turns (822b804)
- Final review fix wave for the UI workspace rework (b71659a)
- Carry comparison agent/team selection through send opts (133a8d9)
- Revert plain comparison prompt, exclude team-authored answer tools, cover call-site wiring (4febdfa)
- Analysis agent path uses the analysis prompt, cover run_failed + answer provenance (669ae5e)
- Cover KB-override wiring, log load failures, fix log noise (7b4a698)
- Gofmt struct-tag alignment and drop stale studioConfig from OpenAPI schema (81e5126)
- Derive mobile active tab from kbView; add swipe + mobile-branch tests (197fb06)
- Stale-chat reselect, sidebar-wiring/delete-branch/order test gaps, history label keys, mobile border (f94a8cb)
- Sichtbarkeits-Test und CSS-Spezifizität für SidebarShell (fa08bb6)
- Bound invite-link retries and keep the token out of the console (b5f81f4)
- Redact the invite token from OTel span attributes (5ffb549)
- Redact invite-link token at every remaining log sink (6ab8f9c)
- Distinguish invalid invite links from retryable redemption failures (8a7e5f2)
- Redact invite-link tokens from logs, metrics, and traces (3c3c454)
- Merge only the name after a KB rename, add title + 255-char guard (7ac5ca1)
- Propagate FileName in the retrieval-only search adapter (084a116)


### Refactoring
- Make the team tool policy opt-in; drop studio_config column (4535581)
- TeamParams-Aufbau als BuildTeamParams extrahieren (e38f170)
- Studio_config als Mechanismus entfernen (Spalte bleibt bis Cleanup-Release) (42b19e0)
- Workspace ohne eigene Artefaktliste, Auswahl kommt aus dem Verlauf (12f95c8)
- Quellen als SourcesPanel in die rechte Spalte (dbfc8f6)
- Seitenleisten-Rahmen als SidebarShell extrahieren (1e429b7)


### revert
- Pull the studio_config drop out of this branch (4b52063)

## v0.9.0 — 2026-08-18

### ⚠ Upgrade notes

- **Migration 0066** adds `usage_events` and backfills it from message history.
  The backfill scans `messages` in one statement; on a large corpus expect the
  migrate step to take proportionally longer than usual.
- **Message counts drop by roughly half** on the home KB cards and in the admin
  KB overview. They previously counted `COUNT(messages)` — user *and* AI rows —
  so a 4-turn chat read as 8. They now count turns. No data was lost.
- **Historical `/api/v1` traffic is attributed to `web`.** `messages` carries
  nothing that separates the two surfaces, so the backfill cannot distinguish
  them. Attribution is exact from this release onward. Historical OpenAI-compat
  and MCP traffic cannot be recovered at all — it was never persisted.
- `GET /api/admin/kb-overview` no longer returns `messageCount`; it returns
  `webTurns`, `apiTurns` and `lastTurnAt`.
- The KB list endpoints — including **`GET /api/v1/kb`**, the key-authenticated
  external surface external scripts pin — return `turnCount` / `lastActivityAt`
  instead of `messageCount` / `lastMessageAt`.
- **Research sessions (`chats.type='research'`) are not counted**, in either
  the migration 0066 backfill or the live recording path. An operator
  reconciling spend against the LLM gateway's own usage view should expect
  LLM-heavy research runs to be absent from this ledger.

### Documentation
- Usage ledger, migration 0066, and upgrade notes (6251e93)


### Features
- Home KB cards count turns from the usage ledger (bd1e464)
- KB overview Aktivität column and API-aware Letzte Aktivität (b948186)
- Read Aktivität from usage_events, not messages (016cca6)
- Total and 24h turn counts with the API share (0b262b6)
- Record one usage event per ask_kb call (4720d24)
- Record one usage event per accepted turn (f8c6f77)
- Record one usage event per accepted /api/v1 turn (5738eed)
- Record one usage event per accepted web turn (4c10eb7)
- Expose the authenticating API key id on the request context (de6a6f8)
- Usage.Recorder writing one usage_events row per accepted turn (c7d8513)
- Usage_events ledger + backfill of historical web turns (a378f30)


### Fixes
- Record turns strictly after acceptance on all four surfaces (357406e)
- Scope TestBackfill_IsIdempotent's count to the fixture KB (2d3edf6)

## v0.8.0 — 2026-08-17

### ⚠ Upgrade notes

- **A KB admin can now bind an agent or team they did not create — provided
  it is already attached to that KB.** `PUT /api/kb/{id}/agents/{agentId}` and
  `PUT /api/kb/{id}/teams/{teamId}` have always been on `kbAdminChain`, but the
  handlers additionally required the caller to *own* the agent and answered 404
  otherwise. A KB admin who had not created it therefore could not bind it —
  and, because clearing a default goes through the same endpoint with
  `isDefault:false`, could not remove one someone else had bound either. The
  new rule is: **the caller owns the agent/team, OR it is already attached to
  this KB.** Three answers, in order — 404 if it does not exist, 403 if it
  exists but is neither yours nor attached here, otherwise the write.
  **What this deliberately does not allow:** binding an arbitrary agent by id.
  The link row *is* the use-time authorization (chat-time resolution checks
  attachment, never ownership), so a bind makes that agent's persona, model
  override and config overrides run for every viewer of the KB, with its owner
  given no notice and no veto. Tool escalation is separately impossible — the
  per-agent allowlist is re-enforced at dispatch time and privileged tools are
  stripped unless `agents_allow_privileged_tools` is on. The listing endpoints
  (`GET /api/agents`, `GET /api/agent-teams`) stay owner-scoped. **Detach is
  unchanged** and was never owner-scoped: it can only remove a link on the KB
  you already administer.
- **A default agent/team that is switched off is now visible in the KB's
  workflow view.** `agent_kb_links.is_default` survives the agent being
  disabled, and the workflow canvas was reading the chat picker's
  `is_enabled`-filtered list — so such a KB showed „keine Vorgabe", the row
  could not be cleared, and it took effect again as soon as anyone re-enabled
  the agent. The canvas now reads a separate query that includes disabled
  entries and marks them. **No behaviour change in chat:** a disabled agent
  remains unselectable there, and a KB with a disabled default still answers
  with the standard path — which is what the canvas now says it does.
- **A default team with no active member is no longer drawn as if it answered.**
  A team can be attached, flagged default and switched *on*, and still take no
  turn at all: chat-time resolution counts only its **enabled** members, and
  zero members means the selection is dropped and the normal pipeline runs. (A
  team reaches that state by having its members switched off, or by being
  created with no members — the API accepts that.) The workflow canvas read
  only the team's own on/off flag, so it drew such a KB as fully bound: the
  binding node „aktiv", the agent/team orchestrator projected, and CRAG,
  context compression and the sufficient-context gate demoted to „bedingt" on
  every lane — for a binding that could never fire. The canvas now reports it
  as inactive with its own reason („keine aktiven Mitglieder", distinct from
  „abgeschaltet", because the remedy differs) and nothing below it is demoted.
  No behaviour change in chat.
- **The KB settings „Agenten & Teams" tab lists everything attached to the KB,
  not only your own.** It rendered the caller's agents and teams alone, so on a
  KB whose default is a co-admin's agent there was no row for it: no default
  shown, no way to detach — while the Workflow tab, one click away in the same
  panel, named it. Entries you did not create are listed, attachable,
  detachable and bindable, and marked as not editable here.
- **A KB's default agent/team applies in the web UI only — and binding one
  turns off the standard retrieval path for that KB.** Two separate facts,
  both worth knowing before you set a default:

  *Reach.* The default is applied client-side: the web UI seeds a new chat's
  selection from it and then sends that selection with every message. The
  server never consults the binding on its own, so the public API
  (`/api/v1/*`), the OpenAI-compatible layer, the KB-as-MCP endpoint and any
  non-streaming `POST /api/kb/{id}/chat` ignore it entirely and answer as if
  no default were set. This asymmetry **predates the workflow editor** — it is
  how `is_default` has behaved since it shipped — and is documented here
  because the workflow canvas now makes the binding visible for the first
  time. The canvas says so on the node.

  *Consequence.* The selection rides on every message of a chat that started
  with it — not just the complex ones — so a bound KB routes those turns,
  simple lookups included, through the agent/team orchestrator. That path does
  not run the standard retrieval pipeline, so CRAG, context compression and the
  sufficient-context gate do not execute on such a turn regardless of their
  flags. Nothing silently degrades — the specialists still retrieve — but if
  you rely on those three, do not bind a KB-wide default. Three kinds of turn
  are **not** affected and still run the normal pipeline: chats that already
  existed before the default was set (they carry their own stored selection,
  which is empty), „Verbessern"-turns, and any non-streaming request. The
  workflow canvas draws exactly this: on a bound KB those stages render
  *bedingt* — conditional, not off — rather than *aktiv*, on every lane.
- **The KB settings surface now requires an operator system role.** Reaching
  `KbSettingsPanel` — RAG Settings, Agenten & Teams, Evals, Workflow — used to
  need only KB role `admin`, which every KB owner has. Since every ordinary
  user who creates a KB owns it, that made all of them operators of the 52-key
  per-KB `site_config` registry (three of whose keys force a full re-ingest),
  of eval runs, and of the workflow presets that rewrite how a KB answers. The
  new `kbAdvancedChain` requires a system role in **`api-user`, `admin` or
  `superadmin`** *in addition to* KB role `admin`, on 22 routes: settings
  read/write/delete, workflow projection, preset preview and apply, the eval
  routes, agent/team attach/detach, and `POST /api/kb/{id}/reembed`.
  **Who loses access:** any user with system role `user`, including on KBs
  they own — they keep the KB, its files, its chats, sharing and member
  management, and lose only the tuning surface. If someone in your deployment
  legitimately tunes their own KB, promote them to `api-user`, which exists for
  exactly this and grants no admin UI. `kbaccess.EffectiveRole` and the KB-role
  ladder are unchanged; this is layered on top. `GET /api/kb/{id}/agents` stays
  on the view chain so the chat agent picker keeps working for everyone.
- **`package.json` and the k8s worker image pins were stale and are corrected
  here.** Both sat at `0.6.1` through the v0.7.0 tag — runbook steps 4 and 5
  were missed when that release was cut, so `git checkout v0.7.0 && kubectl
  apply -f k8s/` deploys **v0.6.1** workers. The tag is immutable, so v0.7.0
  cannot be repaired; deploy the workers from `v0.8.0` (or pass the image
  explicitly) if you are on that path.
- No migration (schema level stays at `0065`), no `site_config` default
  change, no re-ingest, no new env vars.


### Documentation
- Clarify that Editable says nothing about a node's other keys (940686a)
- Explain why agent configs are not conflict-checked at save time (8a59352)


### Features
- Bind a KB's default agent/team from the canvas, and confirm presets honestly (3afc517)
- Project the KB's default agent/team, and stop overstating a preset apply (b647f11)
- Add the preset picker with an honest overwrite confirmation (add623b)
- Apply presets atomically, preview overwrites, report deviations (e44ed6c)
- Price presets by projecting their own bundles (db75f68)
- Register workflow_preset and ship its reader alongside it (95447cf)
- Add curated workflow presets with a validity guard (cb7d602)
- Save, reset and refetch from the workflow canvas (dfde2f9)
- Make the workflow node inspector editable (cec36c7)
- Add the workflow node field control (afd2e1c)
- Add a safe field lookup helper for the mostly-unregistered key space (ea6b6e5)
- Add workflow field metadata types and re-export the settings writers (74da7c3)
- Ship registry field metadata with the workflow projection (ea4e89e)
- Register the drifted retrieval and orchestrator keys (f40e634)
- Make the verification and correction stages per-KB configurable (2f974e1)
- Add the Workflow tab to KB settings (ef6b141)
- Add the read-only workflow canvas with keyboard-accessible node activation (907b50f)
- Add the workflow node inspector panel (4466f50)
- Add the workflow canvas node component (c1e34f9)
- Add dagre layout for the workflow canvas (6976d56)
- Add workflow graph types and API client (8691bf0)
- Expose GET /api/kb/{id}/workflow (ea36218)
- Report per-key value origin (kb vs global vs default) (258c7c7)
- Project the node vocabulary against a KB's resolved config (e98cfcc)
- Add node vocabulary and static topology (3a984f6)


### Fixes
- Require an operator system role for the KB advanced-settings surface (a1303ee)
- Seed the tester user from TESTER_PASSWORD (5fa3edf)
- Hedge the cache claim and pin the tool-aware planner off (8ce5743)
- Honest error paths, reachable draft on load failure, single field-type mirror (0068e47)
- Derive the inspector from the live graph so a save no longer appears to revert (35395ea)
- Withhold Reset where the server would reject the DELETE (4cf350d)
- Narrow the citation-validation exclusivity claim to Self-RAG (d3311a9)
- Drop the structurally-global router key and correct three cost/gating claims (e3cb503)
- Enforce mutual-exclusion conflicts on the per-KB save path (1c03f03)
- Disclose the tool-aware DAG dependency and register the answer-tool round cap (6ea41d7)
- Correct the refine cost and regroup the context gate (0929aa4)
- Make the workflow canvas usable, accurate and reachable (b0d3093)
- Anchor node lookup to the RF wrapper, refit on lane switch, split reasonLabel (87a719b)
- Explain disabled stages, fail visibly on unknown origin, wrap long values (360ddb0)
- Use the shadow token and retarget the node focus ring to React Flow's wrapper (8b5eec7)
- Model orchestrator bypass, split factuality verifier, guard defaultOn (9472573)


### Refactoring
- Make the workflow endpoint's JSON camelCase throughout (3220f16)
- Dispatch through SelectOrchestrator instead of the inline ladder (5081b35)
- Extract orchestrator precedence into SelectOrchestrator (19e34ec)

## v0.7.0 — 2026-08-14

### ⚠ Upgrade notes

No migration (schema level stays at `0065`), no `site_config` change, no
re-ingest, no new env vars. Rollback by re-pointing the image tag works.

- **Citation page labels get narrower, and that is the fix, not a regression.**
  A citation now names the page of the chunk the retriever actually matched.
  Previously neighbour expansion merged the page numbers of the surrounding
  `context_window_size` chunks into the matched chunk's metadata, so at the
  default window of 3 a hit on page 8 was labelled with all seven chunks'
  pages — users saw `S. 8-16` for a passage living on one page. The stitched
  neighbour text still reaches the answer model unchanged; only the page
  attribution narrows. The source card's "open in document" jump now lands on
  the matched page rather than the neighbourhood's first.
- **No re-ingest.** The correct per-chunk page was always stored; only the read
  path discarded it. This holds for Docling-parsed PDFs too, whose pages come
  from item provenance through the same per-page split. The one exception is
  data ingested by **pre-v0.1.0 builds**, which baked wrong page numbers into
  chunk metadata before the Docling provenance fix — those PDFs still need a
  re-ingest, and this release makes their labels narrower but no less wrong.
- **The OpenAI-compat citation fields are purely additive.** `annotations[]`
  and `context.citations[]` are omitted rather than emitted empty, and unknown
  fields are ignored by OpenAI SDKs, so the ILIAS and OpenWebUI integrations
  need no change. That endpoint runs no citation validator, so its annotations
  reflect what the model claimed rather than what was verified — see `API.md`.

### Features
- Return retrieved sources as citations (b56e9d2)


### Fixes
- Cite the matched chunk's page, not the neighbourhood span (81c7cf6)

## v0.6.1 — 2026-08-14

### ⚠ Upgrade notes

Security-only release. No migration (schema level stays at `0065`), no
`site_config` change, no re-ingest, no new env vars, no behaviour change —
the only difference is the Go standard library the binaries are built against.
Rollback by re-pointing the image tag works.

- **Deploy this instead of v0.6.0.** `:v0.6.0` *was* published (unlike v0.4.0
  — `docker-image.yml` carries no `needs:` on the CI workflow, and `ci.yml`
  does not even trigger on tags, so a red CI run does not block the image),
  but it was built with Go 1.26.5, whose standard library carries seven
  advisories that `govulncheck` reports as reachable from called code:
  `GO-2026-6218` (net/url), `GO-2026-6091` (html/template), `GO-2026-6090`
  (crypto/tls), `GO-2026-6089` (net/http), `GO-2026-6088` (encoding/xml),
  `GO-2026-5972` (encoding/asn1), `GO-2026-5026` (x/net/idna, vendored into
  net/http). The `toolchain` floor in `go-backend/go.mod` moves to
  `go1.26.6`; the `go` directive stays at the 1.26 language baseline.
- **No dependency versions changed.** `govulncheck` still counts six uncalled
  findings — excelize, go-ntlmssp, x/net, x/crypto, klauspost/compress,
  quic-go — none of which our code reaches, so they do not gate CI. They are
  left for a deliberate dependency round rather than folded into a security
  patch.
- **`golangci-lint` stays pinned at v2.12.2.** The go.mod comment's warning to
  bump it alongside the toolchain is about the Go *minor*; a patch bump within
  1.26 does not change how its staticcheck analyses the stdlib (verified
  locally: 0 issues).

### Fixes
- Bump the Go toolchain floor to 1.26.6 for seven stdlib advisories (7c8bcfe)

## v0.6.0 — 2026-08-14

### ⚠ Upgrade notes

- **No migration.** Schema level stays at `0065`. No `site_config` defaults
  change, no re-ingest, no new env vars. Rollback by re-pointing the image
  tag works.
- **Behaviour change in the public-KB overview:** an explicit
  `kb_subscriptions.state = 'opted_out'` row now hides a public KB from the
  overview for **everyone**, including a caller who holds a `kb_members` row
  on it. Previously membership bypassed the opt-out unconditionally. This is
  what makes the star on a Favoriten card work for curators and for the
  demoted ex-owner of a published KB. No data migration is involved — the
  rows already existed, only the query changed. Operator-visible effect: a
  KB admin who un-favorites their own KB no longer sees it in the overview
  until they re-add it from "KBs entdecken"; their membership, permissions
  and chats are untouched.
- **`GET /api/kb/catalog` now also returns *staged* public KBs (`visibility =
  'public' AND is_published = false`) to callers holding a `kb_members` row on
  them.** It previously returned published KBs only. Nothing new is exposed:
  staged KBs were already reachable by their members through
  `GET /api/kb/global` and every `view`-gated route, and non-members still do
  not see them in the catalog. This arm is what keeps un-favoriting a staged
  KB reversible.
- **`PUT` / `DELETE /api/kb/{id}/subscription` no longer answer `409` for a
  staged public KB.** They still answer `409` for a private KB. Only a member
  reaches the view-gated route for a staged KB, and their subscription row is
  what the favorites toggle writes.
- **New endpoint `GET /api/kb-categories`**, authentication-only, serving the
  same flat taxonomy as the existing system-admin `GET
  /api/admin/kb-categories` (same handler, read-only). The category list is
  not sensitive — every catalog row already carries its `categoryIds` — and
  behind the admin route the discovery filter was invisible to ordinary
  users. The admin CRUD routes are unchanged and stay system-admin-only.
- **Removing a public KB from Favoriten no longer deletes chats.** If you
  relied on the star as a "leave this KB" action, that action now lives only
  in the members dialog; the star is a favorites toggle and touches neither
  membership nor chat history.

### Features
- Category tabs and a folded grid in KB discovery (7d86d49)


### Fixes
- Make removing a KB from Favoriten harmless and reversible (5b45d7c)
- Label the chat source counter passively (3b78c8f)

## v0.5.0 — 2026-08-13

### ⚠ Upgrade notes

- **No migration.** Schema level stays at `0065`. No `site_config` defaults
  change, no re-ingest, no new env vars. Rollback by re-pointing the image
  tag works.
- **New endpoint `GET /api/kb/{id}`**, view-gated through `kbViewChain`. It
  returns one KB in the shape the list endpoints already produce and grants
  nothing new: `RequireKBRole(view)` is the whole access decision, so a
  caller can read exactly the KBs they could read before. A KB the caller
  may not see answers 403 (from the chain); a KB that does not exist answers
  404.
- **The product name in the UI is now "JLU RAG"** — browser title, meta
  description, PWA manifest `name`/`short_name` and the onboarding welcome
  text. The manifest change means installed PWAs will show the new name after
  the service worker updates. The privacy policies under
  `web/public/legal/` and the `langfuse_base_url` admin tooltip still say
  "JustRAG"; renaming a published privacy policy is a legal decision and was
  left out on purpose. Repository, module path, image name and this
  changelog's preamble are unchanged.

### Features
- Render discovery results as KB cards with a favorites toggle (630a1f2) —
  "KBs entdecken" now uses the same tile as the other three overview
  sections, with a star toggle for adding to Favoriten. The cards carry no
  chips or "last active": `GET /api/kb/catalog` is a thin projection, and
  enriching it was weighed and declined in favour of keeping the list light,
  so the detail row is fetched on click instead.
- Add a view-gated single-KB read endpoint (e5a015d)

### Chores
- Rebrand the product name to "JLU RAG" (84a4b00)

## v0.4.1 — 2026-08-13

### ⚠ Upgrade notes

Test-only release. No migration, no `site_config` change, no re-ingest, no
runtime behaviour change — the application code is byte-identical to v0.4.0.

- **Deploy this instead of v0.4.0.** The web test job failed on the v0.4.0
  tag, so its CI run did not complete and no `:v0.4.0` image was published.
  `:v0.4.1`, `:v0.4` and `:stable` come from this tag.

### Tests
- Isolate the overview tests from persisted accordion state (3c1c8d7) —
  `KbAccordion` persists each section's open state, so a test that expanded a
  section decided the starting state of the ones after it. The local run hid
  it: this machine's jsdom exposes `localStorage` as a bare object with no
  `getItem`/`setItem`, so the accordion's `try`/`catch` fell back to its
  default on every render. The file now installs its own in-memory `Storage`
  per test and pins that a remembered section survives a remount, so an inert
  stub fails loudly instead of quietly weakening the file.

## v0.4.0 — 2026-08-13

### ⚠ Upgrade notes

Follow-up round on the Phase-2 visibility model: the overview becomes
"Favoriten" and gains discovery, shared and own sections as accordions.

- **No migration.** Schema level stays at `0065` (unchanged since v0.3.0). No
  `site_config` defaults change, no re-ingest, no new env vars. This release
  *is* rollback-able by re-pointing the image tag — v0.3.0 is not.
- **System admins are now subscription-filtered in the overview.** `GET
  /api/kb/global` previously returned every public KB to a system admin,
  regardless of subscription; it now applies the same rule as for everyone
  else (a `kb_members` row, an explicit subscription, or `auto_subscribe`
  without an opt-out). An admin who saw *every* public KB in their overview
  will now see fewer. Nothing became unreachable: the full list is in
  Admin → Globale KBs and in the "KBs entdecken" section, and subscribing
  there puts a tile back. Migration 0065 backfilled `auto_subscribe=true` for
  everything that was global *and* published before v0.3.0, so on a
  deployment upgraded through that path most tiles stay. Staged (public but
  not yet published) KBs are the exception — they are reachable only by their
  members, which is what staging means.
- **The OpenAI-compat / ILIAS surface is unaffected** — `internal/openaicompat`
  still lists every public KB for an admin caller.
- **Creating a global KB now enrols the creating admin as a KB admin**
  (`kb_members` `role='admin'`). Required by the filter above, or a
  freshly created public KB would be invisible to its creator. Pre-existing
  public KBs are untouched; add curators through Admin → Globale KBs →
  Editors as before.
- **Bookkeeping fix from v0.3.0.** That release was tagged without running
  steps 2, 4 and 5 of `docs/runbooks/release.md`: its changelog section was
  left headed `## Unreleased`, `package.json` stayed at `0.2.0` and
  `k8s/worker-*.yml` stayed pinned to `:v0.2.0`. All three are corrected here
  (`package.json` jumps `0.2.0` → `0.4.0`). The **tag `v0.3.0` is immutable
  and still carries the stale pins** — `git checkout v0.3.0 && kubectl apply
  -f k8s/` deploys v0.2.0 workers. Deploy v0.4.0 rather than v0.3.0 from the
  manifests, or override the image on the command line.

### Documentation
- Record the favorites overview and the admin subscription filter (f06d48f)


### Features
- Allow granting KB admin when sharing, and label every role picker (555dfc9)
- Rebuild the overview as favorites, discovery, shared and own (678dfa5)
- Subscription-filter the public KB overview for admins too (6cdbc95)


### Fixes
- Make category assignment removable and widen the admin KB overview (cade01f)


### Uncategorized
- JLU design guide upgrade (f5038d4) — not in conventional-commit form, so
  git-cliff skipped it; recorded by hand.

## v0.3.0 — 2026-08-13

### ⚠ Upgrade notes

- **Migration 0065** is required and **cannot be rolled back by re-pointing the
  image tag** (`cmd/migrate` is up-only). Under k8s, run the migrate step
  before `kubectl apply` — see `docs/runbooks/release.md`.
- `knowledge_bases.is_global` becomes a **generated column**. Any external tool
  that writes it will fail with SQLSTATE 428C9; write `visibility` instead
  (`'public'` / `'private'`). Reads are unaffected.
- Existing global KBs are backfilled to `visibility='public'`, and those that
  were also published get `auto_subscribe=true` — every user keeps seeing
  exactly what they saw before.
- **Newly** published public KBs default to `auto_subscribe=false`: they are
  discoverable in the catalog but appear in nobody's overview until a user
  subscribes, or an admin enables the flag.
- Making a KB public is **staged**: `POST /api/admin/kb/{id}/publish` (now
  reachable from the admin KB-Übersicht) sets `visibility='public'` *and*
  `is_published=false`, so the KB is visible only to its KB admins and to
  system admins until an operator publishes it in the global-KB tab. Existing
  rows are untouched; this only affects KBs published from this release on.

## v0.2.0 — 2026-08-12

### ⚠ Upgrade notes

Phase 1 of the four-role KB permission model. `kb_members` becomes the single
authority for KB access; `knowledge_bases.user_id` survives only as a
trigger-maintained mirror.

- **Schema level:** migrations through `0064` (`go-backend/migrations/main/`).
- **This release has no one-step rollback.** `0064` creates `kb_members`,
  backfills it from `knowledge_bases.user_id`, `knowledge_base_shares` and
  `global_kb_editors`, and renames `pending_kb_invites.permission` → `role`.
  Its `Down` section deliberately does not reverse the rename, and
  `cmd/migrate` is up-only — re-pointing the image tag is **not** sufficient.
  See [`docs/runbooks/migration-rollback.md`](docs/runbooks/migration-rollback.md).
- **Do not split this release across a partial deploy.** The `/api/kb/{id}/share*`
  endpoints and the frontend that called them are removed in the same tag. An
  older image served against a `0064` database would ship a UI calling five
  endpoints that no longer exist.
- **Two intentional behaviour changes an operator will notice:**
  - Plain users can now reach the settings of KBs they own or administer. The
    former `kbTuningChain` additionally required the *system* role
    `api-user`/`admin`/`superadmin`, which locked users out of their own KBs.
    The new `kbAdminChain` gates on the KB role `admin` alone.
  - Unpublished global KBs are no longer readable by every authenticated user.
    The old middleware granted `view` on any `is_global` KB regardless of
    `is_published`, across the whole view chain — chat, files, graph, studio,
    generated content, the public API and MCP included. They are now reachable
    only by their members and by system admins.
- **Backfilled curators become visible, not new.** Every pre-existing
  `global_kb_editors` row was backfilled as `kb_members.role='admin'` and now
  appears in the global-KB editor panel, which previously read a table the
  access check no longer consults. Nothing was granted; prior state became
  visible. To review what exists:

  ```sql
  SELECT kb_id, user_id FROM kb_members m WHERE role = 'admin'
    AND EXISTS (SELECT 1 FROM knowledge_bases kb WHERE kb.id = m.kb_id AND kb.is_global);
  ```

- No `site_config` defaults change. No re-ingest required. No new env vars.
- Phase 2 (visibility enum, system user, subscriptions, category catalogue) is
  **not** in this release.

### Documentation
- Correct the Phase 1 KB-role claims and document the /members surface (fb25e3d)
- Document the four-role KB permission model (675b971)


### Features
- Replace share modal with four-role members dialog (fa923be)
- Contextual remove/delete action driven by the caller's KB role (8496591)
- Promote pending invites into kb_members, unify owner transfer (b7a6cfa)
- Add member management endpoints and owner transfer (2066fe0)
- Add member store with owner invariants and self-leave (a3bfb0d)
- Resolve effective KB role from kb_members (7acd140)
- Add ordered KB role constants (53312d7)
- Add kb_members table with role backfill and owner mirror trigger (cf93ded)


### Fixes
- Repoint the global-KB editor surface at kb_members (82eee0f)
- Restore pending-invite revocation on the /members surface (e9a5ecc)
- Guard KB removal against re-entry, fix vacuous test assertion (3b94bc8)
- Guard membership-impact and owner-transfer against non-members (fc863d8)
- Enforce LeaveKB owner-immutability in SQL, not a racy Go pre-check (622dcc5)
- Write the owner kb_members row on KB creation (81e928b)


### Refactoring
- Gate settings surface on KB admin role, drop kbTuningChain (9a4f83a)

## v0.1.0 — 2026-08-12

### ⚠ Upgrade notes

First tagged release — the baseline for every later entry.

- **Schema level:** migrations through `0063` (`go-backend/migrations/main/`).
  A fresh install applies all of them via `cmd/migrate`.
- Pre-versioning builds (`:latest` images published before this tag) are not
  a supported upgrade source. Deploy `v0.1.0` and run `cmd/migrate`.
- No `site_config` defaults change with this release.

### Documentation
- Add technical documentation, feature configuration recipes, and runbooks (a58ba35)
- Migrate detailed feature enablement recipes from CLAUDE.md to separate document file (dd6af0a)
- Update CLAUDE.md to reflect qwen3-embedding-8b as the default production model (7900dbc)


### Features
- Make the agent surface reachable and consistent (9a12b6d)
- Superadmin KB actions, pending invites at login, stable chat scroll (b9169ba)
- Increase system prompt character limit from 4000 to 8000 (6ad61f8)
- Increase maximum system prompt length to 8000 characters (e94df26)
- Enable agent team attribution and evaluation by updating storage schemas and telemetry pathways (30e0410)
- Implement agent/team management framework with sticky session selection and per-KB assignment logic (856bc9b)
- Support MRL embedding truncation via configurable dimensions with cache isolation and validation (45a737b)
- Add recency listing capability to query recently ingested documents deterministically (4a1d102)
- Include precomputed date anchors in system prompt to improve relative time resolution accuracy (9804e63)
- Truncate rerank documents to context window to prevent 400 errors (3813bb8)
- Implement date-aware system prompts and introduce recent_documents MCP tool to improve query context and temporal filtering. (e6ac2e8)
- Implement mind map graph export functionality with support for JSON, CSV, and GraphML formats, including security updates to CSP and tests. (1779d16)
- Exempt configured egress proxies from private IP SSRF restrictions (365a873)
- Add WID advisory enrichment support to RSS poller with structured parsing and formatting (2708c88)
- Add git_repo_enabled to the list of allowed site configuration keys (6b82d85)
- Add polling for KB processing status and fix message container overflow layout issues (3c9a6a5)
- Enable Git repository indexing with site configuration gating and implement comprehensive UI design system updates (be83fbf)
- Implement Git repository indexing support and add customizable answer temperature settings (40cecfb)
- Pass reasoning effort to AI stream and tool-answering pipelines to enable thinking mode via template kwargs (faab4c1)
- Add answer export menu for downloading as Markdown, PDF, or DOCX with citations (b6e71ed)
- Implement full iterative DRIFT orchestrator for global-synthesis queries (6d8b2d9)
- Implement KG community detection and summarization (720e077)
- Implement triple filtering logic for knowledge graph refinement (63a15b0)
- Implement query-scoped mindmap functionality - Added support for displaying a mindmap scoped to specific AI answers. - Introduced a View graph button in MessageContent that triggers the mindmap view for the corresponding message. - Enhanced MindMapView to fetch and display a subgraph based on the provided message ID. - Implemented NodeSourcesPanel to show deduplicated sources for nodes in the mindmap. - Updated translations to include new terms related to the mindmap feature. - Added integration tests for the scoped graph functionality in the backend. - Refactored existing components and styles to accommodate the new features. (1458757)
- Implement centralized file extension allowlist for frontend uploads and backend ingestion validation (81b144c)
- Add support for bulk user invitation to knowledge bases and implement MCP server handler infrastructure (7a124cf)
- Inject Docling image captioning credentials dynamically from backend AI provider configuration (9bd53f0)
- Add docling image captioning and table configuration support with new Kubernetes deployment. (0016274)
- Add support for fetching and storing full RSS feed content via configurable per-feed setting (a55d718)
- Implement granular ingestion stage tracking and UI indicators for active file processing (fb71d44)
- Embed cl100k_base vocabulary to enable offline tokenizer initialization (50fefed)
- Add configurable StuckFileTimeout to worker maintenance to identify stalled processing tasks (337a617)
- Implement real-time mindmap updates via SSE and per-file KG linkage tracking (23bb864)
- Implement in-chat document comparison with Redis-backed attachment storage and structured analysis modes (ffccf3f)
- Implement in-chat document comparison with attachment management and LLM-powered findings generation (e9ea8ae)
- Implement chart generation with hybrid SQL-tabular and LLM-context fallback paths (cdef678)
- Implement interactive quiz component and mind map visualization with expanded knowledge base artifact support (1659a52)
- Add styled .xlsx export capability to generic Markdown tables and StructuredTable views using ExcelJS (d8a9cb2)
- Add chat answer history and transform follow-up capabilities with admin configuration (a3d6058)
- Add error reporting fields to file model with persistent tracking and clearing logic (b7816b1)
- Introduce 90s timeout middleware for synchronous LLM routes and add processor integration testing infrastructure (2bbcae4)
- Introduce JSONAPIErrors middleware to standardize 404/405 API error responses, sanitize error messages in handlers, and clean up redundant type conversions. (966031f)
- Enforce read-only transactions on read-only pool and increase Trivy scan timeout (0eac9ea)
- Add idle timeout to SSE parser, implement task error handling in worker, instrument dimension mismatches, and add site configuration conflict validation. (927d32b)
- Add configurable embedding batch size, optimize text file parsing, and enable concurrent KG extraction. (1701274)
- Add advisory locks for migrations and implement robust database pool configuration validation (b6d7f0e)
- Implement automated structured data extraction and comparison for corpus queries with UI visualization (e3e7719)
- Add routing accuracy evaluation, implement reranker score-drop thresholds, and integrate Self-RAG verification types with UI support. (efbb8f9)
- Add govulncheck to CI, pin toolchain, update security dependencies, and add proactive auth/database hardening validations (1d6aea9)
- Optimize database vector operations by implementing binary pgvector codec in pgx pool connections (8b3f977)
- Populate FinalChunks in Agentic, PlanExecute, and Supervisor chat responses to ensure retrieval metric consistency (b326f2f)
- Add migration phase logging and validate vector table names for SQL safety (7908e24)
- Implement image description endpoint with configurable model settings (e7d0248)
- Implement multimodal support for AI chat and harden S3 bucket startup preflight logic (0a11cd1)
- Implement online retrieval feedback loop and admin endpoint for negative chunk identification (55785cc)
- Implement per-KB site configuration overrides and KB-scoped evaluation golden sets (c71b5d8)
- Implement automated golden-set generation with multi-hop reasoning capabilities and database job management (066ce00)
- Implement HyPESearch flag across chat orchestrators and retriever modules for query-time search control (1008f08)
- Implement HyPE (Hypothetical Prompt Embeddings) for improved retrieval by matching query embeddings against generated hypothetical question indices. (cbf5367)


### Fixes
- Bound confluence + git-repo file-processing tasks with asynq.Timeout (1d729a3)
- Stale assets, self-hosted fonts, KG alias matching, CI/toolchain bumps (48aa314)
- Rebuild Docling pages from item provenance, unclash design-system tokens (f3dcdec)
- Derive real Docling page numbers, make citation pills click-to-preview (f28314e)
- Vendor JLU design-system tokens so CI and Docker can build (81257a0)
- Scope SSRF proxy exemption to exact host:port to prevent unintended access to other ports (8c54fca)
- Update progress_updated_at in UpdateFileStage to prevent false stuck-file timeouts (3e59ae6)
- Improve system stability by addressing goroutine leaks, data races, process panic points, and connection timeouts across backend modules (1d09bd2)


### Performance
- Optimize graph performance by replacing inline component styles with injected CSS for hover-dimming and node filtering (34510ad)
- Optimize performance in KG storage, text splitting, and embedding caching through batching and UTF-8 safe boundary alignment. (204d647)
- Migrate sort implementations to slices package, optimize logging allocations, and enable binary pgvector parameter binding. (b7f0988)
- Optimize database connection pooling and knowledge base pagination queries, and refactor SQL update builder to use pgxutil (cb1be30)
- Increase HTTP connection pool sizes and introduce memory pooling for search-related maps to reduce allocation overhead (bda0b20)
- Add benchmarks, optimize SSE buffer allocation, and refactor vector search helpers (6ae5180)
- Optimize SSE framing and filename sanitization, update documentation, and add benchmark smoke tests to CI (c752c9d)


### Refactoring
- Keep both agent controls in the composer, drop the KB-settings trigger (820fdd0)
- Introduce GraphInteractions hook and update MindMap UI with EntityCard, GraphToolbar, and improved interactivity (37087a0)
- Switch to proxy-aware HTTP client and move SSRF protection to redirect layer (8bb99aa)
- Remove failed knowledge base file status from HomeView chips (c9d6221)
- Standardize formatting and simplify recursive function declaration in canonicalization algorithm (c70cfa0)
- Update OpenAPI paths with explicit base prefixes and fix null alias handling in KG store insertion (633b898)
- Remove StructuredTableView component in favor of rendering Markdown tables directly via prompt update (e7fa358)
- Implement global AI request concurrency limiting, make ingest parallelism configurable, and improve health server shutdown gracefulness (28687b2)
- Optimize concurrent specialist execution, improve stream cleanup logging, and modernize string suffix detection (497c473)
- Update all benchmarks to use b.Loop() and remove redundant rune conversion in trimTokenPunct (6999a26)
- Implement structured output contracts for AI classification and add recency-based vector search boosting (9d08d10)
- Update MaxBytesExcept middleware to use path-agnostic predicates and enforce gofmt in CI (386bb4c)
- Add context-awareness, job timeouts, concurrency limits, and lazy initialization across internal services (fb206c6)
- Remove legacy drizzle migrations and add new chat processing modules and CI configuration (572c399)
- Initialize agent channel early, fix multi-query result deadlocks, and explicitly use bare goroutines for correct panic recovery ordering (1d21385)
- Centralize SSE header logic, improve internal error logging, and add mustInt64 config helper (0101647)
- Migrate registry probes to GoCtx and document concurrency invariants across chat modules (2aed7b9)
- Improve SQL error handling, add configurable DB connection retries, and instrument rate-limit and proxy configuration metrics. (4bd906f)
- Improve documentation, add interface compile-time assertions, and enhance transaction error reporting (f9604c3)
- Update concurrency patterns and improve shutdown logging for background operations (9d0a01b)
- Introduce ClauseBuilder in pgxutil to simplify SQL clause construction and prevent parameter indexing errors (3df8aeb)
- Add graceful shutdown for query cache sweeper, clarify concurrency in DAG/search logic, and update documentation. (6a944e8)
- Remove legacy golden set validation tests from loader_test.go (00ea177)

