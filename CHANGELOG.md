# Changelog

All notable changes to JustRAG are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project uses
SemVer `0.x`, where a **minor** bump covers features *and* breaking changes
and a **patch** bump is fixes only.

Releases may carry a hand-written **⚠ Upgrade notes** block recording
migrations, changed `site_config` defaults, and re-ingest requirements.
Those are not generated — a release whose notes list a migration has **no
one-step rollback** (`cmd/migrate` is up-only).

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

