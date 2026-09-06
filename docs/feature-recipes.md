# Feature enablement recipes

Most chat-pipeline features default OFF. This file holds the full combined toggle list to make each feature actually do something on a deployment, in dependency order — plus the operator prerequisites, ops sequences, and gotchas that travel with each recipe. CLAUDE.md keeps only the index table; this is the source of truth for the per-feature detail.

## Refine gate + KB router + turn budget

```
chat_factuality_verifier_enabled = true        # dependency: must run before the gate
chat_factuality_gate_enabled     = true        # refine when verifier flags ≥1 unsupported/contradicted
chat_kb_router_enabled           = true        # needs kb.description set on every KB
chat_turn_budget_seconds         = 90          # typical prod cap; 0 = unlimited
```

No new migrations beyond 0042. `chat_kb_router_enabled` is a no-op without `?route=auto` on the chat request.

## Tool-aware planner + tool tier

```
chat_plan_execute_enabled        = true        # baseline orchestrator
chat_plan_execute_dag            = true        # DAG-shaped plans
chat_plan_execute_tool_aware     = true        # planner sees tool catalog
chat_code_exec_enabled           = false       # keep off until gVisor is verified
```

Migration **0043**. `code_exec` requires docker `--runtime=runsc` in `/etc/docker/daemon.json`; security-review `internal/mcp/builtin/code_exec.go` before prod. Planner falls back LLM-error → legacy DAG → flat.

## Answer-time tool calling

```
chat_answer_tools_enabled        = true        # gate; default false
chat_answer_tools_max_rounds     = 5           # valid range [1,10]
```

Migration **0043**. Composable with `chat_plan_execute_tool_aware`. KB chat model MUST support native `tools` + `tool_calls` (verified on gemma-4-26b-A4B-it). **Known limit:** models emitting `<think>` inline (vs `reasoning_content`) leak reasoning into the answer.

## Per-user long-term memory + Self-RAG

```
chat_longmem_enabled               = true        # GDPR "My Memory" drawer ships (see note below)
chat_longmem_min_salience          = 0.5         # filter low-salience extractor output
chat_longmem_recall_top_k          = 5           # facts prepended per turn
chat_longmem_decay_days            = 30          # half-life of the recency component
chat_longmem_recall_semantic       = true        # T1-2: ANN recall via embeddings (see dim caveat below)
chat_longmem_conflict_resolution   = true        # T1-3: Mem0 {create_new,supersede,skip_redundant} classifier
chat_longmem_conflict_model        = <small>     # falls through to model_tier_fast
chat_longmem_conflict_candidates   = 3           # nearest-N pool the classifier sees per insert
chat_self_rag_enabled              = true        # REPLACES chat_factuality_verifier_enabled (mutually exclusive)
chat_self_rag_model                = <small>     # LLM override for the unified verifier
chat_plan_execute_dag_iterative    = true        # inter-level critic; needs chat_plan_execute_dag = true
chat_plan_execute_dag_iterative_model = <small>  # critic LLM override
```

Migration **0045**. "My Memory" drawer (per-entry delete + bulk clear + JSON export per GDPR Art. 20) ships in `Profile.tsx` regardless of `chat_longmem_enabled`, backed by `/api/user/memory` via the `internal/memory` handler.

**T1-2 dim — ops sequence after embedder change:** the `user_memory.embedding` column is re-widened by `migrate.EnsureUserMemoryEmbedding` at `cmd/migrate` + worker startup (**not** server startup; DROP INDEX → ALTER → CREATE INDEX; halfvec for 2000–4000 dims, no HNSW index above 4000 — mirrors `vector/schema.go`). Run `cmd/migrate` (or restart the worker) → trigger `POST /api/admin/reembed-user-memory` (the ALTER discards old vectors) → only then enable `chat_longmem_recall_semantic` + `_conflict_resolution`.

## Knowledge-graph routing

```
kg_extraction_enabled                = true        # adds 1 LLM call per chunk; provider auto-cache covers ~90%
kg_extraction_model                  = <small-fast-model>   # optional override
chat_graph_routing_enabled           = true        # diagnostic gate — emits the trajectory event
chat_graph_routing_inject_chunks     = true        # chunk injection — folds subgraph chunks into RRF
chat_graph_routing_max_chunks        = 15          # cap on injected chunks (1..50, default 15)

# T1-4 / T1-5 traversal-mode trichotomy (default neighbors keeps v1 behaviour):
chat_graph_routing_path_mode         = ppr         # neighbors | ppr | paths
chat_graph_routing_ppr_damping       = 0.85        # only used when path_mode=ppr
chat_graph_routing_ppr_max_iter      = 20          # only used when path_mode=ppr
chat_graph_routing_ppr_top_entities  = 10          # only used when path_mode=ppr
chat_graph_routing_paths_max_len     = 3           # only used when path_mode=paths
chat_graph_routing_paths_max_paths   = 5           # only used when path_mode=paths
```

Migration **0044**. No-op until `kg_extraction_enabled` has run on the KB's files — re-ingest ≥1 file after enabling extraction (else lands in the `db_error` bucket). `_inject_chunks` is a separate sub-flag (upgrades stay diagnostic-only); Plan-Execute/Agentic inject only into the **initial** search. `path_mode` defaults to `neighbors`; all modes fail open to `neighbors`.

**Cross-feature ordering:** refine gates are independent of tool tier and graph routing; enable KG ingestion + tool-aware planner together for the multi-hop eval gain (planner sees `graph_search` in the catalog).

## Full iterative DRIFT

For global-synthesis queries, a dedicated orchestrator reads KG community summaries (the primer), asks the fast-tier LLM for follow-up sub-questions, runs one light local search per follow-up, and synthesises a single answer — the full iterative DRIFT path.

**Prerequisite:** run the community-build job so community summaries exist (`kg_communities_enabled = true` + `POST /api/kb/{id}/communities/build`). If no summaries exist, DRIFT degrades gracefully to a primerless run (follow-ups generated from the query alone).

```
chat_drift_enabled        = true        # master gate; default off
chat_drift_max_followups  = 4           # [1,8]
chat_drift_primer_top_k   = 6           # [1,20]
chat_drift_search_top_k   = 8           # [1,30]
chat_drift_model          = <fast-tier> # optional; falls through model_tier_fast
```

Migration **0059** (kg_communities). Gated on `complex_reasoning` + `IsGlobalSynthesisQuery`; for matching queries it takes priority over supervisor/plan-execute/agentic/standard. Independent of, and composes with, the P2-B community-primed MVP (`chat_community_search_enabled`) — DRIFT is the multi-step path, the MVP is single-pass injection. Adds 1 fast-tier LLM call + N follow-up searches per matching turn; validate on the golden set before enabling. Metric: `rag_drift_run_total{outcome}`.

## Sub-question decomposition (DecomposeRAG, T1-1)

```
query_decompose_enabled          = true        # adds 1 fast-tier LLM call on complex_reasoning turns
query_decompose_model            = <small>     # falls through to model_tier_fast
```

Fires only when `QueryType == complex_reasoning` AND `opts.SubQueries` is empty (so Plan-Execute doesn't double-fire — standard fallback is the primary beneficiary). Produces 2–4 *semantically distinct* sub-questions (NOT paraphrases — that's `MultiQuery`); folds into RRF via the MultiQuery path. Single-aspect queries return empty.

## Real BM25 keyword arm

```
bm25_scoring_mode = ts_rank    # gate; "ts_rank" (default) | "bm25"; per-KB overridable
bm25_k1           = 1.2        # BM25 term-frequency saturation; clamped 0.5–3.0; per-KB
bm25_b            = 0.75       # BM25 length normalization; clamped 0–1; per-KB
```

No migration. `ts_rank` mode is Postgres's built-in two-argument `ts_rank()` (term frequency only, no IDF, default normalisation 0 — no document-length or cover-density/proximity weighting) — the generated keyword SQL is byte-identical to the pre-Wave-2 builder output at this setting. `bm25` mode scores the same WHERE-clause candidate set (the candidate set never differs between modes — only the ranking does) with real Okapi BM25, computed per arm (`lang`/`simple`) from two new per-KB, per-dimension stats tables: `bm25_kb_stats_<dim>` (doc count + avg length per KB/arm) and `bm25_term_stats_<dim>` (per-lexeme document frequency per KB/arm). `dl`/`avgdl` are distinct-lexeme counts (`length(tsvector)`), not raw token counts, computed identically by the refresher and at query time — no chunk-table column, no re-ingest.

**Operator prerequisites:**
1. A worker process with `WORKER_MAINTENANCE` on (default) so the `bm25_stats_refresh` maintenance loop runs — it sweeps up to 20 stale KBs every 15 minutes across every dim table `ListChunkTableDimensions` reports, creating the stats tables idempotently on first run. **The loop is gated (`BM25StatsRefresher.ModeEnabled`, wired in `internal/app/worker.go`): it is a no-op — `Sweep` returns immediately, no `ts_stat()` call, no DB round-trip — until `bm25_scoring_mode = bm25` is set globally OR at least one KB overrides it to `bm25` in `kb_site_configs`.** So flipping the mode for the first KB in a deployment is what turns the sweep on at all; every other KB's stats catch up on the very next 15-minute tick once that happens (or immediately via `cmd/eval --refresh-bm25-stats`, which also creates the tables idempotently if a dev DB predates them and is unaffected by the gate). A KB is stale when it has no stats row yet, its stats predate its newest chunk, or its stats are older than 24h; a KB with an active ingestion (`files` row in `pending`/`processing`) is skipped until ingestion settles.
2. Flip `bm25_scoring_mode = bm25` **per KB** (it's a `kbConfigRegistry` key, not global-only) once that KB's stats are confirmed fresh.
3. **Bump `queryCacheSchemaVersion`** (`internal/vector/query_cache_shape.go`) or otherwise clear the query cache. `bm25_scoring_mode` is deployment-wide config and is deliberately NOT hashed into the per-request query-cache shape (a shape hash is meant to vary only with request-scoped fields), so a cache entry produced under one mode would otherwise keep being served after the mode flips. The version was bumped to 3 when this feature shipped; operators must bump it again by hand on every subsequent flip.
4. Verify with the two metrics: every `rag.search.stages` log line should carry `"keyword_mode":"bm25"` for that KB's queries, `rag_keyword_arm_mode_total{mode}` should show `bm25` incrementing, and `rag_bm25_mode_fallback_total{reason="no_stats"}` should stay flat (a rising fallback counter means the stats sweep hasn't caught up with that KB/dimension yet — fail-soft, not an error, but worth checking the maintenance-loop logs).

**Tokeniser-divergence caveat:** the WHERE-clause candidate floor (`buildOrTokensExpr`) and `to_tsvector` (which `bm25`'s `qlex` CTE independently re-tokenises the raw query text through, per `keyword_sql.go`'s `bm25ArmCTE`) can segment the same input differently — e.g. `Stud.IP` may pass the OR-token floor as a unit but split into `stud` + `ip` under `to_tsvector`. A candidate admitted by the floor can therefore score exactly 0 under `bm25` (its independent re-tokenisation finds no matching lexeme) while `ts_rank` still gives it a small positive score — it scores directly off the same composed tsquery the floor already admitted the candidate under, term-frequency only (`ts_rank`'s default normalisation is 0 — no document-length or cover-density/proximity weighting), rather than re-tokenising from scratch. Scoring-only: the candidate is never dropped from the keyword-tier list, just ranked poorly.

**Default stays `ts_rank`.** A/B on the production fixture (`eval/golden/production-ppm-2026-08.jsonl`, 89 questions, `docs/retrieval.md` §"Keyword arm scoring: ts_rank vs BM25 (2026-09)"): `bm25` (no tiered boost) lifted lookup recall +5.1 pp and lookup MRR +2.3 pp against a 2.3 pp noise band — right at the noise floor, not clearly above it — while `bm25` + tiered boost lifted lookup MRR +4.1 pp, clearing the bar. But both `bm25` cells regressed `complex_reasoning` MRR by ~7 pp (recall on that route stayed flat — the right chunks are retrieved, they just rank lower after RRF fusion), tripping `cmd/eval`'s own regression gate. The likely cause is scale mismatch: `rrf_weight_bm25` (tuned against `ts_rank`'s output range) applied unchanged to BM25's differently-scaled IDF·TF-saturation scores. **`bm25` ships as an opt-in per-KB mode** — flip it on a KB where lookup-heavy traffic outweighs `complex_reasoning` traffic, or once the Wave-3 follow-up re-tunes `rrf_weight_bm25`/`rerank_blend_alpha` for BM25's score scale and the default gets revisited.

**Side finding:** `bm25_tiered_boost_enabled` (the coarse ts_rank×100/×10 IDF proxy) was net negative under `ts_rank` on every route on this fixture (overall MRR −3.5 pp vs. the `ts_rank`/no-boost baseline, more than the 1.3 pp overall-MRR noise band) and didn't clearly help under `bm25` either (real BM25 already carries an IDF term, so the boost stacks rather than substitutes). Keep `bm25_tiered_boost_enabled` default off; it's a Wave-3 retirement candidate.

## Tiered BM25 boost (T0-3) and per-query-type cache thresholds (T0-4)

```
bm25_tiered_boost_enabled                              = true        # ts_rank × 100 (strict match) or × 10 (OR-fallback)
query_cache_similarity_threshold_lookup                = 0.92        # paraphrase-tolerant
query_cache_similarity_threshold_enumeration           = 0.94        # mid
query_cache_similarity_threshold_complex_reasoning     = 0.98        # paraphrase-sensitive
```

Pure-config (no migration, no LLM). Tiered boost: ts_rank ×100 on strict AND-match, ×10 on OR-floor; simple-arm unboosted; single-token = no-op. Cache thresholds: sentinel `0` inherits the global.

## Dynamic alpha (T2-4)

```
hybrid_dynamic_alpha_enabled        = true        # per-query α shift from BPE-token rarity
hybrid_dynamic_alpha_sensitivity    = 0.3         # caps shift magnitude; [0, 1]; 0 disables
```

Shifts effective `rerank_blend_alpha` by mean cl100k_base BPE-token ID — rare tokens → α down (more BM25), common → α up (more reranker). Composes with per-route overrides (resolved first, then shifted). Formula: `internal/vector/dynamic_alpha.go`.

## Online feedback loop (retrieval boost + admin review)

```
chat_feedback_boost_enabled = true     # gate; default off
feedback_boost_weight       = 0.05     # max |score adjustment|; clamped [0, 0.5]; 0 → 0.05 at apply
```

Migration **0052** (`message_chunks` link table). Capture reuses the existing per-message thumbs up/down (`SubmitFeedback` → `messages.feedback` + `message_feedback_events`). At answer time, cited chunk IDs are linked in `message_chunks` (in `AddMessage` — `ChatSource.ChunkID` is `json:"-"`, so this is the only capture point). At search time the net signal (upvotes − downvotes) of candidate chunks applies a bounded `weight·tanh(net/2)` boost right after the rerank blend (before score-filter/MMR/trim), then re-sorts; fail-open. Admin review: `GET /api/admin/feedback/chunks?kb_id=<id>&limit=<n>` lists the most net-negative chunks. Cross-DB: links + feedback live in **main** Postgres, read in Go and applied to the **vector**-DB result by chunk ID. `internal/feedback` (reader), `internal/vector/feedback_boost.go` (scoring), `internal/adminfeedback` (review).

## Recency prior (similarity ⊕ freshness)

```
recency_boost_enabled   = true     # gate; default off
recency_boost_weight    = 0.1      # score adjustment for a brand-new file; clamped (0, 0.5]
recency_half_life_days  = 14       # exponential-decay half-life; clamped (0, 3650]
```

No migration. After the rerank blend + feedback boost (stage 10c), each doc gets `weight · 2^(−ageDays/halfLife)` added, keyed on the source file's `created_at` (one main-DB query per search over the fused pool's distinct file IDs; fail-open), then re-sorts. Worth enabling ONLY for time-sensitive corpora — RSS / Confluence KBs where each item is its own file row; on static document KBs it adds nothing and re-uploading a file resets `created_at`, which churns rankings. Deployment-wide flag until per-KB overlays land. `internal/vector/recency_boost.go`. Evidence basis: arXiv 2509.19376 (α≈0.7 similarity ⊕ recency, ~14-day half-life).

**Now measurable (Wave 2, Task 8/9):** `eval/golden/cert-recency-de.jsonl` (25 questions, synthetic CERT-advisory corpus, committed — see `eval/fixtures/seed-cert.sh` below) is the first golden set with time-sensitive questions. Seed it, then A/B with `--recency-boost on|off` against the standard path forced (`--orchestrator-dispatch=false` — recency listing and the boost's NEU/UPDATE effect only apply on `PrepareChatContext`, and dispatch is itself a non-deterministic LLM call that would otherwise confound the comparison):

```bash
JUSTRAG_URL=http://localhost:3000 JUSTRAG_ADMIN_USER=admin JUSTRAG_ADMIN_PASSWORD=<pw> \
  ./eval/fixtures/seed-cert.sh
cd go-backend
./cmd/eval/eval --golden ../eval/golden/cert-recency-de.local.jsonl \
  --production-context --orchestrator-dispatch=false --recency-boost off --output off1.json
./cmd/eval/eval --golden ../eval/golden/cert-recency-de.local.jsonl \
  --production-context --orchestrator-dispatch=false --recency-boost on  --output on.json
```

Result (standard path forced, the trustworthy isolated estimate — see `eval/golden/cert-recency-de.acceptance.md` for the production-like dispatch-on table too, which shows a larger but partly dispatch-confounded effect): `recency_boost_enabled=on` raised overall recall 0.651 → 0.696 against a 4.0 pp same-flag noise band (0.611/0.651 off-run pair), and raised the UPDATE-outranks-NEU win rate on the 8 NEU/UPDATE pairs from 3/8 (off) to 5/8 (on) — a marginal but directionally positive, noise-exceeding effect. **Default stays off** (it still only helps time-sensitive corpora, per the antipattern above); recommended for RSS/CERT-style KBs specifically. `cmd/eval` previously wired no recency lister at all under `--production-context`, silently diverging from production date-awareness on every prior eval run — fixed in the same task (`internal/recencylister`, `eval.WithRecencyLister`).

## Sufficient-context abstention gate

```
chat_sufficient_context_enabled = true     # gate; default off — 1 fast-tier LLM call per gated turn
chat_sufficient_context_model   = <small>  # falls through to model_tier_fast → KB chat model
```

No migration. One fast-tier call between context assembly and generation asking whether the assembled chunk set as a WHOLE suffices to answer (Google ICLR 2025 "sufficient context": models hallucinate most when context is partially relevant but jointly insufficient). Complements CRAG (per-chunk relevance) — it does not replace it. On "insufficient": the existing abstain plumbing fires (abstain notice in the system prompt, `ChatContext.Abstain` for metrics/UI); the context still reaches the prompt so the model can say what IS covered. Wired in the standard path (`PrepareChatContext`, skipped when CRAG already abstained or under long-context routing) and the supervisor orchestrator (flags arrive pre-resolved via `SupervisorChatParams`). Fail-open: judge errors or unparsable verdicts never block answers. Validate abstain rates on a golden set with unanswerable items before flipping on. `internal/ai/sufficient_context.go`, `internal/prompts/sufficient_context.go`.

## ECoRAG evidentiality compression (T2-3)

```
chat_context_compression_enabled    = true        # 1 fast-tier LLM call between rerank and prompt
chat_context_compression_min_chunks = 15          # skip when pool is smaller
chat_context_compression_threshold  = 0.3         # drop chunks scoring below
chat_context_compression_model      = <small>     # falls through to model_tier_fast
```

Drops chunks judged to lack DIRECT evidence (distinct from reranker topicality); "never drop everything" fallback. Skipped under long-context (T2-1).

## Long-context routing (System 2, T2-1)

```
chat_longcontext_enabled         = true          # CAUTION: per-turn LLM cost up to ~30× when gate fires
chat_longcontext_max_tokens      = 100000        # 10k..500k; chat-layer truncation budget for the wide pool
```

Fires on `complex_reasoning` + the `IsGlobalSynthesisQuery` classifier (EN+DE "summarise all"). When fired: top-k → 200; MMR + score-drop + parent-child + ECoRAG + multipass skipped (still relevance-ranks, NOT a bypass). Watch `rag_longcontext_route_total{outcome=fired}` before broad rollout.

## Late chunking (Jina-style)

```
late_chunking_enabled             = true        # provider must understand the `late_chunking: true` field (Jina-compatible)
late_chunking_max_input_tokens    = 8192        # cl100k_base estimate; documents split into windows at this cap
```

Ingestion-side only (no migration); embedding cache bypassed; re-ingest to benefit. Orthogonal to `contextual_enrichment` (prefix still feeds BM25, but is NOT concatenated into the late-chunked input). **Provider gotcha:** most OpenAI-compatible servers silently ignore the `late_chunking` field and return standard embeddings — verify before prod (Jina `/v1/embeddings` is the reference).

## RAPTOR hierarchical indexing

```
raptor_enabled                   = true        # mutually exclusive with parent_child_enabled (skipped at ingest if both on)
raptor_min_chunks                = 25          # files with fewer leaves are skipped
raptor_max_levels                = 4           # hard cap on tree depth
raptor_branching_factor          = 5           # K-means cluster size target; ignored when algorithm=leiden
raptor_summary_model             = ""          # falls through to model_tier_fast → KB chat model
raptor_clustering_algorithm      = leiden      # T2-2: kmeans (default) | leiden
raptor_leiden_resolution         = 1.0         # γ for modularity; only used when algorithm=leiden
```

Migration **0046**. Mutually exclusive with `parent_child_enabled` (skipped at ingest if both on). Ingest-only LLM cost (~31% extra rows at branching=5); zero query-time cost. Backfill by re-ingest. Eval ablation: `./cmd/eval/eval --node-kind leaf|summary|""`.

## Structured spreadsheet Q&A (table_query)

What ships: a streaming, typed reader (`sheetsource`, xlsx/xls/ods/csv/tsv — deliberately excludes `.xlsm`) → a sheet profiler (`tabular/profile`: regions, header blocks, column roles, sheet kind) → `tabular/ingest`, which drives both a native-typed Postgres materialiser (`internal/tabular`, one table per table region, plus a `tabular_column_values` distinct-value index for fuzzy lookup) and a key:value hybrid text render (`tabular/render`) that folds a profile card per region into the normal chunk/embed pipeline → the Phase-3 deterministic **tabular router** (`chat.NewTabularRouter`), which turns a lookup/aggregation question into one validated, executed read-only SQL statement ahead of retrieval → the `table_query` MCP tool as a manual fallback. Spreadsheets skip enrichment/KG/HyPE/RAPTOR entirely (row records, not prose).

Enablement block, in dependency order:

```
chat_tabular_query_enabled          = true       # master gate: streaming materialiser + table_query tool; default false
# JUSTRAG_DB_URL_READONLY must be set to a SELECT-only role before this does anything useful —
# see the OPERATOR PREREQUISITE grants below; the router logs a startup warning and disables
# itself otherwise (internal/app/routes.go).

tabular_profile_llm_enabled         = true       # per-region LLM column descriptions / low-confidence overrides; RequiresReingest
tabular_profile_llm_threshold       = 0.7        # [0,1] heuristic confidence below which the LLM proposal wins; RequiresReingest
tabular_profile_model               = <small>    # falls through to model_tier_fast; RequiresReingest
tabular_profile_sample_rows         = 200        # [20,2000] rows sampled per sheet for structure detection; RequiresReingest
tabular_max_rows                    = 2000000    # [1000,5000000] per table region: rows past this dropped from the SQL table (and counted); RequiresReingest
tabular_embed_max_rows              = 50000      # [1,100000] per table region: rows past this are SQL-only, not embedded in the hybrid text page; 0 is rejected (would silently mean the default); RequiresReingest
tabular_column_values_max_distinct  = 10000      # [100,100000] per column: above this no tabular_column_values row is written (fuzzy lookup falls back to BM25/ILIKE); RequiresReingest
chat_tabular_charts_enabled         = true       # Phase 3: chart prompt-guidance (no new tool/migration); default false

chat_tabular_router_enabled           = true     # Phase 3 kill switch (default ON); pre-pass that turns a lookup/aggregation question into one validated read-only SQL statement before falling back to normal retrieval; effective only together with chat_tabular_query_enabled
chat_tabular_router_model             = <small>  # falls through to model_tier_fast
chat_tabular_router_max_rows          = 200      # [10,1000] row cap on the router-generated query's result set
chat_tabular_router_max_repairs       = 3        # [0,5] LLM repair attempts on a DB error / empty result / all-NULL aggregate
chat_tabular_router_timeout_ms        = 5000     # [500,30000] wall-clock budget for SQL generation + execution
chat_tabular_router_schema_max_tokens = 12000    # [1000,60000] token budget for the schema text in the router's SQL-generation prompt

# Phase 4 (ops hardening — no new migration):
chat_tabular_guidance_max_tokens      = 6000     # [1000,30000] token budget for the per-KB catalog summary folded into the ANSWER prompt (maybeTabularGuidance); a separate budget from the router's own schema prompt above — replaces the former hardcoded 6000-token constant
tabular_max_file_bytes                = 524288000     # [1048576,2147483647] bytes (default 500 MiB); an uploaded spreadsheet over this answers HTTP 413; NOT RequiresReingest (read at upload time only) — see the runbook's caveat about the separate, hardcoded 500 MiB transport cap
tabular_large_file_bytes              = 20971520      # [1048576,1073741824] bytes (default 20 MiB); ingested spreadsheets above this are "large" for concurrency purposes; NOT RequiresReingest, read fresh per file
tabular_large_file_concurrency        = 1             # [1,8]; how many "large" spreadsheets one worker process materialises at once; NOT RequiresReingest, but read ONCE at worker startup into a semaphore — changing it needs a worker restart, not just a re-ingest
```

Migrations **0048** (`tabular` schema + `tabular_catalog`) and **0069** (catalog v2 columns, `tabular_column_values`, `tabular_query_log`, `files.parse_report`/`stage_detail`). No migration in Phase 4. The 2026-09-04 spreadsheet-ingest rework's Phase 2 replaced the Phase-1 buffered-memory materializer with a streaming reader (`sheetsource`) → profiler (`tabular/profile`) → typed Postgres tables (`internal/tabular`; a majority-numeric TEXT column or a `RoleMeasure` column additionally gets a `_num` shadow column, primary stays text) + a key:value hybrid text render (`tabular/render`, one profile card per table region) for the standard chunk/embed pipeline. The three `chat_tabular_semantic_columns_enabled`/`tabular_semantic_min_avg_len`/`tabular_semantic_min_distinct_ratio` free-text-column-embedding keys from the earlier Phase-2 draft are **REMOVED** (superseded by the hybrid render + `tabular_column_values` value index). `table_query` runs read-only SELECTs through `JUSTRAG_DB_URL_READONLY` with the per-KB catalog allowlist. **Any `tabular_*`/`tabular_profile_*` key change needs a re-ingest to take effect** (values are baked in at ingest time) — re-upload the file, or use the per-KB rematerialise endpoint `POST /api/kb/{id}/tabular/rematerialize` (kbAdmin), which re-ingests every spreadsheet file in the KB via `TypeReEmbedding`. The three Phase 4 size/concurrency keys are the exception: none is `RequiresReingest` (confirmed in `internal/siteconfig/registry.go`) — they govern upload-time rejection and ingest-time scheduling, not the values baked into a materialised table.

`tabular_embed_max_rows` is capped at **100 000** (Ruling R23). The renderer buffers one window of that many rows per table region in memory before it writes a line — `render.RenderSheet` collects the region's rows into a map, then formats them — so the knob bounds worker RSS, not just page length; a million-row window is an OOM, not a slow ingest. A larger window waits for the incremental renderer planned for a future phase. Rows past the cap stay reachable through `table_query` (the profile card says so), and only say so when the region actually has a materialised table.

**Ops sequence** (per file, after the feature is enabled): upload the spreadsheet (`POST /api/kb/{id}/files`) → watch its ingest progress in the sidebar ("Quellen"/sources panel), which now surfaces `files.stage_detail` live, including the large-file gate's wait message → once `completed`, open the "Tabellen" panel from the file's row (`GET /api/kb/{id}/files/{fileId}/tabular`, view role) to inspect the parsed regions, column roles, and any dropped/uncoerced rows → after changing any `tabular_*`/`tabular_profile_*` knob, run `POST /api/kb/{id}/tabular/rematerialize` (kbAdmin) rather than waiting for the next unrelated re-ingest, since the old values stay baked into the existing tables until then. See `docs/runbooks/spreadsheet-ingest-ops.md` for the full operator runbook (sizing, the large-file gate, the orphan sweep, alerting, and the acceptance procedure).

**OPERATOR PREREQUISITE** — run once as DB owner/superuser after migration 0048 (and `tabular_column_values` again after 0069):

```sql
GRANT SELECT ON tabular_catalog TO <readonly_role>;          -- required: tool reads catalog through readonly pool
GRANT SELECT ON tabular_column_values TO <readonly_role>;    -- required: fuzzy free-text-cell value lookup (0069)
GRANT USAGE ON SCHEMA tabular TO <readonly_role>;
GRANT SELECT ON ALL TABLES IN SCHEMA tabular TO <readonly_role>;
ALTER DEFAULT PRIVILEGES FOR ROLE <db_user> IN SCHEMA tabular
    GRANT SELECT ON TABLES TO <readonly_role>;               -- required: per-sheet tables created on every ingest
```

`<db_user>` = `DB_USER` (Go worker/server role that creates the per-sheet tables). `<readonly_role>` = role behind `JUSTRAG_DB_URL_READONLY`. The tabular router (below) reuses this same read-only role and the same `tabular_catalog`/`tabular_column_values` grants above — no additional operator grant is needed for it. The one asymmetry: `tabular_query_log` (the router's post-response SQL audit trail) is written by the **app pool** (`DB_USER`, not `<readonly_role>`) via `Catalog.InsertQueryLog` — the read-only role never needs write access to it, and does not need a SELECT grant on it either, since nothing reads it back through that pool.

**Phase 3 — the tabular router.** On top of the materialized schema and the `table_query` tool above, a deterministic router (`chat.NewTabularRouter`, `internal/chat/tabular_router.go`) runs on the standard and Supervisor chat paths before retrieval: deterministic cues + a stored-value lookup decide whether to fire; when they do, a fast-tier LLM (`chat_tabular_router_model` → `model_tier_fast`) proposes one SELECT against a token-budgeted schema summary, `internal/tabular/sqlcheck` validates it (fail-closed AST walker; `AllowedTables` scoped to the KB; function/relation allowlists; LIMIT enforced), and `internal/tabular/sqlexec` executes it read-only with a `statement_timeout`, with up to `chat_tabular_router_max_repairs` repair rounds on a DB error/empty/all-NULL result (never on a clean one). See `docs/retrieval.md`'s "Spreadsheets" section for the full step-by-step mechanism, the retrieval-hint side effects, the SQL log, and the metrics/eval rates.

`internal/tabular/sqlcheck` depends on `github.com/cockroachdb/cockroachdb-parser@v0.25.2` for real Postgres-dialect AST parsing (rather than hand-rolled regex/tokenizing) — measured cost to a static `cmd/server` build: **+13.9 MiB (+17%)**, from 82.0 MiB to 95.97 MiB, plus +304 transitively pulled modules (Task 0's before/after build). No new operator action follows from this; it is purely a deployed-artifact-size note.

**SECURITY:** the read-only role's `search_path` must **NOT** include `tabular`. Per-KB isolation depends on schema-qualified `tabular.<name>` references — unqualified table names must fail to resolve so a prompt-injected bare name cannot bypass the catalog allowlist. The pool additionally sets `default_transaction_read_only=on` per session (writes fail even if the role's grants are ever fat-fingered); an explicit `default_transaction_read_only` in the `JUSTRAG_DB_URL_READONLY` DSN overrides it, same as `statement_timeout`. In operator terms (design spec §6.6): the `table_query` allowlist plus the read-only role are the **only** KB isolation boundary for this feature — there is no per-cell or per-row ACL. Spreadsheet cell contents are attacker-controlled data, not instructions: a cell that reads like one (`ignore previous instructions`, `system:`, `assistant:`, a bare URL in a non-URL column) is dropped from column descriptions, samples, and value sets before it can reach an LLM prompt or the "Tabellen" panel response — it still lives in the row data itself and in `table_query`/router SQL results, which is by design (a row's literal cell value is data, never re-interpreted as an instruction by the SQL layer).

**Known limits:**
- A numeric-looking string with an annotation (`"2007; Anbau 2018"`) stays TEXT in the primary column — the Q&A layer never guesses at the "real" number; only a materialised `_num` shadow column (majority-numeric TEXT columns and `RoleMeasure` columns) is typed, and the primary text column is what a citation quotes.
- The tabular router only runs on the standard `PrepareChatContext` path and the Supervisor orchestrator — Plan-Execute, Agentic, and DRIFT do not consult it (they still see the hybrid rendered text through normal retrieval, just not the deterministic SQL fast-path).
- `.xlsm` (macro-enabled workbooks) is deliberately unsupported — out of scope, not a bug.
- Numeric router results always render as canonical Postgres decimal-string text, not a typed number — correct for citation display, not for further arithmetic downstream.

See `docs/runbooks/spreadsheet-ingest-ops.md` for sizing, the large-file gate, the orphan sweep, alerting, and the acceptance procedure.

**Phase 3:** prompt-guidance only — Recharts JSON in a ` ```chart ` block rendered by the frontend ChartRenderer; non-SQL reshapes use code_exec (gated by `chat_code_exec_enabled`). Specs in `docs/superpowers/specs/2026-05-28-tabular-data-qa-design.md` (Phase 1) and `docs/superpowers/specs/2026-09-04-spreadsheet-ingest-rework-design.md` (Phase 2, current mechanism).

## HyPE — hypothetical prompt embeddings (ingest + retrieval)

```
hype_enabled              = true     # ingest: generate+embed N hypothetical questions per chunk into chunk_hype_questions_<dim>
hype_questions_per_chunk  = 3        # [1,20]
hype_model                = <small>  # falls through to model_tier_fast
hype_search_enabled       = true     # query-time arm: match query against question embeddings, fold parent chunks into RRF
```

No migration (dim-keyed `chunk_hype_questions_<dim>`, created at startup by `EnsureHyPETable`; same halfvec/HNSW rules). Re-ingest is the only backfill. Build the index first (`hype_enabled` + re-ingest), then enable `hype_search_enabled` and validate with `cmd/eval`. Vector-only (does NOT feed BM25); orthogonal to `contextual_enrichment`. Fail-open. `hype_search_enabled` fires on the standard `PrepareChatContext` path AND every orchestrator's **initial** search (Supervisor / Plan-Execute / Agentic / DeepChat) — wired exactly where `GraphChunkIDs` is threaded (initial retrieval only; not sub-query / hop-2+ / DAG-node searches).

## In-chat document comparison

```
chat_compare_enabled             = true        # master gate; default off
chat_compare_model               = <small>     # per-task fast-tier override → model_tier_fast → KB chat model
chat_compare_max_sections        = 60          # [1,500] cap on sections analyzed per uploaded file
chat_compare_concurrency         = 6           # [1,32] fan-out cap for per-section LLM checks
chat_compare_peers_per_section   = 5           # [1,20] KB retrieval limit per section
chat_compare_attachment_ttl_hours = 24         # [1,720] Redis attachment store TTL
chat_compare_max_file_bytes      = 10485760    # 10 MB upload cap
```

No migration, no operator SQL grants — the feature reuses the chat KB read-access ACL (`chat_compare` requests are gated by the same auth + KB-view check + `chat` rate limiter as the chat send route). The user uploads a single file in the chat (`POST /api/kb/{id}/chat/attachment`, multipart `file`); it is **parsed in memory and held session-scoped in a Redis-backed `chatattach` store with a 24h TTL — it is NEVER ingested into the KB**. The upload endpoint returns `{attachmentId, filename, sectionCount, charCount}`; 503 when disabled, 413 over the 10 MB cap, 415 on an unsupported type, 422 on unparsable content. The chat send body then carries `attachmentId` + `comparisonModes[]`; when both are present (and the feature is enabled) the turn dispatches to `RunComparisonChat` (highest orchestrator priority).

Three selectable comparison modes:

- **contradiction** — claims in the uploaded file that conflict with the KB's documents
- **formal** — formal-correctness issues, *inferred from the retrieved KB peers* (there is no template; the reference style is learned from the KB itself)
- **completeness** — sections present in the KB peers but missing from (or under-specified in) the uploaded file

The reference set is **auto-retrieved from the whole KB** per section (no manual document selection). The engine fans out per section → per-section KB retrieval → a strict-`json_schema` structured-output LLM check per mode (fast tier; resolves via `ResolveFastTierModel(ctx, reader, "chat_compare_model")`) → aggregated findings sorted by severity, streamed to the client via a `comparisonFindings` SSE event, followed by a streamed prose summary. The per-section checks run on the fast tier; the prose **summary uses the KB chat model**.

Security: uploads are user-scoped — an attachment is readable only by the user who uploaded it — and the upload endpoint is rate-limited via the shared `chat` limiter. New package `internal/chatattach`; engine in `internal/chat/comparison_chat.go`; endpoint handler in `internal/chat/http_attachment.go`.

> Follow-up: the `chat_compare_*` keys are **not** yet surfaced in the admin Agent panel (`web/src/components/admin/AdminAgentTab.tsx` is a hand-written component, not driven by `siteconfig/registry.go`). Until that JSX is added, set these keys directly in `site_config` (e.g. via the generic site-config editor / SQL). Verify admin-UI exposure as a separate frontend task.

## Date-aware chat

```
chat_date_awareness_enabled       = true                # default ON; kill switch for date injection
chat_date_timezone                = Europe/Berlin       # IANA timezone used to resolve "today" (deployment-wide)
chat_date_tools_enabled           = false               # gate for recent_documents tool; default off
chat_date_tools_max_results       = 50                  # [1,500] cap on files returned per recent_documents call
chat_recency_listing_enabled      = true                # default ON; kill switch for the deterministic recency-listing path
chat_recency_listing_window_days  = 7                   # [1,365] window "new" resolves to when the query names none
chat_recency_listing_max_results  = 50                  # [1,500] cap on the injected file listing
chat_recency_listing_name_match_enabled = true          # default ON; include name-labeled files (NEU/new) outside the window
```

**Date injection (always-on by default):** when `chat_date_awareness_enabled = true`, the current date is injected into the answer system prompt for all six answer orchestrators (standard/`PrepareChatContext`, the legacy `RunDeepChat`, supervisor, plan-execute, agentic, and DRIFT), allowing the LLM to resolve temporal queries like "what was added today", "since May", or "recent changes". The date line is computed once per request at dispatch and threaded onto each orchestrator's params. Set the flag to `false` to disable date context entirely (the answer prompt then stays byte-identical to the pre-feature prompt).

**Timezone resolution:** `chat_date_timezone` is a single deployment-wide IANA timezone (default `Europe/Berlin`) used to resolve "today" — there is no per-user or per-KB override. Fail-open: if the timezone string is invalid or unparseable, resolution falls back to UTC.

**Recent-documents tool:** the `recent_documents` MCP tool lists files added to the current KB within a date window (newest first, name + origin + date). It requires:
- `chat_date_tools_enabled = true` (gate; default off — when off the tool returns a "disabled" message)
- `chat_answer_tools_enabled = true` (answer-time tools must be enabled for the LLM to call any MCP tool)

Parameters: `date_from` (required, ISO `YYYY-MM-DD`) and `date_to` (optional, ISO `YYYY-MM-DD`; defaults to now, treated as inclusive of the whole day). The LLM computes these absolute dates from the injected current date. Results are capped at `chat_date_tools_max_results`. `kb_id` is injected automatically at dispatch.

**Search date filtering:** the `kb_search` MCP tool accepts optional `date_from` / `date_to` params (ISO `YYYY-MM-DD`, always available and ungated) that constrain retrieval to files whose ingest date falls in the window — independent of whether the recent-documents tool is enabled. Because the chunk tables (vector DB) and `files` (main DB) may be separate databases, the window is resolved to file IDs via the main DB and folded into the existing file-ID filter rather than a cross-DB join; a zero-match window returns no results.

**Deterministic recency listing (default ON):** queries that ask "what is new / recently added" ("Welche neuen Meldungen gibt es?", "What's new?", "Welche Artikel wurden in den letzten 5 Tagen veröffentlicht?") carry almost no semantic signal, so plain BM25+vector retrieval returns an arbitrary subset of recent files and the enumeration pre-pass — which only verifies items already in context — cannot recover the rest (observed in production 2026-07-02: 1 of many new advisories listed). When `chat_recency_listing_enabled = true` and the regex classifier `IsRecencyListingQuery` fires (precision-over-recall: recency adjectives anchored to document nouns, so "neue Erkenntnisse"/"new features" do not match), `PrepareChatContext` (a) sets `SearchOptions.CreatedAfter` to the window start — day-start of today − `chat_recency_listing_window_days` in `chat_date_timezone`, overridden by an explicit window in the query ("in den letzten 5 Tagen", "von heute") — so retrieved chunks and citations come from recent files only, and (b) fetches the complete file listing for the window from the main DB and injects it as a system-prompt addendum: the listing, not the semantic top-k, is the completeness contract. An at-cap listing (`chat_recency_listing_max_results`) is disclosed as incomplete in the prompt; an empty window instructs the model to answer "nothing new since <date>". The listing supersedes the enumeration pre-pass for these queries (two competing completeness contracts would conflict). Fail-open: a lister error reverts to legacy retrieval. Standard-path only (`PrepareChatContext`); orchestrator paths for complex_reasoning queries are unaffected, as is the eval harness unless it routes through the standard path. No migration.

**Name-marker arm (`chat_recency_listing_name_match_enabled`, default ON):** some corpora label new items in the file NAME — CERT-Bund advisories carry "NEU" vs "UPDATE" in the title — so "neue Meldungen" can target the labeled subset rather than ingest recency. When the query literally mentions "neu"/"new" (any inflection; purely temporal phrasings like "aktuelle Warnungen" or "zuletzt hinzugefügt" do not trigger it), files whose name matches the word-boundary regex `\m(neu|new)\M` are fetched regardless of window, merged into the listing (out-of-window matches annotated with their date and label provenance), and — when safe (no user file selection to respect, window listing not truncated) — retrieval switches from the `CreatedAfter` window to an explicit `FileIDs` union so the labeled files' chunks stay citable. The addendum also instructs the model to consider name status labels when the question targets them. A marker-lookup error keeps the window arm (fail-open).

**Date column:** date windows key on `files.created_at` (file ingest timestamp). A future phase 2 feature will introduce a per-file `published_at` column for corpora with explicit publication dates (RSS feeds, news archives, etc.); the single `effectiveDateExpr` constant in `internal/vector/recency_boost.go` will swap in the published-at column when it becomes available, requiring no config change.

**Golden-set coverage (Wave 2, Task 8):** `eval/golden/cert-recency-de.jsonl` (25 questions; synthetic, fictional German CERT-advisory corpus, `eval/fixtures/cert-advisories/*.md` + `manifest.tsv`, both committed) exercises both `chat_recency_listing_enabled` and `recency_boost_enabled` (see the "Recency prior" recipe above for the boost numbers) against 12 fictional products / 26 fictional WID-SEC advisories (14 NEU→UPDATE pairs, 12 single-issue). Seed with `eval/fixtures/seed-cert.sh` (creates/reuses KB "CERT Fixtures", ingests via `POST /api/kb/{id}/text`, verifies every file landed under its expected name, backdates `files.created_at` per the manifest, writes the gitignored `eval/golden/cert-recency-de.local.jsonl` with `kb_id` resolved; `--restamp --kb-id <uuid>` re-runs just the backdating against an already-seeded KB). Confirmed live: the recency-listing classifier resolves explicit windows (3/6/7/14 days) and the name-marker arm ("neu"/"Neues" → all NEU-labeled files regardless of window) correctly, logs `rag.recency_listing.fired`, and window-scopes retrieval as documented — but only fires when the question actually reaches the standard path; under production-like orchestrator dispatch, an LLM misclassification of a recency-listing-shaped question as `complex_reasoning` routes it to `plan_execute` instead, where the lister never runs — a real, pre-existing production interaction, not a fixture defect. See `eval/golden/README.md` §"CERT recency set" and `eval/golden/cert-recency-de.acceptance.md` for the full per-question tables.

Code: `internal/chat/date_prompt.go` (injection), `internal/chat/recency_classifier.go` + `internal/chat/recency_listing.go` (recency listing), `internal/mcp/builtin/recent_documents.go` (tool implementation), `internal/mcp/builtin/kb_search.go` (search date params).

## Image captioning + better tables (Docling)

Sidecar prerequisites (both shipped manifests set them; a hand-rolled deployment must too):

```
DOCLING_SERVE_ENABLE_REMOTE_SERVICES=true                 # REQUIRED for captioning — without it docling refuses the
                                                          # captioning pipeline and EVERY conversion falls back to pdftotext
DOCLING_SERVE_MAX_SYNC_WAIT=600                           # only for sync /v1/convert/file callers (curl); the worker uses the task endpoints
image: quay.io/docling-project/docling-serve:v1.32.0     # pinned; bump deliberately, then re-run the live integration tests
```

```
docling_enabled                              = true        # prerequisite: sidecar reachable (see docs/observability/docling.md)
docling_base_url                             = http://docling:5001   # or the k8s Service DNS
docling_table_mode                           = accurate    # default (the sidecar's own default; "fast" trades structure for speed)
docling_ocr_languages                        = de,en       # default; docling's RapidOCR default is English + Chinese
docling_force_ocr                            = false       # default; true for scan-heavy KBs or broken text layers
docling_document_timeout_seconds             = 600         # default; docling's own per-document limit
docling_picture_description_enabled          = true        # gate; default off
docling_picture_area_threshold               = 0.05        # skip images < 5% of page area (filters logos/icons); [0,1]
docling_picture_description_prompt           = …           # optional; default asks (in German) for the document's language,
                                                           # figure type, and every readable number/label verbatim
docling_picture_description_timeout_seconds  = 120         # default; docling's 20 s default silently drops slow captions
```

No migration. Captioning rides the Docling convert call, so `docling_enabled` must be on. **The vision endpoint + API key are injected per-request by the Go backend** from the admin AI provider config (same endpoint + key the app already uses); the vision model follows `describe_image_model` (→ `model_tier_fast`) — set it to a vision-capable model (e.g. `jlu/gemma-4-26b-it`). The key is **never** stored on the Docling sidecar (required when the model API needs auth); Docling only needs network reachability to that model URL plus the `ENABLE_REMOTE_SERVICES` flag above. The worker probes the captioning path at startup (tiny embedded PDF, live options) and logs at **error** level when the sidecar rejects it. Pictures docling classifies as logo / icon / signature / stamp / barcode / QR / page thumbnail never reach the vision model. Captions, the text found *inside* figures (axis values, labels), table captions and footnotes are walked out of the DoclingDocument and land on the figure's or table's own page; retrieval is caption→text, no multimodal embeddings.

`docling_enabled` / `docling_base_url` are read at worker start; every other key above is re-read per conversion, so admin-panel edits apply to the next file. When on, standalone image uploads (`.png`/`.jpg`/…) also route through Docling (caption + OCR) with Tesseract as the fallback. Existing files are **not** retroactively captioned — re-ingest a KB to benefit (captions, in-figure text, table captions and heading levels are all baked into chunk text at ingest).

**Throttle / GPU contention:** Docling's calls to gemma-4 bypass `AI_MAX_CONCURRENT_REQUESTS`, so cap Docling replicas + per-pod `DOCLING_SERVE_ENG_LOC_NUM_WORKERS` (the `k8s/docling.yml` fixed replica count is the throttle; vision calls run one at a time per document). The worker converts through the task endpoints (submit → poll → result, bounded only by `DOCLING_TIMEOUT_SECONDS`), so `DOCLING_SERVE_MAX_SYNC_WAIT` matters only for sync callers. Fast-follows available on the same request and not yet wired: `do_chart_extraction` (granite-vision chart→table on the sidecar, GPU-only in practice), `do_formula_enrichment` (open upstream memory-growth issue).

## Git repository source

```
git_repo_enabled = true    # master gate; default off (admin Agent panel)
```

When enabled, a grid button appears in the KB Sources panel. Clicking it opens `GitRepoModal` where the operator enters a repository URL, selects public or private (private requires a Personal Access Token), and optionally specifies a branch (defaults to the remote HEAD). Saving creates a `git_repo_sources` row; the UI lists all sources grouped under a "Git Repositories" section with a manual re-sync button.

**How it works:** submitting the form enqueues a `git-repo-sync` worker task on the `rag-heavy` queue. The worker shallow-clones the repository via go-git v5 through the existing SSRF-safe transport (`fetcher.SafeHTTPClient`). On each sync it compares the remote HEAD commit SHA against the stored SHA; if unchanged the sync is a no-op. Otherwise it performs a path + blob-SHA delta reconcile: files added or modified since the last sync are ingested as individual `files` rows (`origin='git'`) through the normal chunk/embed/KG pipeline; deleted files are cascade-removed. Each file carries `git_repo_source_id`, `git_file_path`, and `git_blob_sha` for future delta tracking. A source stays manual-only re-sync by default, but can now be switched to `daily`/`weekly` like RSS and Confluence sources — see "Night-window source syncing" below.

**Security:**

- **HTTPS-only** clone; `git://` and `ssh://` URLs are rejected at validation.
- Private-repo PATs are **encrypted at rest** via `confluence.EncryptToken` (AES-256-GCM, JWT-derived key) and are **never returned** to the client — responses carry only `hasToken: true`.
- The SSRF-safe transport (`fetcher.SafeHTTPClient`) re-resolves hostnames at dial time and blocks requests to private/loopback/link-local ranges, preventing server-side request forgery via DNS rebinding.
- **Per-file cap:** 1 MiB max file size; files exceeding the cap are skipped and logged.
- **Per-repo cap:** 2000 files per sync; excess files are skipped.
- **Clone timeout:** enforced via context deadline on the go-git `CloneContext` call.
- Binary files are detected by NUL-byte sniff and skipped before any parse or embed call.

**File filter defaults:** text and code extensions allowlist (`.go`, `.py`, `.ts`, `.tsx`, `.js`, `.jsx`, `.java`, `.kt`, `.rs`, `.c`, `.cpp`, `.h`, `.cs`, `.rb`, `.php`, `.swift`, `.md`, `.txt`, `.rst`, `.yaml`, `.yml`, `.json`, `.toml`, `.xml`, `.html`, `.css`, `.sh`, `.sql`, `.proto`, …) plus known-name allowlist (`README`, `LICENSE`, `CHANGELOG`, `Makefile`, `Dockerfile`, and their common variants). Skip-list noise directories: `node_modules`, `vendor`, `dist`, `build`, `.git`, `__pycache__`, `.cache`, `.idea`, `.vscode`.

Migration **0060** (`git_repo_sources` table + `files.git_repo_source_id`, `files.git_file_path`, `files.git_blob_sha` columns + `'git'` value for `files.origin`). Package `internal/gitrepo`.

## Night-window source syncing

```
sync_window_start_hour = 1               # 0-23; unparseable/out-of-range falls back to the default
sync_window_end_hour   = 5               # 0-23; start == end is a 60-minute window, not zero
sync_window_timezone   = Europe/Berlin   # IANA name; unresolvable falls back to Europe/Berlin, then UTC
```

Always on — no master flag, and no migration beyond **0068**. Every RSS feed, Confluence source and git repository carries a `sync_schedule` column (`manual` | `daily` | `weekly`, default `manual`, CHECK-constrained) and a `next_sync_at` timestamp. A single leader-side sweeper (`internal/syncsched.Sweeper`, elected via the existing Redis lock in `internal/app/scheduler.go`) ticks every **5 minutes**: for every source whose `next_sync_at` has arrived, it stamps that source's next slot *first*, then enqueues it via the normal `rss-poll` / `confluence-sync` / `git-repo-sync` worker tasks — stamp-before-enqueue, so a crash between the two costs one missed night rather than an enqueue loop on every subsequent tick. `internal/syncwindow` (pure, no DB) computes that slot: the offset inside the window and — for `weekly` — the weekday are both hashed from the source's id, so sources spread deterministically across the window instead of firing in a burst at its first minute, and the schedule survives restarts and redeploys without reshuffling.

**Manual is a separate axis from the schedule.** The existing manual "sync now" trigger on each source (RSS/Confluence/git-repo) enqueues directly and never reads or writes `sync_schedule` or `next_sync_at` — it fires immediately regardless of window, and does not consume or move a source's next scheduled slot.

**`next_sync_at IS NULL` means "not yet stamped," not "due now."** A source newly switched to `daily`/`weekly` (or created with one), and every row touched by the 0068 backfill, starts with `next_sync_at = NULL`. The sweeper's first pass over such a row only stamps a future slot — it does not enqueue. This is why turning a source's schedule on, or applying the migration, never fires a sync outside the window: the row is invisible to the "due" query (which additionally requires `next_sync_at <= now()`) until it has a stamped slot to be due against.

**A window change takes effect within one tick, but only moves runs that have not fired yet.** `Tick` re-reads the three site_config keys every 5 minutes and applies the new window only to `ListUnscheduled` rows (nothing stamped yet); a row already carrying a `next_sync_at` inside the old window keeps that slot until it fires and is restamped into the new one on its next cycle.

**Operational note — a source due during downtime fires on the next sweep, which may be daytime.** The sweeper has no "skip if we're outside the window now" check on `ListDue` — `next_sync_at <= now()` is the only gate. If the leader replica (or Redis) was down through the night, every source stamped for that night stays due and fires on the first tick after recovery, whatever the wall-clock hour is. This trades a bounded number of daytime syncs for never silently dropping a night's work.

**A brief overlap between an outgoing and an incoming leader is accepted by design**, not a bug: on lock handover mid-tick, both replicas can be mid-sweep for a moment, and a source can be enqueued twice for the same night. `Tick` always stamps a row's next slot *before* enqueuing it, so this cannot loop into a second duplicate; the cost is one redundant poll/sync, which ingestion's `content_hash` dedup absorbs. Do not add cross-leader locking to close this window — it is deliberately not fixed.

**Migration 0068 backfill** (irreversible, one-shot): every RSS feed with `status = 'active'` becomes `daily` (a paused feed stays `manual`, so un-pausing it later requires explicitly choosing a schedule rather than silently inheriting nightly syncs); every Confluence source with a set `sync_interval` becomes `daily` regardless of its old interval, including previously-weekly ones (one uniform target beats reproducing a per-interval map that is being retired); git repositories, which had no scheduler before this release, stay `manual`. The old `rss_feeds.poll_interval` / `confluence_sources.sync_interval` columns are left in place and unread (expand/contract) for a later cleanup release.

Both `cmd/server` and `cmd/worker` blank-import `_ "time/tzdata"` so `sync_window_timezone` (and any other configured IANA timezone, `chat_date_timezone` included) resolves correctly on the alpine runtime image, which ships no zoneinfo database — before this, an unresolvable timezone name silently fell back to UTC in production. Packages: `internal/syncwindow` (pure slot math), `internal/syncsched` (the sweeper), store methods on `internal/rss`, `internal/confluence`, `internal/gitrepo`. Admin panel: Agent tab → "Night sync window".

## Scheduled eval + regression gate

**Prerequisites:** a golden set stored for the KB via the admin Eval tab ("Generate from corpus" or a manual upload), and `sync_window_*` configured (this feature reuses the same sweeper — see "Night-window source syncing" above).

```
# per golden set, in the admin Eval tab's schedule dropdown (or PATCH below):
schedule = daily                        # manual (default) | daily | weekly

# global, admin Agent panel → "Geplante Evaluationen" section:
eval_regression_recall_pp = 2.0         # max tolerated mean-recall drop, in pp; unset/≤0/>100 falls back to this default
eval_regression_mrr_pp    = 3.0         # max tolerated MRR drop, in pp; same fallback rule
```

Migration **0070** (`eval_golden_sets.schedule`/`next_run_at`, `eval_runs.scheduled`). Setting `schedule` via `PATCH /api/admin/eval/golden-sets/{id}` (system admin) or `PATCH /api/kb/{id}/eval/golden-sets/{gsId}` (KB advanced chain) with `{"schedule":"daily"}` — 400 on an invalid value, 404 on an unknown/foreign id. `internal/syncsched`'s existing leader-side sweeper picks up due golden sets exactly like RSS/Confluence/git-repo sources: `next_run_at IS NULL` means "not yet stamped" (a freshly-scheduled set gets its first slot stamped, not enqueued, so turning scheduling on never fires a daytime run). A due set enqueues `TypeEvalScheduled`, whose handler (`admineval.ScheduledWorker.HandleScheduled`) creates a normal `eval_runs` row — judge off, top-k 10, label `scheduled`, `scheduled=true`, config snapshot merged with the KB's per-KB overrides exactly like the KB-scoped `CreateRun` handler — and hands it to the same `TypeEvalRun` executor every other run uses (advisory-lock slots, per-KB lock, 2h timeout unchanged). Silent skips (Info/Warn log, no retry — the sweeper stamps the next slot regardless): the golden set was deleted, its schedule was switched back to `manual` in the meantime, it has no KB, or the KB already has an active run.

On completion, `Worker.checkScheduledRegression` loads `Store.LatestCompletedScheduled` — the temporal predecessor by `finished_at` for the same golden set, **manual runs excluded** — and runs `eval.CheckRegression` with the two threshold keys above. It publishes `rag_eval_scheduled_metric{kb,golden_set,route,metric}` (metric ∈ recall|precision|mrr|ndcg; route = `overall` or a query type) unconditionally, and `rag_eval_scheduled_regression{kb,golden_set,route}` (1/0) only when a baseline existed — **the first scheduled run for a golden set has no predecessor, so the regression gauge is left unset rather than defaulted to 0.** A regression also logs a WARN `eval.scheduled.regression`. Errors anywhere in this path never fail the run.

```yaml
- alert: RAGRetrievalRegression
  expr: max by (kb, golden_set, route) (rag_eval_scheduled_regression) == 1
  for: 1h
  labels: { severity: warning }
  annotations: { summary: "Scheduled eval regressed on {{ $labels.route }} ({{ $labels.golden_set }})" }
```

`internal/admineval/{scheduled,regression,worker}.go`, `internal/eval/store_pg.go` (`LatestCompletedScheduled`), `internal/observability/metrics.go`.

### Local A/B against a golden set (`cmd/eval --baseline`)

The same comparison, run by hand instead of by the sweeper — use it to grid a knob before committing a site_config change. Any prior `cmd/eval` JSON report works as `--baseline` (normalized fields don't matter; the comparison reads `Report.Aggregate`/`RouteAggregates`, not the raw bytes):

```bash
cd go-backend
./cmd/eval/eval --golden ../eval/golden/production-ppm-2026-08.jsonl --top-k 10 --output /tmp/base.json
# change one knob in site_configs (e.g. rerank_candidate_depth_complex_reasoning=120), then:
./cmd/eval/eval --golden ../eval/golden/production-ppm-2026-08.jsonl --top-k 10 --output /tmp/cand.json --baseline /tmp/base.json
echo "exit=$?"   # 0 ok, 1 question errors, 2 usage/unreadable baseline, 3 regression
```

`--regress-recall-pp` / `--regress-mrr-pp` override the defaults (2.0 / 3.0 pp) per invocation. A regression prints a per-route delta table (`eval.WriteDeltaTable`) to stdout before exiting 3. See `eval/golden/snapshots/README.md` for the operator convention on keeping a local baseline file.

## Rewrite ⊕ raw last turn

```
chat_condense_keep_raw_enabled = true    # gate; default off, per-KB overridable (group Retrieval)
```

No migration, no new LLM call. When a multi-turn follow-up gets condensed (rewritten against conversation history) and this flag is on, the chat layer additionally forwards the user's **verbatim** last-turn utterance into `vector.SearchOptions.RawQuery`. `Search()`'s `effectiveRawQuery` guard skips it when it's empty or equal (after trimming) to the already-condensed final query — so a single-turn question, which has nothing to condense, pays nothing. When it fires, the raw utterance runs as one extra list on **both** arms (vector + BM25 — independent of `rag_fusion_enabled`) and folds into RRF alongside the condensed query's own lists; `rag_raw_query_list_total{outcome}` counts it, and `raw_query_lists` appears in the `rag.search.stages` log event. `RawQuery` presence is part of the query-cache shape hash, so a raw-lane hit and a condensed-only hit never collide.

Threaded on the standard `PrepareChatContext` path and the Supervisor path (both `RetrieverAgent` and `EnumeratorAgent`). **Not** wired into public API / OpenAI-compat / MCP server / agent teams / plan-execute / agentic / DRIFT / `RunDeepChat` (the legacy 2-step default orchestrator for `complex_reasoning` when no orchestrator flag is on) — those paths don't carry a condensed-vs-raw distinction today. **Cost:** one extra embedding call + one extra BM25 query per condensed follow-up turn; zero on the first turn of a conversation or on any turn that isn't condensed.

**When to flip it on:** the mechanism exists because query condensation can drop a named entity or literal phrase the condensed rewrite paraphrases away (classic multi-turn lookup failure mode) — the raw lane gives BM25/vector a second, unparaphrased shot at it.

**Measured, stays OFF (Wave 2, Task 4/9).** Scored against `eval/golden/multi-turn-de.jsonl` (18 conversations / 45 turns, KB `PPM-Eval`, gitignored — JLU-internal) with `--keep-raw off|on` plus a same-flag noise repeat; see `eval/golden/multi-turn-de.acceptance.md`. The `pronoun_ref` turn kind (n=12) is the one the mechanism should help — every other turn kind scored byte-identical across all three runs (no condensation on the opening turn; `answer_ref` bypasses condensation entirely). Result: `pronoun_ref` recall OFF 0.833 / ON 0.806 / OFF-repeat 0.806 (2.8 pp noise band), and MRR OFF 0.833 / ON 0.767 / OFF-repeat 0.833 — keep-raw ON regressed MRR by 6.7 pp, more than double the noise band and in the wrong direction. The decision rule (flip ON only if `on − off` on `pronoun_ref` recall exceeds the noise band, with no route losing more than the noise band) fails outright: `on − off` is **negative**, not merely below the bar. The MTRAG "rewrite ⊕ raw" gain did not replicate on this fixture. Caveats worth weighing before revisiting: single run per cell (no variance estimate beyond the one noise repeat), 12-question `pronoun_ref` bucket, and LLM-condensation non-determinism (temperature > 0 on the condenser call) as a confound alongside the flag itself. **Default stays off.**

## KB permission model — rights matrix (Phase 1)

No flag; live since migration **0064**. Four roles, strictly ordered `view < edit < admin < owner`, resolved by `kbaccess.EffectiveRole` and enforced by `kbaccess.RequireKBRole(min)` — see the Quick reference block in `CLAUDE.md` for the five-rule resolution ladder. Reproduced here (from `docs/superpowers/specs/2026-08-12-kb-rollen-und-sichtbarkeit-design.md`) so operators and developers don't have to open the spec for the matrix itself:

| | view | edit | admin | owner |
|---|---|---|---|---|
| See the KB, chat, Studio/Export/Research | ✓ | ✓ | ✓ | ✓ |
| Upload/delete files, re-ingest, crawl | | ✓ | ✓ | ✓ |
| Create and sync RSS/Confluence/git sources | | ✓ | ✓ | ✓ |
| Name, description, prompt, models, tuning knobs | | | ✓ | ✓ |
| Attach agents/teams, eval, canonicalize, communities | | | ✓ | ✓ |
| Manage members (view/edit/admin) | | | ✓ | ✓ |
| Delete the KB, transfer ownership | | | | ✓ |

The `edit`/`admin` dividing line: `edit` fills the corpus, `admin` decides how it is processed and answered. Anything that can force a re-ingest or change answer quality belongs to `admin`. `owner` is unique per KB (`kb_members_owner_uniq` partial unique index) and is never assignable through the member endpoints — only through `POST /api/kb/{id}/transfer-owner`.

**Endpoints** (`internal/kbmembers`): `GET/PUT/DELETE /api/kb/{id}/members[/{userId}]`, `POST /api/kb/{id}/members/bulk`, `DELETE /api/kb/{id}/members/pending/{username}` — all `admin`. `POST /api/kb/{id}/transfer-owner` and `DELETE /api/kb/{id}/membership` — `view` chain, with the owner-only / self-only check inside the handler. The member list itself sits behind `admin`, not `view`: on a public KB every authenticated caller resolves to `view`, and the roster isn't theirs to read.

**Server-enforced invariants** (not just UI): a KB admin can neither remove nor demote the owner; the owner cannot leave their own KB; an admin can remove themselves (that's a self-leave, not a revocation); `RemoveMember` (admin revokes someone) leaves the target's chats in place, `LeaveKB` (self-service) deletes them — only the self-triggered action is destructive to chat history.

**Legacy remnants, not yet dropped:** `knowledge_base_shares` and `global_kb_editors` were backfilled into `kb_members` by migration 0064 and are deliberately still present as tables (expand/contract). Two code surfaces read or wrote them and both were repointed in Phase 1:

- the `/share*` HTTP surface and `kb/http_sharing.go` — deleted, since `MembersModal` fully replaced the share dialog;
- the admin global-KB **editor** endpoints (`GET/POST /api/admin/global-kbs/{id}/editors`, `DELETE .../editors/{userId}`, `internal/adminglobalkbs`) — now `kbmembers.Store` calls against `kb_members` with `role='admin'`. This was a live bug, not a tidy-up: `EffectiveRole` reads `kb_members` alone, so while these endpoints still touched `global_kb_editors`, *add* granted nothing beyond the implicit `view` of rule 4, and *remove* deleted a row nobody consults while the backfilled `kb_members` admin row survived — an invisible, un-revokable KB-`admin` on every global KB that had curators before the migration. Covered by `internal/adminglobalkbs/store_pg_editors_integration_test.go`.

No access decision consults either table now (`internal/cascade` still DELETEs from both, which is cleanup). The tables themselves are dropped only in a release after Phase 2 (visibility enum, system user, subscriptions, categories, catalogue — **not built**).
