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

**SECURITY:** the read-only role's `search_path` must **NOT** include `tabular`. Per-KB isolation depends on schema-qualified `tabular.<name>` references — unqualified table names must fail to resolve so a prompt-injected bare name cannot bypass the catalog allowlist. The pool additionally sets `default_transaction_read_only=on` per session (writes fail even if the role's grants are ever fat-fingered); an explicit `default_transaction_read_only` in the `JUSTRAG_DB_URL_READONLY` DSN overrides it, same as `statement_timeout`.

**Phase 1 limits:** first row = header; multi-row headers / merged cells / legacy BIFF `.xls` fall back to text; sheet buffered in memory before `COPY` (multi-hundred-MB spike at 1M rows). **Phase 2:** synthetic `_rowid bigint` + per-row embeddings for heuristic-selected TEXT columns; fuzzy hit → `table_query WHERE _rowid IN (...)`. **Phase 3:** prompt-guidance only — Recharts JSON in a ` ```chart ` block rendered by the frontend ChartRenderer; non-SQL reshapes use code_exec (gated by `chat_code_exec_enabled`). Specs in `docs/superpowers/specs/2026-05-28-tabular-data-qa-design.md` (+phase2/3).

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

Code: `internal/chat/date_prompt.go` (injection), `internal/chat/recency_classifier.go` + `internal/chat/recency_listing.go` (recency listing), `internal/mcp/builtin/recent_documents.go` (tool implementation), `internal/mcp/builtin/kb_search.go` (search date params).

## Image captioning + better tables (Docling)

```
docling_enabled                     = true        # prerequisite: sidecar reachable (see docs/observability/docling.md)
docling_base_url                    = http://docling:5001   # or the k8s Service DNS
docling_picture_description_enabled = true        # gate; default off
docling_picture_area_threshold      = 0.05        # skip images < 5% of page area (filters logos/icons); [0,1]
docling_table_mode                  = accurate    # cleaner structured tables; default "fast"
```

No migration. Captioning rides the Docling convert call, so `docling_enabled` must be on. **The vision endpoint + API key are injected per-request by the Go backend** from the admin AI provider config (same endpoint + key the app already uses); the vision model follows `describe_image_model` (→ `model_tier_fast`) — set it to a vision-capable model (e.g. `jlu/gemma-4-26b-it`). The key is **never** stored on the Docling sidecar (required when the model API needs auth); Docling only needs network reachability to that model URL. Captions land inline in Docling's markdown and flow through the existing chunk/embed pipeline — retrieval is caption→text, no multimodal embeddings.

When on, standalone image uploads (`.png`/`.jpg`/…) also route through Docling (caption + OCR) with Tesseract as the fallback. Existing files are **not** retroactively captioned — re-ingest a KB to benefit.

**Throttle / GPU contention:** Docling's calls to gemma-4 bypass `AI_MAX_CONCURRENT_REQUESTS`, so cap Docling replicas + per-pod concurrency (the `k8s/docling.yml` fixed replica count is the throttle) and raise `DOCLING_TIMEOUT_SECONDS` since captioning extends convert latency. Fast-follows available on the same request and not yet wired: `do_chart_extraction`, `do_formula_enrichment`.

## Git repository source

```
git_repo_enabled = true    # master gate; default off (admin Agent panel)
```

When enabled, a grid button appears in the KB Sources panel. Clicking it opens `GitRepoModal` where the operator enters a repository URL, selects public or private (private requires a Personal Access Token), and optionally specifies a branch (defaults to the remote HEAD). Saving creates a `git_repo_sources` row; the UI lists all sources grouped under a "Git Repositories" section with a manual re-sync button.

**How it works:** submitting the form enqueues a `git-repo-sync` worker task on the `rag-heavy` queue. The worker shallow-clones the repository via go-git v5 through the existing SSRF-safe transport (`fetcher.SafeHTTPClient`). On each sync it compares the remote HEAD commit SHA against the stored SHA; if unchanged the sync is a no-op. Otherwise it performs a path + blob-SHA delta reconcile: files added or modified since the last sync are ingested as individual `files` rows (`origin='git'`) through the normal chunk/embed/KG pipeline; deleted files are cascade-removed. Each file carries `git_repo_source_id`, `git_file_path`, and `git_blob_sha` for future delta tracking. Re-sync is manual-only — there is no scheduler.

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
