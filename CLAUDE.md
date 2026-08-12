# JustRAG

Go-first RAG application with a React frontend, PostgreSQL + pgvector, Redis, and Asynq workers.

## Quick reference

**Site_config flag namespaces** (admin Agent panel; long-form mechanism docs in `docs/retrieval.md` and `docs/agent-orchestration.md`):

- `chat_*` — chat orchestrators, gates, post-response
- `chat_answer_history_*`, `chat_transform_followup_enabled` — answer-time conversation history (the answer LLM was single-turn until 2026-06) + retrieval-free reformat follow-ups ("das als Tabelle"); both **default ON** (correctness fixes; the keys are kill switches)
- `chat_date_awareness_enabled` (default **ON**, kill switch) — injects the current date (`chat_date_timezone`, default `Europe/Berlin`) into the answer system prompt so the LLM resolves "today"/"yesterday"/"since May". `chat_date_tools_enabled` (default off) + `chat_date_tools_max_results` (default 50) enable the `recent_documents` MCP tool (list files added in a date window). `kb_search` also accepts optional `date_from`/`date_to` (always available). Date windows key on `files.created_at` (ingest time; a future `published_at` swaps in via the single `effectiveDateExpr` in `internal/vector/recency_boost.go`). `chat_recency_listing_enabled` (default **ON**, kill switch; + `_window_days` 7, `_max_results` 50, `_name_match_enabled` ON) — deterministic path for "Welche neuen Meldungen gibt es?"-style queries: `IsRecencyListingQuery` window-scopes retrieval via `SearchOptions.CreatedAfter` and injects the complete file listing for the window as a system-prompt addendum (semantic retrieval alone returns an arbitrary subset for content-free recency queries; the listing supersedes the enumeration pre-pass). The name-match arm additionally includes files whose NAME carries a word-boundary "neu"/"new" label (CERT advisories: NEU vs UPDATE) even outside the window — when the query says "neu"/"new", no user file selection exists, and the window listing isn't truncated, retrieval switches from CreatedAfter to an explicit FileIDs union so those stay citable. Standard `PrepareChatContext` path only. `internal/chat/date_prompt.go`, `internal/chat/recency_listing.go`, `internal/mcp/builtin/recent_documents.go`.
- `chat_longmem_*` — per-user long-term memory (incl. `_recall_semantic`, `_conflict_*` for ANN + Mem0 conflict resolution)
- `chat_longcontext_*` — System-2 long-context routing for global-synthesis queries
- `chat_context_compression_*` — ECoRAG evidentiality-based post-rerank filtering
- `chat_sufficient_context_*` — holistic "does the assembled set suffice?" abstention gate (standard + supervisor paths)
- `chat_compare_*` — in-chat single-file comparison against the KB (upload a file in chat; contradiction / formal / completeness modes). `_enabled` master gate (default off), `_model` (fast-tier), `_max_sections`, `_concurrency`, `_peers_per_section`, `_attachment_ttl_hours`, `_max_file_bytes`. File parsed in memory, held in a Redis-backed `chatattach` store (24h TTL), never ingested. New package `internal/chatattach`; endpoint `POST /api/kb/{id}/chat/attachment`.
- `crag_*`, `adaptive_routing_*` — corrective-RAG + skip rules
- `kg_*`, `chat_graph_routing_*` — knowledge-graph extraction + routing (incl. `_path_mode` PPR/PathRAG trichotomy; `chat_graph_routing_ppr_dual_node_enabled` (HippoRAG 2 dual-node passage PPR), `chat_graph_routing_ppr_triple_filter_enabled` (+ `_model`, `_max_triples`) — recognition-memory seed prune/expand; both default off; `chat_graph_routing_bridge_rerank_enabled` (+ `bridge_boost_weight`, default 0.1) — post-rerank boost for multi-hop bridge chunks on a KG path between matched entities; default off). `kg_canonicalization_enabled` (+ `_threshold` 0.85, `_max_pairs` 200, `_model`) — admin-triggered batch entity dedup (`POST /api/kb/{id}/canonicalize`): embedding-cosine candidates, LLM-confirmed, merged via edge re-pointing; default off. `internal/canonicalize`. `kg_communities_enabled` (+ `_resolution` 1.0, `_min_size` 3, `_summary_model`, `_summary_input_cap`) — admin-triggered batch (`POST /api/kb/{id}/communities/build`): topology-Louvain communities over kg_edges → per-community LLM summaries stored as `node_kind='community_summary'` chunks; default off. `internal/community`. (Index for a future DRIFT global-search.) `chat_community_search_enabled` (+ `_top_k` 8) — community-primed global search: for global-synthesis queries, inject KG community summaries (from `kg_communities_enabled`) into the answer pool; community summaries are excluded from normal retrieval by default. Default off. `internal/chat/community_search.go`. `chat_drift_enabled` (+ `_max_followups` 4, `_primer_top_k` 6, `_search_top_k` 8, `_model` fast-tier) — full iterative DRIFT: a global-synthesis orchestrator that reads KG community summaries (the primer), generates follow-up sub-questions, runs one light local search per follow-up, and assembles a single answer pass. Gated on `complex_reasoning` + `IsGlobalSynthesisQuery`; takes priority over the other orchestrators for those queries. Default off. `internal/chat/drift_chat.go`.
- `query_cache_*`, `step_back_*`, `mmr_*`, `rerank_blend_alpha*`, `top_n_*` — retrieval pipeline (`query_cache_similarity_threshold_*` for per-query-type thresholds)
- `query_decompose_*` — sub-question decomposition (DecomposeRAG)
- `bm25_simple_arm_enabled`, `bm25_tiered_boost_enabled` — BM25 keyword-arm tuning
- `hybrid_dynamic_alpha_*` — per-query α shift from BPE-token rarity
- `recency_*` — exponential-decay freshness boost post-rerank (RSS/Confluence KBs; keyed on files.created_at)
- `rss_wid_enrichment_enabled` — when an RSS item links to a CERT-Bund/WID advisory (`wid.cert-bund.de`), fetch the WID JSON API and ingest structured markdown (CVSS scores, affected products, CVE list, references) instead of generic HTML full-text. Requires the feed's `fetch_full_text=true`. Default **ON** (kill switch). `internal/widcert`; enrichment branch in `worker/rsspoll.go`.
- `contextual_enrichment*`, `parent_child_*`, `docling_*` (incl. `_picture_description_enabled` + `_picture_area_threshold` for gemma-4 image captioning, `_table_mode` fast/accurate), `late_chunking_*`, `embedding_batch_size`, `ingest_enrich_concurrency` (default 10), `kg_extraction_concurrency` (default 4) — ingestion (the two `*_concurrency` keys cap per-file LLM fan-out; see also the `AI_MAX_CONCURRENT_REQUESTS` runtime note for the global ceiling)
- `raptor_*` — per-file RAPTOR hierarchical summary trees (`raptor_clustering_algorithm` selects kmeans vs leiden)
- `chat_tabular_*`, `tabular_semantic_*` — structured spreadsheet Q&A + fuzzy free-text-cell search + charts/pivots (Phase 1/2/3; `chat_tabular_charts_enabled` is the Phase-3 flag)
- **User-created agents & agent teams** (no master flag — feature is live once migrated; per-user, off-path until a user attaches something): users create agents (persona prompt + model override + tool allowlist + retrieval-knob overrides via a third config-overlay layer agent→KB→global, allowlist = per-KB registry minus RequiresReingest keys) and teams (LLM router → parallel specialists → synthesis; router model via `agent_team_router_model` → fast tier). Attach to KBs (edit perm), pick per chat session (sticky on `chats.team_id/agent_id`); explicit selection beats every flag-driven orchestrator except in-chat comparison and runs regardless of query classification. `agents_allow_privileged_tools` (default **off**) gates `code_exec`/`sql_query`/`web_search` out of user agents — enforced at save AND dispatch time (`chat.RestrictedDispatcher`); answer-time tools are disabled on team synthesis turns (persona-influenced findings in prompt). Fail-soft everywhere: unresolved/disabled/empty selections degrade to the standard path with a `team_unavailable` trajectory event. Tables `agents`/`agent_teams`/`agent_team_members`/`agent_kb_links`/`team_kb_links` (mig 0061); message attribution `messages.team_id/agent_id` + `eval_runs.team_id` (mig 0063). **Per-team eval:** `cmd/eval --production-context --team-id <uuid>` (every question through the real `RunTeamChat`, HARD per-question errors — no silent standard-path fallback; judge composes; per-agent config overlay applied) or the KB eval tab's team dropdown (async path). `internal/agentteams`, `internal/chat/team_{router,specialist,chat}.go`, `internal/chat/restricted_dispatcher.go`, `internal/prompts/team.go`, `internal/eval/team_adapter.go`; FE `web/src/components/agents/`. publicapi/openaicompat/mcpserver paths ignore selections (v1). Report note: a team whose router selects no specialist yields per-question ERRORS (by design), not zero-recall rows.
- `mcp_server_enabled` — expose each KB as an MCP server at `POST /api/v1/kb/{id}/mcp` (single tool `ask_kb`, runs the real RAG pipeline; per-KB access via existing API-key + KB-permission chain). Default off. `internal/mcpserver`.
- `git_repo_enabled` — gated KB source that shallow-clones a git repository (go-git v5, HTTPS-only, optional PAT encrypted at rest) and ingests text/code files as individual `files` rows (`origin='git'`) under one grouped source; manual re-sync only; default off. `internal/gitrepo`; migration 0060.
- `ragas_*`, `factcheck_*`, `citation_validation_*`, `langfuse_*` — validation + observability
- `model_tier_fast` — deployment-wide default for fast-tier tasks (CRAG grader, KG extractor, contextual enricher, factuality / Self-RAG verifier, DAG critic, longmem extractor, KB router, RAPTOR summariser, **query decomposer, longmem conflict classifier, evidentiality classifier, HyPE question generator, golden-set question generator, sufficient-context gate, comparison section checker** (`chat_compare_model`), **DRIFT follow-up generator** (`chat_drift_model`)); per-task `*_model` keys override. All fast-tier JSON calls send strict `json_schema` Structured Outputs (vLLM guided_json) with auto-downgrade to `json_object` on backend rejection; tolerant parsing stays as last resort (`ai.GenerateCompletionStructured` / `structuredCompletionFn`). Sole exception: the tool-aware DAG planner (free-form per-tool `args` is incompatible with strict mode).

**Runtime-only knob (no site_config):** `hnsw.iterative_scan = relaxed_order` is set by `BuildPoolConfig`'s `AfterConnect` hook on every new pool connection (T0-1). Required for filtered ANN queries (kb_id, node_kind, GraphChunkIDs, file_id) to expand the HNSW candidate list until the WHERE clause is satisfied. Tolerates pgvector < 0.8 with a one-shot warning; no operator action needed when pgvector ≥ 0.8 is installed.

**Runtime-only knob (env, no site_config):** `AI_MAX_CONCURRENT_REQUESTS` (default unset = unbounded) caps simultaneous in-flight **unary** model-provider requests per endpoint (chat completion / embedding / rerank / models — the `ai.doJSON` path; streaming answers are deliberately exempt to avoid starving the unary calls answer-time tools depend on). Per-op fan-out caps (`ingest_enrich_concurrency`, `kg_extraction_concurrency`, multi-query, rerank, RAPTOR) bound each site locally but not their *product*; under an ingest burst that product can saturate the vLLM/embedding backend into timeouts. Set this to the backend's safe concurrent-request ceiling to turn saturation into backpressure. Deadlock-free by construction (each call holds ≤1 slot, released before any other acquire). `internal/ai/concurrency.go`.

**KB permission model (four roles):** `view < edit < admin < owner`, strictly ordered. `edit` fills the corpus (upload/delete files, RSS/Confluence/git sources, crawl); `admin` decides how it is processed and answered (name/description/prompt/models/tuning knobs, agents/teams, eval, canonicalize, communities, member management) — the dividing line is: anything that can force a re-ingest or change answer quality system-wide is `admin`, not `edit`. `owner` is unique per KB (partial unique index) and moves only through the explicit transfer endpoint, never through `/members`. `kb_members(kb_id, user_id, role)` is the **sole authority**; `knowledge_bases.user_id` survives only as a trigger-maintained mirror (`kb_members_sync_owner_trg`, fires on `INSERT OR UPDATE ... WHERE role='owner'`) that ~40 pre-existing owner-display queries still join through — never write it directly. `kbaccess.RequireKBRole(min)` is the only route gate and `kbaccess.EffectiveRole(kb, sysRole, memberRole)` the only resolution ladder (`internal/kbaccess/middleware.go`); `internal/files`, `internal/openaicompat`, and `internal/academic` call `EffectiveRole` directly for their second, handler-internal check instead of hand-rolling one. Five rules, in order — order is load-bearing (an explicit `kb_members` row must beat the implicit public-KB roles, or an editor on a public KB would be demoted to view): (1) system role `superadmin` → `owner`; (2) a `kb_members` row → that role; (3) `IsGlobal` + system role `admin` → `admin`; (4) `IsGlobal` + `IsPublished` → `view`; (5) else → 403. `knowledge_base_shares` and `global_kb_editors` are **legacy remnants** — Phase 1 (migration 0064) backfilled them into `kb_members` and left them in place (expand/contract); they are dropped only in a release after the still-unbuilt Phase 2 (`docs/superpowers/specs/2026-08-12-kb-rollen-und-sichtbarkeit-design.md`). Nothing consults either table for an access decision any more: the `/share*` surface was deleted, and the admin global-KB **editor** endpoints (`/api/admin/global-kbs/{id}/editors*`) now read and write `kb_members` with `role='admin'` — while they still touched `global_kb_editors`, adding an editor was a silent no-op grant and removing one left the backfilled `kb_members` admin row behind, i.e. an invisible and un-revokable KB-`admin` privilege. `internal/cascade` still DELETEs from both tables; that is cleanup, not access. Operator-visible behavior changes from Phase 1: a plain user can now reach their own KB's settings (`kbAdminChain` replaced `kbTuningChain`, dropping the extra system-role gate); **unpublished** global KBs are no longer readable by ordinary users on *any* `view`-gated route — the whole `kbViewChain` (chat, graph, studio/generated content, files, research, export) plus the `/api/v1/*` public API and the KB-as-MCP endpoint, which wrap `RequireKBRole(kbaccess.RoleView)` directly. The old `RequireKBPermission` granted `view` on any `IsGlobal` KB regardless of `IsPublished`; rule 4 now requires both (`files`, `openaicompat` and `academic` additionally repeat the check handler-internally, which is why they get named — the tightening is not limited to them).

**Migrations** (in `go-backend/migrations/main/`, sequential, idempotent — conventions in `go-backend/migrations/README.md`, incl. the `NO TRANSACTION` + `CREATE INDEX CONCURRENTLY` rule for indexes on already-large tables):

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
| 0054 | `files.error_stage` + `error_message` (ingestion error visibility + per-file/per-KB retry) |
| 0059 | `kg_communities` (GraphRAG community detection) |
| 0060 | `git_repo_sources` + `files.git_repo_source_id`/`git_file_path`/`git_blob_sha` (git repository ingestion) |
| 0064 | `kb_members` (four KB roles) + owner-mirror trigger; also **renames** `pending_kb_invites.permission` → `role` and widens its CHECK to allow `admin` |

**Vector tables** are dim-keyed (`document_chunks_2560`, `document_chunks_4096`, …); switching the embedder requires a re-ingest.

**Read order for newcomers:** Architecture → Internal package map → Feature enablement recipes → `docs/retrieval.md` (full pipeline mechanism) → `docs/agent-orchestration.md` (orchestrators + features).

## Deep dives

CLAUDE.md is the operational reference (commands, env, architecture, feature index). The two retrieval/orchestration subsystems each have a dedicated mechanism-and-rationale doc — open them when you need the *why* behind a knob or feature:

- **`docs/feature-recipes.md`** — full enablement toggle blocks (combined flag lists in dependency order, operator-prerequisite SQL grants, ops sequences, provider caveats, security notes) for every gated feature indexed in the Feature-enablement-recipes table below.
- **`docs/retrieval.md`** — every retrieval-pipeline subsystem (MMR, query cache, reranker blend, BM25 floor, embedder choice, CRAG, contextual retrieval, enumeration pre-pass, citation validator, ingestion dedup, …) with eval numbers and antipatterns.
- **`docs/presentation/tuning-knobs.md`** — admin-UI knob reference, oriented toward operations rather than mechanism.
- **`docs/observability/docling.md`** — opt-in Docling sidecar for layout-aware PDF parsing.
- **`docs/runbooks/hnsw-reindex.md`** — operator runbook for the T0-1 HNSW iterative-scan path (rebuilding the index after pgvector upgrade).
- **`docs/runbooks/migration-rollback.md`** — operator runbook for rolling back a migration with the goose CLI (`cmd/migrate` is up-only by design).
- **`docs/runbooks/release.md`** — cutting a versioned release, deploying it, and the rollback caveat that a release containing a migration cannot be reverted by re-pointing the image tag alone.
- **`docs/runbooks/stuck-quick-jobs.md`** — operator runbook for draining asynq tasks wedged in `active` (a `rollout restart` requeues rather than removes them; the pause → restart → delete-by-id sequence, plus the enqueue-site `asynq.Timeout` audit that prevents recurrence).

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
- `cd go-backend && ./cmd/eval/eval --golden ../eval/golden/<set>.jsonl --production-context --team-id <uuid>` — per-team eval: every question dispatches through the real `RunTeamChat` (team must be attached + enabled on the KB; requires `--production-context`; incompatible with `--trajectory`; errors are hard per-question, never a silent standard-path fallback)
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

## Releases

SemVer `0.x` via **annotated git tags** (`v0.1.0`); the tag is the only source
of truth. Pre-1.0: **minor** = features *and* breaking changes, **patch** =
fixes only (no migration, no changed `site_config` default, no re-ingest).
`v1.0.0` is reserved for the first public release. Independent of the API
version — `/api/v1/*`, OpenAI-compat, MCP, and the ILIAS shim stay `v1`.

- **CI:** `main` → `:edge` + `:<sha>`; tag `v*` → `:vX.Y.Z` + `:vX.Y` +
  `:stable` (prerelease tags like `v0.2.0-rc.1` deliberately do **not** move
  `:stable`, since it is the unpinned-compose default). **`:latest` is not
  published** — it was how prod drifted. What suppresses it is
  `flavor: latest=false` on the metadata step; `docker/metadata-action`
  defaults to `latest=auto` and generates `:latest` for `type=semver` by
  itself, so omitting it from `tags:` is not sufficient. Do not remove it.
- **`GET /version`** reports `git describe --tags --always` (`v0.1.0`, or
  `v0.1.0-12-gabc1234` on a main build), stamped in via `BUILD_VERSION`.
- **Pinning:** compose files interpolate `${JUSTRAG_VERSION:-stable}`; k8s
  worker manifests take a literal `:vX.Y.Z` + `imagePullPolicy: IfNotPresent`.
  The `go-server` Deployment lives outside this repo and is pinned manually.
- **`CHANGELOG.md`** is generated by git-cliff (`cliff.toml`) plus a
  hand-written `⚠ Upgrade notes` block per release (migrations, flipped flag
  defaults, re-ingest requirements).
- **Migrations on deploy:** compose applies them automatically (the `migrate`
  one-shot service + `service_completed_successfully` gates). **k8s does
  not** — there is no migrate Job and neither binary self-migrates, so the
  runbook's `kubectl run … /app/migrate` step is mandatory before
  `kubectl apply`.
- **A release whose upgrade notes list a migration has no one-step rollback**
  — `cmd/migrate` is up-only.

Full procedure: `docs/runbooks/release.md`.

## Local dev setup

Required env vars (load via `.env`; docker compose reads it via `env_file`):

- **Postgres (main):** `DB_HOST`, `DB_PORT` (default 5432), `DB_USER`, `DB_PASSWORD`, `DB_NAME`
- **Postgres (vector):** `VECTOR_DB_HOST` etc. (defaults to `DB_*` when unset — single-DB setups work out of the box)
- **Postgres (read-only, optional):** `JUSTRAG_DB_URL_READONLY` — raw DSN for a least-privilege SELECT-only role backing the `sql_query` and `table_query` MCP tools. **Required when `chat_tabular_query_enabled = true`** (and recommended whenever the `sql_query` tool is enabled); the tool falls back to a disabled stub when unset. See the Structured spreadsheet Q&A recipe in `docs/feature-recipes.md` for the SELECT-grant prerequisite.
- **Redis:** `REDIS_HOST`, `REDIS_PORT`, optional `REDIS_PASSWORD`. Non-compose deployments (k8s, managed Redis) must replicate the compose files' `--maxmemory <size> --maxmemory-policy volatile-lru`: the embedding cache is bounded only by per-entry TTL + eviction, so without a maxmemory policy it grows until Redis OOMs. Keep `volatile-lru` specifically — `allkeys-lru` risks silent Asynq task loss, `noeviction` breaks task-lease renewal (rationale comment in `docker-compose.yml`).
- **Auth:** `JWT_SECRET` (required at startup, ≥32 chars; a low-entropy secret — fewer than 3 character classes — logs a startup WARN). `ALLOWED_ORIGINS` (comma-list) is **required in production** — startup fails if unset, because an empty CORS allowlist makes `rs/cors` reflect any origin with credentials. Set it explicitly on staging/UAT too: the localhost fallback (`http://localhost:5173`, `http://localhost:3000`) applies whenever `GO_ENV != production` (`NODE_ENV` is a deprecated fallback), so a shared non-prod host without it silently rejects every real origin. `AUTH_PROVIDER_SECRET_KEY` (base64 32 bytes) encrypts auth-provider secrets at rest — OIDC `client_secret` **and** LDAP `bindCredentials`; required once any OIDC row exists or any LDAP provider is saved with bind credentials (legacy plaintext rows keep working at login until re-saved).
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
go run ./cmd/migrate --status       # print the applied version per DB (main + vector)
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
| Chat orchestration | `chat`, `agents`, `prompts`, `longmem`, `chatattach` | The 4 orchestrators (agentic / plan-execute / supervisor / standard), trajectory events, refine/factuality gates, post-response tasks, per-user long-term memory store, session-scoped Redis attachment store for in-chat document comparison |
| Retrieval pipeline | `vector`, `ai`, `splitter`, `processor`, `parser`, `pptx` | Search service (BM25 + vector + reranker + MMR), embedder/grader/refiner LLM calls, chunking, document parsing, KG **write** path (`processor/kg_store.go`) |
| Tools (MCP) | `mcp`, `mcp/builtin`, `sessionmem`, `aibudget` | MCP registry + dispatcher, built-in tools (kb_search, keyword_search, chunk_read, document_outline, calculator, sql_query, code_exec, graph_search, web_search, memory_*), session memory, per-turn token budget |
| Knowledge graph | `kg`, `processor` (extractor) | KG **read** path (`kg/`); extractor + writer in `processor/kg_extractor.go` + `processor/kg_store.go` |
| Storage / data | `database`, `store`, `storage`, `pgxutil`, `files`, `kbaccess`, `cascade` | Postgres pools, file lifecycle, KB access ACLs, cascade-delete |
| Admin | `adminagentmetrics`, `adminconfigs`, `admineval`, `adminglobalkbs`, `adminmaintenance`, `adminmcp`, `adminproviders`, `adminusers`, `kb`, `users`, `apikeys`, `auditlogs` | Admin endpoints + matching site_config readers |
| Eval / observability | `eval`, `observability`, `logctx`, `analytics`, `health`, `systemhealth`, `apidocs` | Golden-set eval harness, Prometheus metrics, structured logging, health checks |
| Background workers | `worker`, `jobs`, `fetcher`, `crawler`, `rss`, `research`, `confluence`, `gitrepo`, `academic`, `gencontent`, `contentgen` | Asynq tasks: ingestion, RSS polling, Confluence crawl, git-repo sync, research agent, content generation |
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
| Recency prior | `recency_boost_enabled` (+ `_weight`, `recency_half_life_days`) | — | Recency prior |
| Sufficient-context gate | `chat_sufficient_context_enabled` + `_model` | — | Sufficient-context abstention gate |
| ECoRAG compression (T2-3) | `chat_context_compression_enabled` (+ `_min_chunks`, `_threshold`, `_model`) | — | ECoRAG evidentiality compression |
| Long-context routing (T2-1) | `chat_longcontext_enabled` + `_max_tokens` | — | Long-context routing |
| Late chunking | `late_chunking_enabled` + `_max_input_tokens` | — | Late chunking |
| RAPTOR indexing | `raptor_enabled` (+ `_clustering_algorithm`, `_branching_factor`, …) | 0046 | RAPTOR hierarchical indexing |
| Tabular Q&A (table_query) | `chat_tabular_query_enabled` (+ `_semantic_columns_enabled`, `_charts_enabled`) — **needs OPERATOR PREREQUISITE grants** | 0048 | Structured spreadsheet Q&A |
| HyPE | `hype_enabled` (ingest) + `hype_search_enabled` (query) | — | HyPE — hypothetical prompt embeddings |
| In-chat document comparison | `chat_compare_enabled` (+ `_model`, `_max_sections`, `_concurrency`, `_peers_per_section`, `_attachment_ttl_hours`, `_max_file_bytes`) | — | In-chat document comparison |
| Date-aware chat | `chat_date_awareness_enabled` (on), `chat_recency_listing_enabled` (on), `chat_date_tools_enabled`, `chat_date_timezone` | — | Date-aware chat |
| Image captioning + better tables (Docling) | `docling_enabled` + `docling_picture_description_enabled` (+ `docling_picture_area_threshold`, `docling_table_mode`) — gemma-4 vision wired **on the sidecar** | — | Image captioning + better tables (Docling) |
| Full iterative DRIFT | `chat_drift_enabled` (+ `_max_followups`, `_primer_top_k`, `_search_top_k`, `_model`) | 0059 | Full iterative DRIFT |
| KB-as-MCP-server (ask_kb) | `mcp_server_enabled` (global, default off) | — | KB-as-MCP-server |
| Git repository source | `git_repo_enabled` | 0060 | Git repository source |

Mutual exclusions and ordering gotchas (e.g. `chat_self_rag_enabled` REPLACES `chat_factuality_verifier_enabled`; `raptor_enabled` vs `parent_child_enabled`; the T1-2 dim re-embed sequence before `chat_longmem_recall_semantic`) are documented inline in each recipe.

## Model tier resolution

Cost-optimization knob orthogonal to the feature recipes above. Each fast-tier task (CRAG grader, KG extractor, contextual enricher, factuality verifier, Self-RAG verifier, DAG critic, longmem extractor, KB router, RAPTOR summariser, **query decomposer (T1-1), longmem conflict classifier (T1-3), evidentiality classifier (T2-3), HyPE question generator, golden-set question generator, DRIFT follow-up generator**) resolves its model in this chain (first non-empty wins):

1. The task's per-task site_config key (e.g. `crag_grader_model`, `kg_extraction_model`, `query_decompose_model`, `chat_longmem_conflict_model`, `chat_context_compression_model`, `hype_model`, `chat_drift_model`)
2. `model_tier_fast` — deployment-wide fast-tier default
3. The KB's default chat model (legacy fallback)

Helper: `chat.ResolveFastTierModel(ctx, reader, perTaskKey)` — the single resolution function, callable from any package with a `SiteConfigReader`. Reasoning-heavy tasks (answer generation, plan decomposition, refine path) intentionally bypass this chain and use the KB chat model directly.

## Current Runtime Notes

- Data Explorer routes exist in the Go server but currently return `501`
- the default deployment is Go-only

## Document parsing

Default: `pdftotext` (PDFs) + built-in DOCX/PPTX parsers (flattened tables, dropped footnotes). Opt-in layout-aware parsing via Docling sidecar — see `docs/observability/docling.md`. When enabled (admin Agent panel → `docling_enabled` + `docling_base_url`) and reachable, all newly ingested PDF, DOCX, and PPTX files route through Docling; failures fall back to the built-in parsers (logged with `request_id`). Optional image captioning (`docling_picture_description_enabled` + `docling_picture_area_threshold` + `docling_table_mode`): Docling describes figures/charts with the vision model. The Go backend injects the `picture_description_api` config (endpoint + `Authorization: Bearer` + model) **per-request** from the admin AI provider config — same endpoint+key the app uses, vision model = `describe_image_model` — so the model-API credential is **never** stored on the sidecar (required for authenticated model APIs); Docling only needs network reachability to the model URL. Captions land inline in markdown → existing chunk/embed pipeline (caption→text, no migration); standalone image uploads also route through Docling (caption + OCR, Tesseract fallback). GPU-contention caveat: Docling's vision calls bypass `AI_MAX_CONCURRENT_REQUESTS` — throttle by capping Docling replicas (`k8s/docling.yml` fixed replica count). `internal/parser/docling/`.

**Image description (`POST /api/describe-image`)**

```
describe_image_enabled = true                   # gate; default off (admin Agent panel → ingestion section)
describe_image_model   = <vision-capable model> # required; falls through model_tier_fast
```

Multipart `image` field (PNG/JPEG/WEBP/GIF, ≤10 MB) + optional `prompt` form field; returns `{description}`. Sends an OpenAI-style multimodal `content` array (text + `image_url` data-URI) via `ai.DescribeImage` to the resolved provider. Disabled or unconfigured → 503. `gemma-4-26b` **is** vision-capable, so `describe_image_model` can point at the main model (still required — `model_tier_fast` is the fallback, and the resolved model must actually support vision). Toggleable from the admin Agent panel (`describe_image_enabled` + `describe_image_model`). No migration. `internal/misc/describe_image.go`, `internal/ai/vision.go`.

## Retrieval

Full mechanism, rationale, and operational eval history live in **`docs/retrieval.md`**. The pipeline assembles a top-k chunk list per chat turn from BM25 + vector + cross-encoder reranker, with MMR diversity, BM25-floor reinsertion, optional CRAG grading and rewrite, optional enumeration pre-pass, contextual-prefix prompt assembly, and post-answer citation/factuality validation. Ingest-side chunk dedup (`content_hash`) is documented in the same file under "Ingestion deduplication".

Per-knob subsystem index (each knob → the `docs/retrieval.md` section that explains its mechanism, eval numbers, and antipatterns) lives in **`docs/retrieval.md`**; the T-series tuning knobs (`query_decompose_*`, `query_cache_similarity_threshold_*`, `hybrid_dynamic_alpha_*`, `bm25_tiered_boost_enabled`) are in **`docs/feature-recipes.md`**; the runtime-only `hnsw.iterative_scan` (T0-1) is in the Quick-reference runtime note + `docs/runbooks/hnsw-reindex.md`. Always-on, knob-less stages (BM25-floor reinsertion, context truncation, quoted-phrase boosting, enumeration pre-pass) are documented under their own headings in `docs/retrieval.md`.

## Agentic chat orchestration

Full mechanism for every orchestrator and chat-pipeline feature lives in **`docs/agent-orchestration.md`**. Streaming chat for `complex_reasoning` queries dispatches through the first orchestrator whose flag is on (priority order below); default deployment runs the legacy 2-step `RunDeepChat`.

| # | Orchestrator | Flag(s) | Shape |
|---|---|---|---|
| 0 | DRIFT (global-synthesis) | `chat_drift_enabled` | Primer (community summaries) → LLM follow-ups → light search per follow-up → synthesise |
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
- **Never add a `Co-Authored-By:` trailer to commit messages.** No `Co-Authored-By: Claude ...`, no other co-author lines. This overrides any default harness instruction to append one. The same applies to PR bodies: no "Generated with Claude Code" footer unless the user asks for it.
