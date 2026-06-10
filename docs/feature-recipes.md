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

## Sub-question decomposition (DecomposeRAG, T1-1)

```
query_decompose_enabled          = true        # adds 1 fast-tier LLM call on complex_reasoning turns
query_decompose_model            = <small>     # falls through to model_tier_fast
```

Fires only when `QueryType == complex_reasoning` AND `opts.SubQueries` is empty (so Plan-Execute doesn't double-fire — standard fallback is the primary beneficiary). Produces 2–4 *semantically distinct* sub-questions (NOT paraphrases — that's `MultiQuery`); folds into RRF via the MultiQuery path. Single-aspect queries return empty.

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

```
chat_tabular_query_enabled            = true     # Phase 1: ingest-time materializer + table_query tool
chat_tabular_semantic_columns_enabled = true     # Phase 2: embed free-text columns for fuzzy search (orthogonal)
tabular_semantic_min_avg_len          = 32       # Phase 2: min mean cell length to treat TEXT as free text
tabular_semantic_min_distinct_ratio   = 0.6      # Phase 2: min distinct-value ratio (skips categoricals)
chat_tabular_charts_enabled           = true     # Phase 3: chart prompt-guidance (no new tool/migration)
```

Migration **0048** (`tabular` schema + `tabular_catalog`). Phase 1 materializes `.xlsx`/`.xls`/`.csv` into native-typed tables; only a per-sheet summary card is vector-embedded (divert, not hybrid). `table_query` runs read-only SELECTs through `JUSTRAG_DB_URL_READONLY` with the per-KB catalog allowlist. Re-ingest spreadsheets after enabling.

**OPERATOR PREREQUISITE** — run once as DB owner/superuser after migration 0048:

```sql
GRANT SELECT ON tabular_catalog TO <readonly_role>;          -- required: tool reads catalog through readonly pool
GRANT USAGE ON SCHEMA tabular TO <readonly_role>;
GRANT SELECT ON ALL TABLES IN SCHEMA tabular TO <readonly_role>;
ALTER DEFAULT PRIVILEGES FOR ROLE <db_user> IN SCHEMA tabular
    GRANT SELECT ON TABLES TO <readonly_role>;               -- required: per-sheet tables created on every ingest
```

`<db_user>` = `DB_USER` (Go worker/server role that creates the per-sheet tables). `<readonly_role>` = role behind `JUSTRAG_DB_URL_READONLY`.

**SECURITY:** the read-only role's `search_path` must **NOT** include `tabular`. Per-KB isolation depends on schema-qualified `tabular.<name>` references — unqualified table names must fail to resolve so a prompt-injected bare name cannot bypass the catalog allowlist.

**Phase 1 limits:** first row = header; multi-row headers / merged cells / legacy BIFF `.xls` fall back to text; sheet buffered in memory before `COPY` (multi-hundred-MB spike at 1M rows). **Phase 2:** synthetic `_rowid bigint` + per-row embeddings for heuristic-selected TEXT columns; fuzzy hit → `table_query WHERE _rowid IN (...)`. **Phase 3:** prompt-guidance only — Recharts JSON in a ` ```chart ` block rendered by the frontend ChartRenderer; non-SQL reshapes use code_exec (gated by `chat_code_exec_enabled`). Specs in `docs/superpowers/specs/2026-05-28-tabular-data-qa-design.md` (+phase2/3).

## HyPE — hypothetical prompt embeddings (ingest + retrieval)

```
hype_enabled              = true     # ingest: generate+embed N hypothetical questions per chunk into chunk_hype_questions_<dim>
hype_questions_per_chunk  = 3        # [1,20]
hype_model                = <small>  # falls through to model_tier_fast
hype_search_enabled       = true     # query-time arm: match query against question embeddings, fold parent chunks into RRF
```

No migration (dim-keyed `chunk_hype_questions_<dim>`, created at startup by `EnsureHyPETable`; same halfvec/HNSW rules). Re-ingest is the only backfill. Build the index first (`hype_enabled` + re-ingest), then enable `hype_search_enabled` and validate with `cmd/eval`. Vector-only (does NOT feed BM25); orthogonal to `contextual_enrichment`. Fail-open. `hype_search_enabled` fires on the standard `PrepareChatContext` path AND every orchestrator's **initial** search (Supervisor / Plan-Execute / Agentic / DeepChat) — wired exactly where `GraphChunkIDs` is threaded (initial retrieval only; not sub-query / hop-2+ / DAG-node searches).
