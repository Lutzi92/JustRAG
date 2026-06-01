# JustRAG

Go-first RAG application with a React frontend, PostgreSQL + pgvector, Redis, and Asynq workers.

## Quick reference

**Site_config flag namespaces** (admin Agent panel; long-form mechanism docs in `docs/retrieval.md` and `docs/agent-orchestration.md`):

- `chat_*` — chat orchestrators, gates, post-response
- `chat_longmem_*` — per-user long-term memory (incl. `_recall_semantic`, `_conflict_*` for ANN + Mem0 conflict resolution)
- `chat_longcontext_*` — System-2 long-context routing for global-synthesis queries
- `chat_context_compression_*` — ECoRAG evidentiality-based post-rerank filtering
- `crag_*`, `adaptive_routing_*` — corrective-RAG + skip rules
- `kg_*`, `chat_graph_routing_*` — knowledge-graph extraction + routing (incl. `_path_mode` PPR/PathRAG trichotomy)
- `query_cache_*`, `step_back_*`, `mmr_*`, `rerank_blend_alpha*`, `top_n_*` — retrieval pipeline (`query_cache_similarity_threshold_*` for per-query-type thresholds)
- `query_decompose_*` — sub-question decomposition (DecomposeRAG)
- `bm25_simple_arm_enabled`, `bm25_tiered_boost_enabled` — BM25 keyword-arm tuning
- `hybrid_dynamic_alpha_*` — per-query α shift from BPE-token rarity
- `contextual_enrichment*`, `parent_child_*`, `docling_*`, `late_chunking_*` — ingestion
- `raptor_*` — per-file RAPTOR hierarchical summary trees (`raptor_clustering_algorithm` selects kmeans vs leiden)
- `chat_tabular_*`, `tabular_semantic_*` — structured spreadsheet Q&A + fuzzy free-text-cell search + charts/pivots (Phase 1/2/3; `chat_tabular_charts_enabled` is the Phase-3 flag)
- `ragas_*`, `factcheck_*`, `citation_validation_*`, `langfuse_*` — validation + observability
- `model_tier_fast` — deployment-wide default for fast-tier tasks (CRAG grader, KG extractor, contextual enricher, factuality / Self-RAG verifier, DAG critic, longmem extractor, KB router, RAPTOR summariser, **query decomposer, longmem conflict classifier, evidentiality classifier, HyPE question generator**); per-task `*_model` keys override

**Runtime-only knob (no site_config):** `hnsw.iterative_scan = relaxed_order` is set by `BuildPoolConfig`'s `AfterConnect` hook on every new pool connection (T0-1). Required for filtered ANN queries (kb_id, node_kind, GraphChunkIDs, file_id) to expand the HNSW candidate list until the WHERE clause is satisfied. Tolerates pgvector < 0.8 with a one-shot warning; no operator action needed when pgvector ≥ 0.8 is installed.

**Migrations** (in `go-backend/migrations/main/`, sequential, idempotent):

| # | Adds |
|---|---|
| 0007 | `content_hash` for cross-file dedup |
| 0008 | `contextual_prefix` column (Anthropic-style enrichment) |
| 0042 | `agent_decisions` (admin metrics panel) |
| 0043 | `agent_decisions.tool_calls JSONB` (tool-mix telemetry) |
| 0044 | `kg_entities` + `kg_edges` (knowledge-graph extraction) |
| 0045 | `user_memory` (long-term per-user memory) |
| 0046 | `node_kind`, `tree_level`, `raptor_parent_id` (RAPTOR hierarchical indexing) |
| 0047 | `users.external_id` + unique partial index (OIDC `sub` linkage; legacy LDAP rows stay NULL) |
| 0048 | tabular schema + `tabular_catalog` (structured spreadsheet Q&A) |

**Vector tables** are dim-keyed (`document_chunks_2560`, `document_chunks_4096`, …); switching the embedder requires a re-ingest.

**Read order for newcomers:** Architecture → Internal package map → Feature enablement recipes → `docs/retrieval.md` (full pipeline mechanism) → `docs/agent-orchestration.md` (orchestrators + features).

## Deep dives

CLAUDE.md is the operational reference (commands, env, architecture, toggle recipes). The two retrieval/orchestration subsystems each have a dedicated mechanism-and-rationale doc — open them when you need the *why* behind a knob or feature:

- **`docs/retrieval.md`** — every retrieval-pipeline subsystem (MMR, query cache, reranker blend, BM25 floor, embedder choice, CRAG, contextual retrieval, enumeration pre-pass, citation validator, ingestion dedup, …) with eval numbers and antipatterns.
- **`docs/presentation/tuning-knobs.md`** — admin-UI knob reference, oriented toward operations rather than mechanism.
- **`docs/observability/docling.md`** — opt-in Docling sidecar for layout-aware PDF parsing.
- **`docs/runbooks/hnsw-reindex.md`** — operator runbook for the T0-1 HNSW iterative-scan path (rebuilding the index after pgvector upgrade).

## Current Commands

### Go backend

- `cd go-backend && go build ./cmd/server`
- `cd go-backend && go build ./cmd/worker`
- `cd go-backend && go build ./cmd/migrate`
- `cd go-backend && go run ./cmd/migrate`
- `cd go-backend && go run ./cmd/migrate --status`
- `cd go-backend && go build ./cmd/eval`
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl` — retrieval evaluation against a golden set (see `eval/golden/README.md`)
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl --judge [--judge-model <name>]` — retrieval + LLM-as-judge (faithfulness, answer relevance, context precision)
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl --production-context [--enhance …] [--hyde] [--multi-query] [--crag on|off] [--enumeration on|off] [--orchestrator-dispatch=true|false]` — eval through the production chat pipeline (CRAG, enumeration pre-pass, contextual prefix, sandwich order). `--orchestrator-dispatch` defaults to **true**: each question routes through the same orchestrator predicate production uses (Supervisor / Plan-Execute / Agentic / standard fallback) and the report records per-question `agent` + per-orchestrator aggregates. Set `--orchestrator-dispatch=false` to reproduce pre-2026-05 retrieval-only behaviour for byte-stable diffs against historical runs.
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl --trajectory --orchestrator off|agentic|plan_execute|plan_execute_dag|supervisor|all [--judge]` — trajectory eval. Per (question, orchestrator) the runner emits a JSONL with the full agent decision sequence (`hop`, `iterate`, `plan`, `decision`, `agent_dispatch`, `answer` events). With `--judge`, four LLM judges score decomposition coverage, decision correctness, rewrite utility, and tool-call correctness. Output goes to `eval-trajectory.jsonl` + `.aggregate.json`.
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl --depth-buckets [--depth-buckets-min-chunks 4]` — position-aware retrieval analysis. Adds a `depth_buckets` section to the JSON report (and a stdout summary line) with per-quartile counts of relevant vs non-relevant chunks (chunkIndex/totalChunks bucketed into 0-25/25-50/50-75/75-100). Surfaces position bias: a heavy skew toward early buckets is evidence that retrieval favours the start of long files. NOT a true RULER needle-in-haystack test (which would need controlled ingestion); a synthetic-needle eval is a separate follow-up.
- `cd go-backend && go test ./...`

**Profile-Guided Optimization (optional, ~5-15% CPU on hot paths):** the build is PGO-ready (`-pgo=auto` is the Go default since 1.21 and auto-detects `default.pgo` in the main package dir). To enable, capture a 30s CPU profile under prod-representative load from the always-on pprof endpoint (`PPROF_ENABLED=1`), drop it next to the entrypoint, and rebuild:

```bash
curl "http://<internal-host>:6060/debug/pprof/profile?seconds=30" -o default.pgo
mv default.pgo go-backend/cmd/server/default.pgo   # committed → picked up by -pgo=auto
cd go-backend && go build ./cmd/server
```

Re-capture every few weeks as the hot-path profile drifts. No `default.pgo` is committed yet (it needs a real prod profile, not a synthetic one).

### Frontend

- `npm run web`
- `cd web && npm run build`
- `cd web && npm run test`
- `cd web && npm run lint`

### Root workspace

- `npm install`
- `npm run build`
- `npm test`

### Docker

- `docker compose up -d`
- `docker compose -f docker-compose.yml -f docker-compose.production.yml up -d`
- `docker compose -f docker-compose.local.yml up --build` — local dev: build the Go image from source
- `docker compose -f docker-compose.k8s.yml up` — app-only (no `go-worker`); workers run as separate k8s deployments
- `docker compose -f docker-compose.yml -f docker-compose.docling.yml up -d` — opt-in: enable Docling layout-aware PDF parsing (see `docs/observability/docling.md`)

## Local dev setup

Required env vars (load via `.env`; docker compose reads it via `env_file`):

- **Postgres (main):** `DB_HOST`, `DB_PORT` (default 5432), `DB_USER`, `DB_PASSWORD`, `DB_NAME`
- **Postgres (vector):** `VECTOR_DB_HOST` etc. (defaults to `DB_*` when unset — single-DB setups work out of the box)
- **Redis:** `REDIS_HOST`, `REDIS_PORT`, optional `REDIS_PASSWORD`
- **Auth:** `JWT_SECRET` (required at startup, ≥32 chars; a low-entropy secret — fewer than 3 character classes — logs a startup WARN). `ALLOWED_ORIGINS` (comma-list) is **required in production** — startup fails if unset, because an empty CORS allowlist makes `rs/cors` reflect any origin with credentials. `AUTH_PROVIDER_SECRET_KEY` (base64 32 bytes) encrypts auth-provider secrets at rest — OIDC `client_secret` **and** LDAP `bindCredentials`; required once any OIDC row exists or any LDAP provider is saved with bind credentials (legacy plaintext rows keep working at login until re-saved).
- **Object storage (optional):** `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET`, `S3_REGION` — when unset, files land on local disk
- **Worker:** `WORKER_QUEUES` (comma-list, defaults to all), `WORKER_MAINTENANCE` (`false` to disable the maintenance queue)
- **Tracing (opt-in):** `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_HEADERS`

Bring-up (containers):

```bash
docker compose up -d                # default — Go-only, applies migrations via the one-shot `migrate` service
docker compose logs -f migrate      # confirm the goose run finished cleanly before hitting the API
```

Bring-up (local Go process against compose-managed Postgres + Redis):

```bash
cd go-backend
go run ./cmd/migrate                # apply pending migrations (idempotent)
go run ./cmd/migrate --status       # list applied / pending
go run ./cmd/server                 # HTTP server on :3000
go run ./cmd/worker                 # Asynq worker (separate terminal)
```

Single-test run:

```bash
cd go-backend
go test ./internal/chat -run TestNeedsGraphTraversal -v
go test ./... -race                 # full suite with race detector
```

## Architecture

### Primary runtime

- `go-backend/` is the default backend/runtime
- `web/` is the React frontend
- the legacy Node.js backend and its `src/` tree have been removed; the runtime is Go-only

### Go backend

- Entrypoints: `cmd/server`, `cmd/worker`, `cmd/migrate`
- Startup wiring: `internal/app/`
- Config: `internal/config/`
- Migrations: `migrations/main/`, `migrations/vector/`
- Storage: main Postgres, vector Postgres, Redis, filesystem or S3-compatible object storage

### Frontend

- React 19 + Vite 7
- production assets are built into `web/dist` and copied into `/app/client/dist`

### Internal package map (`go-backend/internal/`)

Grouped by responsibility — names match directory names exactly. Long-form behaviour for each cluster lives in `docs/retrieval.md` (retrieval pipeline cluster) and `docs/agent-orchestration.md` (chat orchestration + tools clusters).

| Cluster | Packages | Owns |
|---|---|---|
| Wire / HTTP | `app`, `httputil`, `middleware`, `auth`, `authhandler`, `apikeyauth`, `sserelay`, `requestid` | Routing, auth chains, SSE relay, request-id propagation |
| Chat orchestration | `chat`, `agents`, `prompts`, `longmem` | The 4 orchestrators (agentic / plan-execute / supervisor / standard), trajectory events, refine/factuality gates, post-response tasks, per-user long-term memory store |
| Retrieval pipeline | `vector`, `ai`, `splitter`, `processor`, `parser`, `pptx` | Search service (BM25 + vector + reranker + MMR), embedder/grader/refiner LLM calls, chunking, document parsing, KG **write** path (`processor/kg_store.go`) |
| Tools (MCP) | `mcp`, `mcp/builtin`, `sessionmem`, `aibudget` | MCP registry + dispatcher, built-in tools (kb_search, keyword_search, chunk_read, document_outline, calculator, sql_query, code_exec, graph_search, web_search, memory_*), session memory, per-turn token budget |
| Knowledge graph | `kg`, `processor` (extractor) | KG **read** path (`kg/`); extractor + writer in `processor/kg_extractor.go` + `processor/kg_store.go` |
| Storage / data | `database`, `store`, `storage`, `pgxutil`, `files`, `kbaccess`, `cascade` | Postgres pools, file lifecycle, KB access ACLs, cascade-delete |
| Admin | `adminagentmetrics`, `adminconfigs`, `admineval`, `adminglobalkbs`, `adminmaintenance`, `adminmcp`, `adminproviders`, `adminusers`, `kb`, `users`, `apikeys`, `auditlogs` | Admin endpoints + matching site_config readers |
| Eval / observability | `eval`, `observability`, `logctx`, `analytics`, `health`, `systemhealth`, `apidocs` | Golden-set eval harness, Prometheus metrics, structured logging, health checks |
| Background workers | `worker`, `jobs`, `fetcher`, `crawler`, `rss`, `research`, `confluence`, `academic`, `gencontent`, `contentgen` | Asynq tasks: ingestion, RSS polling, Confluence crawl, research agent, content generation |
| Misc | `config`, `siteconfig`, `publicapi`, `publicconfigs`, `proxy`, `openaicompat`, `httpclient`, `redisclient`, `safego`, `misc`, `websearch` | Bootstrap, public API, OpenAI-compatible compatibility layer, panic-safe goroutines |

## Feature enablement recipes

Most chat-pipeline features default OFF. The combined toggle list to make each feature actually do something on a deployment, in dependency order:

**Refine gate + KB router + turn budget**

```
chat_factuality_verifier_enabled = true        # dependency: must run before the gate
chat_factuality_gate_enabled     = true        # refine when verifier flags ≥1 unsupported/contradicted
chat_kb_router_enabled           = true        # needs kb.description set on every KB
chat_turn_budget_seconds         = 90          # typical prod cap; 0 = unlimited
```

No new migrations beyond 0042. `chat_kb_router_enabled` is a no-op without `?route=auto` on the chat request.

**Tool-aware planner + tool tier**

```
chat_plan_execute_enabled        = true        # baseline orchestrator
chat_plan_execute_dag            = true        # DAG-shaped plans
chat_plan_execute_tool_aware     = true        # planner sees tool catalog
chat_code_exec_enabled           = false       # keep off until gVisor is verified
```

Migration **0043** (tool-mix telemetry). `code_exec` requires docker `--runtime=runsc` in `/etc/docker/daemon.json`; security-review `internal/mcp/builtin/code_exec.go` before flipping on prod. Tool-aware planner falls back LLM-error → legacy DAG → flat. Mechanism: `docs/agent-orchestration.md`.

**Answer-time tool calling**

```
chat_answer_tools_enabled        = true        # gate; default false
chat_answer_tools_max_rounds     = 5           # valid range [1,10]
```

Migration **0043** (auto telemetry via `MCPDispatcher.Dispatch`). Orthogonal to and composable with `chat_plan_execute_tool_aware`. KB chat model MUST support native `tools` + `tool_calls` (verified on gemma-4-26b-A4B-it). **Known limit:** models emitting `<think>` tags inline (vs `reasoning_content`) leak reasoning into the visible answer. Mechanism: `docs/agent-orchestration.md` → "Answer-time tool calling".

**Per-user long-term memory + Self-RAG**

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

**Knowledge-graph routing**

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

Migration **0044**. `chat_graph_routing_enabled` is a no-op until `kg_extraction_enabled` has actually run on the KB's files — re-ingest at least one file after enabling extraction (otherwise lands in the `db_error` outcome bucket). `chat_graph_routing_inject_chunks` is a separate sub-flag — existing deployments keep diagnostic-only behaviour on upgrade. Plan-Execute and Agentic only inject into the **initial** search (sub-query / hop-2 stays focused). `path_mode` defaults to `neighbors` and all three modes fail open to `neighbors`. Mechanism, injection wiring, and traversal-mode tuning: `docs/agent-orchestration.md` → "Graph-routing …".

**Cross-feature ordering:** refine gates are independent of tool tier and graph routing; enable KG ingestion + tool-aware planner together for the multi-hop eval gain (planner sees `graph_search` in the catalog).

**Sub-question decomposition (DecomposeRAG, T1-1)**

```
query_decompose_enabled          = true        # adds 1 fast-tier LLM call on complex_reasoning turns
query_decompose_model            = <small>     # falls through to model_tier_fast
```

Fires only when `params.QueryType == complex_reasoning` AND `opts.SubQueries` is empty (so Plan-Execute's own decomposition doesn't double-fire — standard fallback path is the primary beneficiary). Produces 2–4 *semantically distinct* sub-questions (NOT paraphrases — that's `MultiQuery`); folds into the RRF pool via the existing MultiQuery search path. Output capped at 4; single-aspect queries return an empty array. Mechanism: `docs/agent-orchestration.md` (stub).

**Tiered BM25 boost (T0-3) and per-query-type cache thresholds (T0-4)**

```
bm25_tiered_boost_enabled                              = true        # ts_rank × 100 (strict match) or × 10 (OR-fallback)
query_cache_similarity_threshold_lookup                = 0.92        # paraphrase-tolerant
query_cache_similarity_threshold_enumeration           = 0.94        # mid
query_cache_similarity_threshold_complex_reasoning     = 0.98        # paraphrase-sensitive
```

Pure-config tweaks (no migration, no LLM). Tiered boost: language-stemmer ts_rank ×100 on strict-form AND-required match, ×10 on OR-tokens floor; simple-arm unboosted; single-token queries are a no-op. Cache thresholds: sentinel `0` inherits the global. External validation: +7.5% NDCG on tiered boost; per-route thresholds kill subtle wrong-answer cache hits on complex queries while preserving lookup hit rate.

**Dynamic alpha (T2-4)**

```
hybrid_dynamic_alpha_enabled        = true        # per-query α shift from BPE-token rarity
hybrid_dynamic_alpha_sensitivity    = 0.3         # caps shift magnitude; [0, 1]; 0 disables
```

Shifts effective `rerank_blend_alpha` per query by mean cl100k_base BPE-token ID — rare tokens (named entities, error codes; mean ID ≫6000) → α down (more BM25); common tokens → α up (more reranker). Composes with per-route/entity overrides (resolved first, then shifted). Formula + calibration constants: `internal/vector/dynamic_alpha.go`.

**ECoRAG evidentiality compression (T2-3)**

```
chat_context_compression_enabled    = true        # 1 fast-tier LLM call between rerank and prompt
chat_context_compression_min_chunks = 15          # skip when pool is smaller
chat_context_compression_threshold  = 0.3         # drop chunks scoring below
chat_context_compression_model      = <small>     # falls through to model_tier_fast
```

One fast-tier LLM call between rerank and prompt; drops chunks judged to lack DIRECT evidence (distinct from the reranker's topical relevance). Defensive "never drop everything" fallback. Skipped under long-context mode (T2-1). Mechanism: `docs/retrieval.md` → "ECoRAG evidentiality compression".

**Long-context routing (System 2, T2-1)**

```
chat_longcontext_enabled         = true          # CAUTION: per-turn LLM cost up to ~30× when gate fires
chat_longcontext_max_tokens      = 100000        # 10k..500k; chat-layer truncation budget for the wide pool
```

Fires on `complex_reasoning` + the `IsGlobalSynthesisQuery` keyword classifier (EN+DE "summarise all" / "Fasse alle … zusammen"). When fired: top-k → 200, MMR + score-drop + parent-child + ECoRAG + multipass skipped. Still relevance-ranks (NOT a retrieval bypass). Watch `rag_longcontext_route_total{outcome=fired}` vs traffic before broad rollout. Mechanism + classifier scope: `docs/agent-orchestration.md`.

**Late chunking (Jina-style)**

```
late_chunking_enabled             = true        # provider must understand the `late_chunking: true` field (Jina-compatible)
late_chunking_max_input_tokens    = 8192        # cl100k_base estimate; documents split into windows at this cap
```

Ingestion-side only (no DB migration). Embedding cache bypassed (vectors depend on document context). Re-ingest to benefit. Orthogonal to `contextual_enrichment` — prefix still feeds BM25 + chat-time prompts but is NOT concatenated into the late-chunked embedding input. **Provider gotcha:** most OpenAI-compatible servers silently ignore the `late_chunking` field and return standard embeddings — verify before prod (Jina `/v1/embeddings` is the reference). Mechanism: `docs/retrieval.md`.

**RAPTOR hierarchical indexing**

```
raptor_enabled                   = true        # mutually exclusive with parent_child_enabled (skipped at ingest if both on)
raptor_min_chunks                = 25          # files with fewer leaves are skipped
raptor_max_levels                = 4           # hard cap on tree depth
raptor_branching_factor          = 5           # K-means cluster size target; ignored when algorithm=leiden
raptor_summary_model             = ""          # falls through to model_tier_fast → KB chat model
raptor_clustering_algorithm      = leiden      # T2-2: kmeans (default) | leiden
raptor_leiden_resolution         = 1.0         # γ for modularity; only used when algorithm=leiden
```

Migration **0046**. Mutually exclusive with `parent_child_enabled` (skipped at ingest if both on). Ingest-only LLM cost (~31% extra rows at branching=5 on 1000-chunk files); zero query-time cost — leaves and summaries compete in the same pool. Backfill by re-ingest. Eval ablation: `./cmd/eval/eval --node-kind leaf|summary|""`. Leiden vs. K-means trade-off + citation-validator recursive CTE on `raptor_parent_id`: `docs/retrieval.md`.

**Structured spreadsheet Q&A (table_query)**

```
chat_tabular_query_enabled            = true     # Phase 1: ingest-time materializer + table_query tool
chat_tabular_semantic_columns_enabled = true     # Phase 2: embed free-text columns for fuzzy search (orthogonal)
tabular_semantic_min_avg_len          = 32       # Phase 2: min mean cell length to treat TEXT as free text
tabular_semantic_min_distinct_ratio   = 0.6      # Phase 2: min distinct-value ratio (skips categoricals)
chat_tabular_charts_enabled           = true     # Phase 3: chart prompt-guidance (no new tool/migration)
```

Migration **0048** (`tabular` schema + `tabular_catalog`). Phase 1 materializes `.xlsx`/`.xls`/`.csv` into native-typed tables; only a per-sheet summary card is vector-embedded (divert, not hybrid). The `table_query` MCP tool runs read-only SELECTs through `JUSTRAG_DB_URL_READONLY` with the per-KB allowlist from the catalog. Re-ingest spreadsheets after enabling.

**OPERATOR PREREQUISITE** — run once as DB owner/superuser after migration 0048:

```sql
GRANT SELECT ON tabular_catalog TO <readonly_role>;          -- required: tool reads catalog through readonly pool
GRANT USAGE ON SCHEMA tabular TO <readonly_role>;
GRANT SELECT ON ALL TABLES IN SCHEMA tabular TO <readonly_role>;
ALTER DEFAULT PRIVILEGES FOR ROLE <db_user> IN SCHEMA tabular
    GRANT SELECT ON TABLES TO <readonly_role>;               -- required: per-sheet tables created on every ingest
```

`<db_user>` = `DB_USER` (Go worker/server role that creates the per-sheet tables). `<readonly_role>` = role behind `JUSTRAG_DB_URL_READONLY`.

**HyPE — hypothetical prompt embeddings (ingest + retrieval)**

```
hype_enabled              = true     # ingest: generate+embed N hypothetical questions per chunk into chunk_hype_questions_<dim>
hype_questions_per_chunk  = 3        # [1,20]
hype_model                = <small>  # falls through to model_tier_fast
hype_search_enabled       = true     # query-time arm: match query against question embeddings, fold parent chunks into RRF
```

No migration (programmatic dim-keyed table `chunk_hype_questions_<dim>`, created at startup by `EnsureHyPETable` alongside the chunk tables — same halfvec/HNSW rules). Re-ingest is the only backfill path. Build the index first (`hype_enabled`, re-ingest a KB), then enable `hype_search_enabled` and validate with `cmd/eval` before broad use — treat external headline gains as illustrative, gate on your own golden-set numbers. Vector-only (does NOT feed BM25); orthogonal to and composable with `contextual_enrichment` (which owns the BM25 side). Fail-open at ingest and query. **Coverage caveat:** `hype_search_enabled` is currently wired into the standard `PrepareChatContext` retrieval path only; the Supervisor / Plan-Execute / Agentic orchestrators do not yet carry the HyPE arm (follow-up). Design: `docs/superpowers/specs/2026-06-01-hype-design.md`.

**SECURITY:** the read-only role's `search_path` must **NOT** include `tabular`. Per-KB isolation depends on schema-qualified `tabular.<name>` references — unqualified table names must fail to resolve so a prompt-injected bare name cannot bypass the catalog allowlist.

**Phase 1 limits:** first row = header; multi-row headers / merged cells / legacy BIFF `.xls` fall back to text; materializer buffers each sheet in memory before `COPY` (multi-hundred-MB spike at 1M rows). **Phase 2** adds a synthetic `_rowid bigint` to each sheet and per-row embeddings for heuristic-selected TEXT columns; fuzzy hit → pivot to `table_query WHERE _rowid IN (...)`; fuzzy→aggregate is bounded by kb_search top-k. **Phase 3** is prompt-guidance only: answer prompt teaches Recharts JSON in a ` ```chart ` fenced block rendered by the existing frontend ChartRenderer; aggregations use SQL GROUP BY, non-SQL reshapes use plan-time code_exec (still gated by `chat_code_exec_enabled`); LLM-reliability-bound. Design specs: `docs/superpowers/specs/2026-05-28-tabular-data-qa-design.md`, `…-phase2-design.md`, `…-phase3-design.md`.

## Model tier resolution

Cost-optimization knob orthogonal to the feature recipes above. Each fast-tier task (CRAG grader, KG extractor, contextual enricher, factuality verifier, Self-RAG verifier, DAG critic, longmem extractor, KB router, RAPTOR summariser, **query decomposer (T1-1), longmem conflict classifier (T1-3), evidentiality classifier (T2-3), HyPE question generator**) resolves its model in this chain (first non-empty wins):

1. The task's per-task site_config key (e.g. `crag_grader_model`, `kg_extraction_model`, `query_decompose_model`, `chat_longmem_conflict_model`, `chat_context_compression_model`, `hype_model`)
2. `model_tier_fast` — deployment-wide fast-tier default
3. The KB's default chat model (legacy fallback)

Helper: `chat.ResolveFastTierModel(ctx, reader, perTaskKey)` — the single resolution function, callable from any package with a `SiteConfigReader`. Reasoning-heavy tasks (answer generation, plan decomposition, refine path) intentionally bypass this chain and use the KB chat model directly.

## Current Runtime Notes

- Data Explorer routes exist in the Go server but currently return `501`
- `POST /api/describe-image` currently returns `501`
- the default deployment is Go-only

## Document parsing

Default: `pdftotext` (PDFs) + built-in DOCX/PPTX parsers (flattened tables, dropped footnotes). Opt-in layout-aware parsing via Docling sidecar — see `docs/observability/docling.md`. When enabled (admin Agent panel → `docling_enabled` + `docling_base_url`) and reachable, all newly ingested PDF, DOCX, and PPTX files route through Docling; failures fall back to the built-in parsers (logged with `request_id`).

## Retrieval

Full mechanism, rationale, and operational eval history live in **`docs/retrieval.md`**. The pipeline assembles a top-k chunk list per chat turn from BM25 + vector + cross-encoder reranker, with MMR diversity, BM25-floor reinsertion, optional CRAG grading and rewrite, optional enumeration pre-pass, contextual-prefix prompt assembly, and post-answer citation/factuality validation. Ingest-side chunk dedup (`content_hash`) is documented in the same file under "Ingestion deduplication".

Subsystem index — pair a knob with the doc section that explains it:

| Knob(s) | Role | docs/retrieval.md section |
|---|---|---|
| `mmr_lambda` | Top-k diversity vs. relevance | MMR (top-k diversity) |
| `auto_spell_correct` | Silent query spell-correction | Auto spell correction |
| `step_back_enabled` | LLM-generated broader query for complex_reasoning | Step-back prompting |
| `query_decompose_enabled` + `query_decompose_model` (T1-1) | Sub-question decomposition into 2-4 distinct sub-queries; folds into RRF via `SubQueries` | (not yet documented in `docs/retrieval.md`) |
| `query_cache_enabled` + threshold/TTL | pgvector-backed result cache, exact + semantic tiers | Semantic query cache |
| `query_cache_similarity_threshold_lookup` / `_enumeration` / `_complex_reasoning` (T0-4) | Per-query-type cache hit thresholds; sentinel 0 inherits the base | (not yet documented in `docs/retrieval.md`) |
| `rag_fusion_enabled` | RAG-Fusion: per-alt-query BM25 arm folds into RRF alongside the existing per-alt vector arm | RAG-Fusion (per-alt-query BM25) |
| `rerank_blend_alpha` + `_lookup` / `_enumeration` / `_complex_reasoning` | Reranker vs. RRF blend (prod default 0.8) | Reranker score weighting |
| `hybrid_dynamic_alpha_enabled` + `_sensitivity` (T2-4) | Per-query α shift from BPE-token rarity; composes with the per-route overrides | (not yet documented in `docs/retrieval.md`) |
| `top_n_lookup` / `top_n_enumeration` / `top_n_complex_reasoning` | Per-route candidate pool size | Per-query-type top-N overrides |
| `bm25_simple_arm_enabled` | Second tsvector column with `simple` regconfig (no stemming); recovers chunks the German stemmer destroys | (admin Agent panel) |
| `bm25_tiered_boost_enabled` (T0-3) | Strict-form match × 100, OR-tokens floor × 10 multiplier on language-stemmer ts_rank | (not yet documented in `docs/retrieval.md`) |
| (no knob — always on) | BM25-floor reinsertion at the end of search | BM25 floor |
| (runtime, T0-1) | `hnsw.iterative_scan = relaxed_order` set per connection — required for filtered ANN recall | (not yet documented in `docs/retrieval.md`) |
| (reranker config) | Reranker model + serving caveats (jina-v3 prod; Qwen3 antipattern) | Reranker deployment |
| (admin `embedding_model`) | Production embedder (qwen3-embedding-8b, 4096-dim) | Embedder choice (production default) / Historical: Octen-8B, Qwen3 4B vs 8B |
| `query_instruction` | Qwen3 asymmetric prefix (keep empty under calibrated reranker) | Query-side embedding instruction |
| `crag_enabled` + `adaptive_routing_enabled` + `crag_grader_model` + `crag_min_relevant_chunks` | Corrective-RAG grading + lookup/enum skip rule | Corrective RAG (CRAG) |
| `chat_context_compression_enabled` + `_min_chunks` + `_threshold` + `_model` (T2-3) | ECoRAG evidentiality-based post-rerank filter (1 fast-tier LLM call) | ECoRAG evidentiality compression |
| `chat_longcontext_enabled` + `_max_tokens` (T2-1) | System-2 wide-retrieval routing for global-synthesis queries; skips MMR + score-drop + compression | (see `docs/agent-orchestration.md` → Long-context routing) |
| `citation_validation_enabled` | Deterministic per-`[N]` n-gram check | Citation validator |
| `contextual_enrichment` + `contextual_enrichment_model` | Anthropic-style 1-sentence chunk prefix at ingest | Contextual Retrieval (Anthropic-style) |
| (no knob — auto when prefix is populated) | Surface `contextual_prefix` in the chat-time prompt | Contextual prefix at chat time |
| (auto-classified, see `IsEnumerationQuery`) | Two-pass pipeline for list-style queries | Enumeration pre-pass / BM25-seeded extraction with deterministic post-processing |
| (no knob — applied when chunks exceed budget) | Token-budget truncation of retrieved chunks | Context truncation |
| (no knob — applied when query contains `"..."`) | Required exact-phrase matches in BM25 | Quoted-phrase boosting in keyword search |

## Agentic chat orchestration

Full mechanism for every orchestrator and chat-pipeline feature lives in **`docs/agent-orchestration.md`**. Streaming chat for `complex_reasoning` queries dispatches through the first orchestrator whose flag is on (priority order below); default deployment runs the legacy 2-step `RunDeepChat`.

| # | Orchestrator | Flag(s) | Shape |
|---|---|---|---|
| 1 | Supervisor | `chat_supervisor_enabled` | One classification call → specialist (RetrieverAgent / EnumeratorAgent) → search → answer |
| 2 | Plan-and-Execute | `chat_plan_execute_enabled` (+ `_dag`, `_dag_iterative`, `_tool_aware`) | Plan → Iterate → Generate; flat or DAG, optional inter-level critic, optional tool-aware planner |
| 3 | Agentic | `chat_agentic_enabled` (+ `_plateau_stop`, `_max_hops`) | Hop-1 → critique LLM → optional follow-up hops |
| 4 | Standard `PrepareChatContext` | (always-on fallback) | CRAG + enumeration pre-pass + contextual prefix + sandwich order |

Feature index — see `docs/agent-orchestration.md` for the full per-feature rationale, metrics, and design decisions:

| Feature | Knob | docs section |
|---|---|---|
| Trajectory streaming | (default on) | Trajectory streaming |
| Admin metrics panel | (always-on) | Admin agent-metrics panel |
| MCP tool registry | `chat_use_mcp_tools` | MCP tool registry |
| Session memory | `chat_session_memory_enabled` | Session memory |
| Factuality verifier | `chat_factuality_verifier_enabled` (+ `_always_run`, `_model`) | Factuality verifier |
| Refine gate | `chat_factuality_gate_enabled` + `chat_refine_model` | Factuality refine gate |
| Refine SSE diff | (gated by refine gate) | Refine SSE diff |
| Turn budget | `chat_turn_budget_seconds` / `_tokens` / `_tool_calls` | Turn budget |
| KB router | `chat_kb_router_enabled` + `chat_kb_router_min_confidence` + `?route=auto` | Sub-KB router |
| Retrieval-tier tools (keyword_search, chunk_read, document_outline) | (registered alongside `kb_search`) | Retrieval-tier tools |
| Non-retrieval tools (calculator, sql_query, code_exec) | `chat_code_exec_enabled` for code_exec | Non-retrieval tools |
| table_query tool (structured spreadsheet SQL) | `chat_tabular_query_enabled` + migration 0048 + OPERATOR PREREQUISITE grants | (see CLAUDE.md recipe above; design: `docs/superpowers/specs/2026-05-28-tabular-data-qa-design.md`) |
| Tool-aware DAG planner | `chat_plan_execute_tool_aware` | Tool-aware DAG planner |
| Tool-mix telemetry | (always-on via dispatcher) | Tool-mix telemetry |
| Answer-time tool calling | `chat_answer_tools_enabled` + `chat_answer_tools_max_rounds` | Answer-time tool calling |
| KG extraction | `kg_extraction_enabled` + `kg_extraction_model` | Knowledge-graph extraction |
| graph_search tool | (registered with MCP) | Graph search tool |
| Graph-routing heuristic | `chat_graph_routing_enabled` | Graph-routing heuristic |
| Graph-routing chunk injection | `chat_graph_routing_inject_chunks` + `chat_graph_routing_max_chunks` | Graph-routing chunk injection (RRF extraLists) |
| Graph-routing traversal mode | `chat_graph_routing_path_mode` (`neighbors`\|`ppr`\|`paths`) + per-mode tuning (T1-4 / T1-5) | Graph-routing traversal modes |
| Long-term memory | `chat_longmem_enabled` + `_min_salience` + `_recall_top_k` + `_decay_days` | Long-term per-user memory |
| Long-term memory ANN recall | `chat_longmem_recall_semantic` (T1-2; needs dim migration — see recipe) | (stub in `docs/agent-orchestration.md` — see CLAUDE.md recipe above) |
| Long-term memory conflict resolution | `chat_longmem_conflict_resolution` + `_model` + `_candidates` (T1-3) | (stub in `docs/agent-orchestration.md` — see CLAUDE.md recipe above) |
| Sub-question decomposition | `query_decompose_enabled` + `query_decompose_model` (T1-1) | (stub in `docs/agent-orchestration.md` — see CLAUDE.md recipe above) |
| ECoRAG context compression | `chat_context_compression_enabled` + `_min_chunks` + `_threshold` + `_model` (T2-3) | (see `docs/retrieval.md` → ECoRAG evidentiality compression) |
| Long-context (System 2) routing | `chat_longcontext_enabled` + `_max_tokens` (T2-1) | Long-context (System 2) routing |
| Self-RAG verifier | `chat_self_rag_enabled` + `chat_self_rag_model` (mutually exclusive with factuality verifier) | Self-RAG verifier |
| Iterative DAG critic | `chat_plan_execute_dag_iterative` + `_model` | Iterative DAG critic |
| CI eval gate | (CI-only, `.github/workflows/eval.yml`) | CI eval gate |
| Online faithfulness metric | (auto-on whenever factuality verifier or Self-RAG ran) | Online faithfulness metric |

## Observability

All HTTP requests get an `X-Request-Id` (auto-generated if absent) which is propagated through `context.Context`. Pipeline code uses `logctx.From(ctx).Info(...)` so structured logs carry `request_id`, `user_id`, `kb_id`. Background worker tasks bootstrap their own request id at task start.

Trace one request end-to-end:

```
docker compose logs go-server go-worker | jq 'select(.request_id == "<id>")'
```

Stage names emitted by the RAG pipeline: `search`, `factcheck`, `llm_completion`. Worker task lifecycle: `worker.task.start` / `worker.task.end` with `task_type` and `duration_ms`.

RAG-specific Prometheus metrics (counters + histograms) are exposed at `GET /metrics` (admin-protected) under names prefixed `rag_`. See `internal/observability/metrics.go` for the full list.

OpenTelemetry tracing is opt-in via env. Set `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` (and `OTEL_EXPORTER_OTLP_TRACES_HEADERS=Authorization=Basic <base64(pk:sk)>` for self-hosted Langfuse) to emit traces with nested spans for `chat.send_message` → `rag.search` → `rag.embed`, `rag.llm_completion`, `rag.factcheck`. When the env var is unset, tracing is a no-op (zero overhead). Logs include `trace_id` + `span_id` when a span is active so logs and traces are joinable.

Per AI message, the OTel `trace_id` is persisted on the `messages` table when tracing is enabled. Admins see a "View trace" button next to each AI message that deep-links to the configured Langfuse instance (set `langfuse_base_url` in the admin Agent panel; URL pattern is `<base>/<trace_id>`).

## Research agent

The research agent stops early when `MaxConsecutiveStepsWithoutFindings` consecutive iterations add no new (non-duplicate) findings — default 2. Independent of the existing LLM-based "isComplete" judgment; deterministic guard against tangential or repetitive query plans.

## Git Policy

- Never run `git add`, `git commit`, or `git push` without explicit user permission.
