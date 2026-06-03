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
- `model_tier_fast` — deployment-wide default for fast-tier tasks (CRAG grader, KG extractor, contextual enricher, factuality / Self-RAG verifier, DAG critic, longmem extractor, KB router, RAPTOR summariser, **query decomposer, longmem conflict classifier, evidentiality classifier, HyPE question generator, golden-set question generator**); per-task `*_model` keys override

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
| 0049 | `eval_golden_set_jobs` (async corpus → golden-set generation status) |
| 0052 | `message_chunks` (answer→chunk links for the online feedback loop) |

**Vector tables** are dim-keyed (`document_chunks_2560`, `document_chunks_4096`, …); switching the embedder requires a re-ingest.

**Read order for newcomers:** Architecture → Internal package map → Feature enablement recipes → `docs/retrieval.md` (full pipeline mechanism) → `docs/agent-orchestration.md` (orchestrators + features).

## Deep dives

CLAUDE.md is the operational reference (commands, env, architecture, feature index). The two retrieval/orchestration subsystems each have a dedicated mechanism-and-rationale doc — open them when you need the *why* behind a knob or feature:

- **`docs/feature-recipes.md`** — full enablement toggle blocks (combined flag lists in dependency order, operator-prerequisite SQL grants, ops sequences, provider caveats, security notes) for every gated feature indexed in the Feature-enablement-recipes table below.
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
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl --production-context [--enhance …] [--hyde] [--multi-query] [--crag on|off] [--enumeration on|off] [--orchestrator-dispatch=true|false]` — eval through the production chat pipeline (CRAG, enumeration, contextual prefix, sandwich order). `--orchestrator-dispatch` defaults **true** (each question routes through the production orchestrator predicate; report records per-question `agent` + per-orchestrator aggregates); set `=false` for byte-stable diffs against pre-2026-05 retrieval-only runs.
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl --trajectory --orchestrator off|agentic|plan_execute|plan_execute_dag|supervisor|all [--judge]` — trajectory eval: per (question, orchestrator) emits the full agent decision sequence to `eval-trajectory.jsonl` + `.aggregate.json`. `--judge` adds four LLM judges (decomposition coverage, decision correctness, rewrite utility, tool-call correctness).
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl --depth-buckets [--depth-buckets-min-chunks 4]` — position-aware analysis: adds a `depth_buckets` section (per-quartile relevant vs non-relevant counts, bucketed by chunkIndex/totalChunks) to surface position bias toward the start of long files. NOT a true RULER needle test.
- `cd go-backend && go build ./cmd/eval-gen`
- `cd go-backend && ./cmd/eval-gen --kb-id <uuid> [--lookup 20] [--complex 10] [--enumeration 5] [--multihop 5] [--lang de|en] [--out <path>] [--model <m>]` — synthesize a **draft** golden set from a KB's ingested chunks (lookup / complex / enumeration / multi-hop via semantic-neighbor pairing; emits both `must_cite_file_ids` and `must_cite_file_names`). Output is a curation draft — review before use. Also available async from the admin Eval tab ("Generate from corpus"), which saves the result into `eval_golden_sets` (migration 0049 `eval_golden_set_jobs` tracks job status; description prefix `auto-generated from corpus`).
- `cd go-backend && go test ./...`

**Profile-Guided Optimization (optional, ~5-15% CPU):** build is PGO-ready (`-pgo=auto` auto-detects `default.pgo` in the main package dir). Capture a 30s CPU profile under prod load (`PPROF_ENABLED=1`), drop it next to the entrypoint, rebuild. Re-capture every few weeks as the profile drifts; none committed yet (needs a real prod profile).

```bash
curl "http://<internal-host>:6060/debug/pprof/profile?seconds=30" -o default.pgo
mv default.pgo go-backend/cmd/server/default.pgo   # committed → picked up by -pgo=auto
cd go-backend && go build ./cmd/server
```

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
- **Postgres (read-only, optional):** `JUSTRAG_DB_URL_READONLY` — raw DSN for a least-privilege SELECT-only role backing the `sql_query` and `table_query` MCP tools. **Required when `chat_tabular_query_enabled = true`** (and recommended whenever the `sql_query` tool is enabled); the tool falls back to a disabled stub when unset. See the Structured spreadsheet Q&A recipe in `docs/feature-recipes.md` for the SELECT-grant prerequisite.
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

Most chat-pipeline features default OFF. The **full toggle blocks** (combined flag lists in dependency order, operator-prerequisite SQL grants, post-embedder-change ops sequences, provider caveats, security notes) live in **`docs/feature-recipes.md`**. This table is the index: feature → flags → migration → recipe§.

| Feature | Key flags | Mig | `docs/feature-recipes.md` § |
|---|---|---|---|
| Refine gate + KB router + turn budget | `chat_factuality_verifier_enabled`, `chat_factuality_gate_enabled`, `chat_kb_router_enabled` (+ `?route=auto`), `chat_turn_budget_seconds` | 0042 | Refine gate + KB router + turn budget |
| Tool-aware planner + tool tier | `chat_plan_execute_enabled` (+ `_dag`, `_tool_aware`), `chat_code_exec_enabled` | 0043 | Tool-aware planner + tool tier |
| Answer-time tool calling | `chat_answer_tools_enabled` + `chat_answer_tools_max_rounds` | 0043 | Answer-time tool calling |
| Long-term memory + Self-RAG | `chat_longmem_*` (`_recall_semantic`, `_conflict_*`), `chat_self_rag_enabled`, `chat_plan_execute_dag_iterative` | 0045 | Per-user long-term memory + Self-RAG |
| Knowledge-graph routing | `kg_extraction_enabled`, `chat_graph_routing_enabled` (+ `_inject_chunks`, `_path_mode` ppr/paths) | 0044 | Knowledge-graph routing |
| Sub-question decomposition (T1-1) | `query_decompose_enabled` + `query_decompose_model` | — | Sub-question decomposition |
| Tiered BM25 boost (T0-3) + cache thresholds (T0-4) | `bm25_tiered_boost_enabled`, `query_cache_similarity_threshold_*` | — | Tiered BM25 boost + per-query-type cache thresholds |
| Dynamic alpha (T2-4) | `hybrid_dynamic_alpha_enabled` + `_sensitivity` | — | Dynamic alpha |
| Online feedback loop | `chat_feedback_boost_enabled` + `feedback_boost_weight` | 0052 | Online feedback loop |
| ECoRAG compression (T2-3) | `chat_context_compression_enabled` (+ `_min_chunks`, `_threshold`, `_model`) | — | ECoRAG evidentiality compression |
| Long-context routing (T2-1) | `chat_longcontext_enabled` + `_max_tokens` | — | Long-context routing |
| Late chunking | `late_chunking_enabled` + `_max_input_tokens` | — | Late chunking |
| RAPTOR indexing | `raptor_enabled` (+ `_clustering_algorithm`, `_branching_factor`, …) | 0046 | RAPTOR hierarchical indexing |
| Tabular Q&A (table_query) | `chat_tabular_query_enabled` (+ `_semantic_columns_enabled`, `_charts_enabled`) — **needs OPERATOR PREREQUISITE grants** | 0048 | Structured spreadsheet Q&A |
| HyPE | `hype_enabled` (ingest) + `hype_search_enabled` (query) | — | HyPE — hypothetical prompt embeddings |

Mutual exclusions and ordering gotchas (e.g. `chat_self_rag_enabled` REPLACES `chat_factuality_verifier_enabled`; `raptor_enabled` vs `parent_child_enabled`; the T1-2 dim re-embed sequence before `chat_longmem_recall_semantic`) are documented inline in each recipe.

## Model tier resolution

Cost-optimization knob orthogonal to the feature recipes above. Each fast-tier task (CRAG grader, KG extractor, contextual enricher, factuality verifier, Self-RAG verifier, DAG critic, longmem extractor, KB router, RAPTOR summariser, **query decomposer (T1-1), longmem conflict classifier (T1-3), evidentiality classifier (T2-3), HyPE question generator, golden-set question generator**) resolves its model in this chain (first non-empty wins):

1. The task's per-task site_config key (e.g. `crag_grader_model`, `kg_extraction_model`, `query_decompose_model`, `chat_longmem_conflict_model`, `chat_context_compression_model`, `hype_model`)
2. `model_tier_fast` — deployment-wide fast-tier default
3. The KB's default chat model (legacy fallback)

Helper: `chat.ResolveFastTierModel(ctx, reader, perTaskKey)` — the single resolution function, callable from any package with a `SiteConfigReader`. Reasoning-heavy tasks (answer generation, plan decomposition, refine path) intentionally bypass this chain and use the KB chat model directly.

## Current Runtime Notes

- Data Explorer routes exist in the Go server but currently return `501`
- the default deployment is Go-only

## Document parsing

Default: `pdftotext` (PDFs) + built-in DOCX/PPTX parsers (flattened tables, dropped footnotes). Opt-in layout-aware parsing via Docling sidecar — see `docs/observability/docling.md`. When enabled (admin Agent panel → `docling_enabled` + `docling_base_url`) and reachable, all newly ingested PDF, DOCX, and PPTX files route through Docling; failures fall back to the built-in parsers (logged with `request_id`).

**Image description (`POST /api/describe-image`)**

```
describe_image_enabled = true                   # gate; default off (admin Agent panel → ingestion section)
describe_image_model   = <vision-capable model> # required; falls through model_tier_fast
```

Multipart `image` field (PNG/JPEG/WEBP/GIF, ≤10 MB) + optional `prompt` form field; returns `{description}`. Sends an OpenAI-style multimodal `content` array (text + `image_url` data-URI) via `ai.DescribeImage` to the resolved provider. Disabled or unconfigured → 503; the default `gemma-4-26b` deployment is **NOT** assumed vision-capable — point `describe_image_model` at a vision model. Toggleable from the admin Agent panel (`describe_image_enabled` + `describe_image_model`). No migration. `internal/misc/describe_image.go`, `internal/ai/vision.go`.

## Retrieval

Full mechanism, rationale, and operational eval history live in **`docs/retrieval.md`**. The pipeline assembles a top-k chunk list per chat turn from BM25 + vector + cross-encoder reranker, with MMR diversity, BM25-floor reinsertion, optional CRAG grading and rewrite, optional enumeration pre-pass, contextual-prefix prompt assembly, and post-answer citation/factuality validation. Ingest-side chunk dedup (`content_hash`) is documented in the same file under "Ingestion deduplication".

Per-knob subsystem index (each knob → the `docs/retrieval.md` section that explains its mechanism, eval numbers, and antipatterns) lives in **`docs/retrieval.md`**; the T-series tuning knobs (`query_decompose_*`, `query_cache_similarity_threshold_*`, `hybrid_dynamic_alpha_*`, `bm25_tiered_boost_enabled`) are in **`docs/feature-recipes.md`**; the runtime-only `hnsw.iterative_scan` (T0-1) is in the Quick-reference runtime note + `docs/runbooks/hnsw-reindex.md`. Always-on, knob-less stages (BM25-floor reinsertion, context truncation, quoted-phrase boosting, enumeration pre-pass) are documented under their own headings in `docs/retrieval.md`.

## Agentic chat orchestration

Full mechanism for every orchestrator and chat-pipeline feature lives in **`docs/agent-orchestration.md`**. Streaming chat for `complex_reasoning` queries dispatches through the first orchestrator whose flag is on (priority order below); default deployment runs the legacy 2-step `RunDeepChat`.

| # | Orchestrator | Flag(s) | Shape |
|---|---|---|---|
| 1 | Supervisor | `chat_supervisor_enabled` | One classification call → specialist (RetrieverAgent / EnumeratorAgent) → search → answer |
| 2 | Plan-and-Execute | `chat_plan_execute_enabled` (+ `_dag`, `_dag_iterative`, `_tool_aware`) | Plan → Iterate → Generate; flat or DAG, optional inter-level critic, optional tool-aware planner |
| 3 | Agentic | `chat_agentic_enabled` (+ `_plateau_stop`, `_max_hops`) | Hop-1 → critique LLM → optional follow-up hops |
| 4 | Standard `PrepareChatContext` | (always-on fallback) | CRAG + enumeration pre-pass + contextual prefix + sandwich order |

Per-feature index — each chat-orchestration feature (trajectory streaming, MCP tool registry, session memory, factuality verifier, refine gate, turn budget, KB router, retrieval/non-retrieval tools, tool-aware DAG planner, answer-time tool calling, KG extraction + graph_search + graph-routing modes, long-term memory + ANN recall + conflict resolution, sub-question decomposition, ECoRAG compression, long-context routing, Self-RAG verifier, iterative DAG critic, online faithfulness metric + feedback loop) with its knob, rationale, metrics, and design notes is indexed in **`docs/agent-orchestration.md`**. Enablement toggle blocks for the gated ones are in **`docs/feature-recipes.md`**.

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
