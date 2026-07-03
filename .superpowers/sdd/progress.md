# Mind-Map Graph Export — SDD Progress Ledger

Plan: docs/superpowers/plans/2026-07-01-mindmap-graph-export.md
Mode: subagent-driven, working on main (established repo pattern), BUILD-ONLY (no commits; user commits).
BASE_SHA: 8c54fca (pre-feature HEAD; working tree was clean at start)
Feature: Export dropdown on the KG mind map -> PNG / GraphML / JSON / CSV. Frontend only.

Task 1 (graphExport.ts pure serializers + downloadText + tests): PENDING
Task 2 (html-to-image dep + exportPng.ts + test): PENDING
Task 3 (GraphExportPanel.tsx dropdown + translations + css + test): PENDING
Task 4 (wire panel into MindMapView + tests): PENDING

--- EXECUTION LOG ---
Task 1: COMPLETE (build-only, uncommitted, review clean — Approved).
  Files: web/src/components/MindMap/graphExport.ts + graphExport.test.ts. 4 describe blocks green, lint clean.
  Minors (for final review triage, none blocking):
   - csvField regex omits bare \r as a quote trigger (RFC strictness; entity names unlikely to contain CR).
   - test coverage gaps: embedded-quote CSV case, edge rel <data> assertion (code correct, just unasserted).
   - downloadText anchor not appended to body before click() — Firefox edge; matches 8 existing inline patterns in repo.

Task 2: COMPLETE (build-only, uncommitted, review clean — Approved).
  Files: web/src/components/MindMap/exportPng.ts + exportPng.test.ts; html-to-image@^1.11.13 added.
  2 tests green (fit-transform wiring verified, empty no-op). Lint clean (21 pre-existing warnings elsewhere).
  Minors (final-review triage): test doesn't assert opts.pixelRatio===2 nor opts.width/height (impl correct, props just unasserted).

Task 3: COMPLETE (build-only, uncommitted, review Approved + fix wave).
  Files: web/src/components/MindMap/GraphExportPanel.tsx + .test.tsx; translations.ts (+5 keys); index.css (.graph-export-* block).
  Review Approved. Reviewer's "missing React import" = FALSE POSITIVE (confirmed via tsc -b; matches MindMapView pattern; jsx=react-jsx).
  CONTROLLER CAUGHT via `npx tsc -b` (per-task diffs can't see whole-file collisions):
   - CRITICAL: translations.ts already had exportPng/exportCsv (lines 396-397, used elsewhere) -> duplicate-key TS1117.
     FIXED: renamed the 4 menu-format keys to graphFmtPng/Graphml/Json/Csv (graphExport button unchanged); consumers+test updated.
   - MINOR: graphExport.test.ts had 2 unused @ts-expect-error (TS2578). FIXED: removed the 2 directive lines (Task 1 test).
   Verified: tsc -b clean; GraphExportPanel.test + graphExport.test 4/4 each; lint clean.
  Minors (final triage): GraphML download+mime path not unit-tested (code correct).

Task 4: COMPLETE (build-only, uncommitted, review Approved — no Critical/Important).
  Files: web/src/components/MindMap/MindMapView.tsx (+5 lines wiring) + MindMapView.test.tsx (+30, panel stub +2 tests).
  MindMap suite 15/15; tsc -b clean; lint clean (21 pre-existing warnings); npm run build succeeded.
  Minors (final triage): test doesn't assert data-scoped="false"; messageId=set(scoped=true) branch untested.

ALL TASKS COMPLETE — proceeding to final whole-branch review (opus).

FINAL WHOLE-BRANCH REVIEW (opus): "Needs fixes: 0 Critical, 2 Important". Integration verified end-to-end.
  FIX WAVE (sonnet, all verified present in source by controller):
   - IMPORTANT: CSV formula injection (CWE-1236) in csvField -> leading =+-@\t\r prefixed with ' (+ folds \r). +test (leading-= neutralized).
   - IMPORTANT: PNG export floated promise / silent fail -> useToast + try/catch -> toast.error(t('exportFailed')) [reused existing key]. test mocks ToastContext.
   - MINOR: GraphML strips XML-illegal C0 control chars (eslint-disable no-control-regex). MINOR: downloadText revoke deferred via setTimeout(,0). MINOR: exportPng test asserts pixelRatio/width/height.
  Verified: tsc -b clean; vitest MindMap 16/16; lint clean (21 pre-existing warnings); npm run build OK.

=== FEATURE COMPLETE (build-only, uncommitted; user commits) ===
NEW: web/src/components/MindMap/{graphExport.ts,graphExport.test.ts,exportPng.ts,exportPng.test.ts,GraphExportPanel.tsx,GraphExportPanel.test.tsx}
MOD: web/package.json (+html-to-image ^1.11.13), web/package-lock.json, web/src/translations.ts (+graphExport/graphFmt* keys),
     web/src/index.css (.graph-export-* block), web/src/components/MindMap/{MindMapView.tsx,MindMapView.test.tsx}
Docs: docs/superpowers/specs/2026-07-01-mindmap-graph-export-design.md, docs/superpowers/plans/2026-07-01-mindmap-graph-export.md
REMAINING: manual browser smoke (subagents can't) — verify Export menu appears, 4 downloads work, PNG = whole graph, scoped filename suffix.

MANUAL SMOKE VERIFICATION (2026-07-01, verify skill): PASS.
  Method: real MindMapView rendered in Chromium via Vite + throwaway harness (removed after) + a local graph stub (:3001); Playwright drove it; in-page anchor shim captured exact export bytes.
  - Whole-KB: 6 nodes/6 edges rendered; "Exportieren ▾" present; menu = PNG-Bild/GraphML/JSON/CSV.
  - JSON: correct node-link {nodes,links}. CSV: header+6 rows, "Alice, Inc." double-quoted. GraphML: valid, edgedefault=directed, LIVE XML-escaped "R&amp;D &lt;core&gt;". PNG: real data:image/png (342KB).
  - Scoped (?messageId): filename -> knowledge-graph-scoped.csv. Confirmed.
  NEW FINDING (Minor, verification-only — unit tests mock xyflow so couldn't catch): PNG export logs React Flow warning
    "use getNodesBounds from useReactFlow hook to ensure correct values for sub flows". exportPng.ts uses the STANDALONE
    getNodesBounds import; recommended is the useReactFlow() instance method (GraphExportPanel already has the hook). PNG output
    is correct for flat graphs (no sub-flows); fix = destructure getNodesBounds from useReactFlow() and pass in / compute in panel.
  PROBE GAP: CSV formula-injection guard NOT reached live (the =SUM node was isolated -> absent from edge-list); guard is unit-tested + code-confirmed present.

PNG "not working" BUG — ROOT CAUSE CONFIRMED (systematic-debugging, 2026-07-01):
  Symptom: real app -> "Export failed" toast on PNG; JSON/CSV/GraphML fine. Harness PNG worked -> env-specific.
  ROOT CAUSE: backend CSP `DefaultCSP` (go-backend/internal/middleware/security.go:23) = "default-src 'self'; ..." has NO img-src,
    so img-src inherits default-src 'self' -> forbids the data: URL <img> html-to-image loads to rasterize -> toPng rejects.
  CONTROLLED EXPERIMENT (Chromium via vite + meta-CSP pages, isolating one directive):
    - no CSP: toPng ok (207KB). - img-src 'self': toPng REJECTS ("[object Event]", CSP violation) = reproduces bug.
    - img-src 'self' data: blob: : toPng ok (207KB). => img-src data:/blob: is the fix.
    - skipFonts:true gave BYTE-IDENTICAL png (web font unused in graph labels); cross-origin gstatic sheet already tolerated
      (readable:false yet toPng succeeded) -> connect-src 'self' blocking the font fetch is NOT an extra blocker.
  PROPOSED FIX (pending user approval — it's a security-policy change, backend):
    PRIMARY: security.go DefaultCSP -> add "img-src 'self' data: blob:". NECESSARY; nothing else makes PNG work.
    OPTIONAL hardening (frontend): exportPng.ts toPng({skipFonts:true}) — output-equivalent, kills font-fetch noise under strict CSP.
  NOTE: config.go CSPHeader env can override DefaultCSP; operators with a custom CSP must also allow img-src data:.

PNG FIX APPLIED + VERIFIED (2026-07-01):
  BACKEND: security.go DefaultCSP now includes "img-src 'self' data: blob:". +security_test.go (2 tests: constant + emitted header). go test/build/vet green. RED->GREEN confirmed.
  FRONTEND: exportPng.ts toPng({... skipFonts:true}) (defense-in-depth for connect-src'self' font fetch; output byte-identical). +test asserts opts.skipFonts===true. exportPng.test 2/2, tsc -b clean, lint clean.
  E2E VERIFY (Chromium, meta CSP img-src 'self' data: blob:, real UI): clicked real PNG-Bild -> knowledge-graph.png (342KB data:image/png), NO "Export failed" toast. Bug resolved.
  Files changed by fix: go-backend/internal/middleware/{security.go, security_test.go (NEW)}, web/src/components/MindMap/{exportPng.ts, exportPng.test.ts}.

PNG DEACTIVATED (2026-07-01, user request — exported image was illegible/mis-scaled):
  GraphExportPanel.tsx: removed 'png' from Fmt, the PNG menu item, the png branch, exportGraphPng + useReactFlow imports; onSelect no longer async.
  Test: removed PNG-delegation test; added GraphML-download test + "does not offer PNG" regression guard. MindMap suite 17/17, tsc/lint/build clean.
  RETAINED for later re-enable: exportPng.ts (+test, still green), html-to-image dep, and the security.go CSP img-src data: blob: change (harmless; needed when PNG returns). Known open issue for PNG: illegible capture — suspect standalone getNodesBounds bounds (React Flow warned to use the useReactFlow() hook version) → fix bounds before re-enabling.
Export bundle now ships: GraphML + JSON + CSV (all verified working live earlier).

=== PHASE 2: "Make the Graph a Tool" ===
Plan: docs/superpowers/plans/2026-07-01-mindmap-graph-tool-phase2.md
Mode: subagent-driven, main, BUILD-ONLY (no commits; user commits). Phase-1 changes already uncommitted in tree.
Baseline snapshot for per-task diffs: scratchpad/phase2-baseline (refreshed after each passing task).
Task 1 (kg.EntityDetail store method + integration test): PENDING
Task 2 (kggraph GetEntity handler + route + unit tests): PENDING
Task 3 (useGraphInteractions hook + tests): PENDING
Task 4 (GraphToolbar search+filter + i18n/css + test): PENDING
Task 5 (EntityCard + i18n/css + test): PENDING
Task 6 (wire MindMapView + ChatView; delete NodeSourcesPanel; full verify): PENDING

Task 1: COMPLETE (build-only, uncommitted, review Approved). kg.EntityDetail + Neighbor + ErrEntityNotFound in query.go; entity_detail_integration_test.go (skipped, no DB). build/vet clean. IDOR guard verified.
  Minors (final triage): sources-loop computes key before empty-FileID guard (unreachable; readability); integration test doesn't assert len(Sources)>0.

Task 2: COMPLETE (build-only, uncommitted, review Approved). kggraph GetEntity handler + widened Store iface + route (routes.go:666). TestGetEntity 3/3, build/vet clean. No Critical/Important.
  Minors (final triage): OK test doesn't assert Type/Aliases/Degree; BadID doesn't cover id<=0 path; error tests don't assert body message.

Task 3: COMPLETE (build-only, uncommitted, review Approved after fix wave). useGraphInteractions hook.
  Review found 2 IMPORTANT (both fixed, controller-verified in source): (1) useMemo-as-side-effect state reset -> replaced with lazy useState(()=>new Set(allTypes)) + render-time prevKey!==activeKey guard; (2) edge hover-dim too strict -> litEdge now `!hoveredId || (lit(source)&&lit(target))` (&& chosen over spec's || so edges to dimmed non-neighbors also dim — cleaner). +edge-opacity test. vitest 5/5, tsc/lint clean. eslint-disable removed.
  Minors (final triage): selectedId/setSelected untested (trivial useState).

Task 4: COMPLETE (build-only, uncommitted, review Pass). GraphToolbar (search+type chips) + 2 i18n keys + .graph-toolbar css. 2 tests, tsc/lint clean. No Critical/Important.
  Minors (final triage): React.FC w/o React import (consistent w/ existing MindMap files); trim path not asserted in test.

Task 5: COMPLETE (build-only, uncommitted, review Pass). EntityCard (fetch detail + neighbors + clickable sources + Ask; error-path keeps Ask) + 3 i18n keys + .entity-card css. 3 tests, tsc/lint clean. No Critical/Important.
  Minors (final triage): React.FC w/o React import (consistent); .entity-card max-height:70% may be no-op without sized ancestor.

Task 6: COMPLETE (build-only, uncommitted, review Pass after fix wave). MindMapView+ChatView integration; NodeSourcesPanel deleted.
  Review found 2 IMPORTANT (both fixed, controller-verified): (I1) click→EntityCard integration test only checked toolbar -> mock ReactFlow now renders rf-fire-node-click button; test fires click + asserts entity-card data-entity='Alice' (also fixed a mock useNodesState instability causing double-fetch); (I2) Esc-to-deselect missing -> useEffect on selectedId added. Minors folded: decorated memoized; onPaneClick useCallback.
  MindMap suite 27/27, tsc/lint/build clean.

=== PHASE 2 ALL TASKS COMPLETE — proceeding to final whole-branch review (opus) ===

FINAL WHOLE-BRANCH REVIEW (opus): "Ready to merge" — 0 Critical, 0 Important.
  Verified: Go↔TS JSON contract exact match (fileId/fileName/chunkId, neighbor id/name/type/rel), IDOR guard solid (all 4 queries kb-scoped, params bound, no injection), Ask→handleFollowUpClick→real cited answer, source→handlePreviewSource compatible, ChatView sole MindMapView caller passes onOpenSource.
  New Minors (non-blocking, for user to decide): (1) useGraphInteractions returns fresh object each render → Esc listener/onNode* callbacks re-bind (perf churn, decorated still memoized so no loop); (2) EntityCard passes raw s.fileName (''→preview-not-supported toast if file deleted; graceful); (3) in-graph search targets unfiltered rawGraph so can fitView to a type-filtered-hidden node (UX papercut).

=== PHASE 2 COMPLETE (build-only, uncommitted). Consolidated verify: go build+kg/kggraph tests green; MindMap vitest 27/27; tsc -b clean. ===
NEW files: web/src/components/MindMap/{useGraphInteractions,GraphToolbar,EntityCard}.{ts,tsx}+tests; go-backend/internal/kg/entity_detail_integration_test.go.
MOD: go-backend/internal/kg/query.go, kggraph/handler.go+test, app/routes.go; web MindMapView.tsx+test, ChatView.tsx, translations.ts, index.css.
DEL: web/src/components/MindMap/NodeSourcesPanel.tsx+test.

=== FEATURE: Date-aware chat (2026-07-01) ===
Plan: docs/superpowers/plans/2026-07-01-date-aware-chat.md
Spec: docs/superpowers/specs/2026-07-01-date-aware-chat-design.md
Mode: subagent-driven, main, BUILD-ONLY (no commits by subagents/controller; user commits).
Baseline tree for per-task diffs: scratchpad/date-baseline-tree.txt (helper: scratchpad/date-pkg.sh; advance after each passing task).
NOTE: tree already has unrelated uncommitted changes incl. http_send.go (parent-id work) — Task 3 also edits http_send.go; per-task tree-diffs isolate only date changes.
Tasks: 1 config readers | 2 prompt helpers | 3 thread date line (6 orchestrators+http_send+eval) | 4 date-window filter (split-DB) | 5 recent_documents tool | 6 kb_search date params | 7 docs
Task 1: COMPLETE (build-only, review clean — Approved). ChatDate{Awareness,Timezone,Tools}Enabled+MaxResults in siteconfig.go:1430+; reused existing fakeSiteConfigReader/strPtr. test PASS, build clean. No issues.
Task 2: COMPLETE (build-only, review clean — Approved). prompts.CurrentDateLine + ChatSystemPromptWithDate + deWeekdays (prompts.go:489+), "time" import. 2/2 tests, build clean. No issues.
Task 3: COMPLETE (build-only, review Approved). date_prompt.go SystemPromptDateLine + 6 params structs+literals + 6 call-site swaps + eval "". DEVIATION (sound): dateLine threaded into tryDeepChat (5/6 literals live there, not SendMessage) — computed once. 119-line http_send churn = gofmt colon re-align (cosmetic). build/vet/tests green.
Task 4: COMPLETE (build-only, review Approved). SearchOptions.CreatedAfter/Before + fileIDsInDateRange + intersectFileIDs + effectiveDateExpr (recency_boost.go); Search() resolution block (search.go:531-544) before cache lookup, empty short-circuit. FIX (controller, 1-line): Warn log on nil-mainDB fail-open (Important finding). build/vet/test green.
Task 5: COMPLETE (build-only, review Approved + controller enhancement). recent_documents.go (tool+RecentDocsStore+PgxRecentDocsStore) + test (6 cases) + routes.go registration (gated ChatDateToolsEnabled). ENHANCEMENT (controller): wired chat_date_tools_max_results via maxResults func(ctx)int (was dead knob) + test. build/vet/tests green.
Task 6: COMPLETE (build-only, review Approved). kb_search.go date_from/date_to (ungated, optional) → SearchOptions.CreatedAfter/Before; +kb_search_test.go capturing fake (date-set + nil cases). Schema JSON valid. build/vet/tests green. MINOR (deferred): date-parse dup with recent_documents (2 sites, diff semantics — YAGNI).
Task 7: COMPLETE (docs). CLAUDE.md flag bullet + feature-recipes table row + docs/feature-recipes.md section. CONTROLLER FIX: agent hallucinated per-user TZ lookup chain + wrong recent_documents params (after/before RFC3339 + 7-day default) — rewritten accurate (date_from required ISO, date_to optional, global TZ only).

Task 7: COMPLETE (see above).
=== DATE-AWARE CHAT FEATURE COMPLETE (build-only, uncommitted; user commits) ===
Final whole-branch review (opus): READY TO MERGE — 0 Critical, 0 Important, 3 Minor (all deferred).
Consolidated verify: go build ./cmd/server ./cmd/worker OK; go test chat/prompts/vector/mcp-builtin/eval all green; go vet clean.
NEW files: internal/chat/date_prompt.go(+test), internal/prompts/prompts_date_test.go, internal/vector/date_filter_test.go, internal/mcp/builtin/recent_documents.go(+test).
MOD: internal/chat/{siteconfig.go,siteconfig_readers_test.go,http_send.go,agentic_chat.go,plan_execute_chat.go,deep_chat.go,supervisor_chat.go,drift_chat.go,service.go}, internal/prompts/prompts.go, internal/vector/{search.go,recency_boost.go}, internal/mcp/builtin/kb_search.go(+test), internal/app/routes.go, internal/eval/answer.go, CLAUDE.md, docs/feature-recipes.md.
DEFERRED MINORS: M1 tool dates parsed UTC-midnight vs Berlin-resolved "today" → ~2h day-boundary skew on timestamptz (fix: ParseInLocation, needs TZ threaded into the 2 tool handlers); M2 date-parse dup across kb_search+recent_documents (extract helper if 3rd site); M3 recent_documents default before=time.Now() server-local (harmless).
REMAINING: user commit; live smoke (enable chat_answer_tools + chat_date_tools, ask "was wurde heute hinzugefügt?").

POST-FEATURE: Admin Agent-panel controls added (user reported "nothing in admin ui").
  web/src/components/admin/AdminAgentTab.tsx (+SECTION_CONFIGS dateAware entry + <Section> w/ 4 controls) + web/src/translations.ts (DE/EN labels+Help).
  Default-ON chat_date_awareness_enabled uses checked={v!=='false'&&v!=='0'} (mirrors chat_answer_history_enabled). tsc -b + lint + build clean. Build-only/uncommitted.

=== FEATURE: User-created Agents & Agent Teams (2026-07-02) ===
Plan: docs/superpowers/plans/2026-07-02-agent-teams.md
Spec: docs/superpowers/specs/2026-07-02-agent-teams-design.md
Mode: subagent-driven, main, BUILD-ONLY (no commits by subagents/controller; user commits). Established repo pattern.
Baseline for per-task diffs: scratchpad/sdd-baseline (helper scratchpad/sdd-pkg.sh: snapshot|diff N). Refresh after each passing task.
Working tree at start: only M docker-compose.local.yml (pre-existing, DO NOT TOUCH). compose db started for migration verify.
Tasks: 1 migration 0061 | 2 siteconfig per-agent | 3 agentteams types+store | 4 handlers+tests | 5 privileged tools+restricted dispatcher | 6 routes | 7 prompts+MergeChunksRRF | 8 router | 9 specialist | 10 RunTeamChat | 11 dispatch integration | 12 FE api+i18n | 13 My Agents view | 14 KB attach UI | 15 chat picker | 16 e2e verify
Task 1: COMPLETE (build-only, review Approved). migrations/main/0061_agent_teams.sql applied to live dev DB (version 61), schema verified via psql. Idempotent + Down verified.
  Minors (final triage, both plan-mandated/intentional): no partial-unique on is_default (single-default-per-KB enforced in API tx by design); agent_decisions.team_id/agent_id have no FK (fire-and-forget telemetry, intentional).
Task 2: COMPLETE (build-only, review Approved). siteconfig: IsPerAgent/AgentFields/ValidateAgentConfig (registry.go) + member-func generalization of KBOverlayReader + NewAgentOverlay (overlay.go); agent_test.go 4 tests. 30/30 package tests green, gofmt/vet clean. Security invariant (jwt_secret never from agent overrides) directly tested.
  Minor (final triage): no batch-path (GetSiteConfigValues) test for the AGENT overlay specifically (same shared code covered via KB overlay test).
  NOTE: PARALLEL SESSION detected mutating go-backend/internal/ai/ (embedding-dimensions work, mid-TDD). Our diffs are now path-scoped per task (sdd-pkg.sh diff N <paths>); subagents instructed to keep off internal/ai; repo-wide gates (go build ./...) may fail for reasons not ours — scope verification to our packages.
Task 3: COMPLETE (build-only, review Approved). internal/agentteams/{types.go,store_pg.go}: 18 store methods, owner-scoped SQL, cross-table default clearing in tx, a.-qualified JOIN correction applied. build/vet clean; psql LIMIT-0 sanity vs live schema OK. (Parallel session = dims plumbing, mig 0062 — no conflict with our 0061.)
  Minor (final triage): Config JSONB scanned via pgx implicit JSON→map (chat store precedent uses RawMessage+explicit decode); verify round-trip in Task 16 smoke.
Task 4: COMPLETE (build-only, review Approved after fix wave). internal/agentteams/handler.go (506 lines, 16 handlers) + handler_test.go. Fix wave (both verified by re-review): attach ownership checks now errors.Is→404 / else logged 500; rejection tests assert store untouched. 4 tests + suite green, gofmt/vet clean.
  Minors (final triage): TestCreateAgentValidatesConfigAndModel lacks store-untouched assert; attachRequest decode errors swallowed (falls back isDefault=false); HandlerDeps callbacks not nil-checked (Task 6 wires both); owner-scoping coverage narrow (only GetAgent foreign-404 tested).
Task 5: COMPLETE (build-only, review Approved). mcp.PrivilegedTools + AgentsAllowPrivilegedTools/AgentTeamRouterModel readers (append-only) + chat.RestrictedDispatcher(+test): dispatch-time allowlist check BEFORE inner (security control verified), construction-time privileged stripping. 2 tests + full chat suite green.
  Minor (final triage): AnswerToolCatalog nil-vs-empty-slice inconsistency vs MCPDispatcher.
Task 6: COMPLETE (build-only, review Approved). routes.go wiring: agentTeamsStore on routeCtx (setupRoutes), handler constructed in registerChatRoutes (where mcpRegistry lives — sound deviation), registerAgentTeamRoutes with correct auth chains (Authenticate CRUD / kbViewChain list / kbEditChain attach). AvailableTools closure lazy per-request, reader = rc.chatStore (same as WithSiteConfigReader). Init-order risk traced safe (register order: chat → agentteams). build/vet/gofmt/app tests clean.
  Minor (final triage): no startup nil-guard on rc.agentTeamsHandler in registerAgentTeamRoutes (ordering enforced by convention only).
Task 7: COMPLETE (build-only, review Approved). prompts/team.go (5 functions, DE/EN, persona containment block verified) + agents.MergeChunksRRF exported wrapper (pure additive). build/vet/gofmt/tests clean. No issues.
Task 8: COMPLETE (build-only, review Approved). chat/team_router.go(+test): routeTeam with enum-constrained strict schema, cap/dedup/unknown-id-drop traced correct, empty selection valid, errors propagate. 3 tests + full chat suite green.
  Minors (final triage): no test for unknown-id-under-binding-cap or duplicate-id cases (brief-inherited gap); teamRouterSpec nil-on-marshal-failure degrades silently to schema-less call (unreachable in practice).
Task 9: COMPLETE (build-only, review Approved). chat/team_specialist.go(+test): TeamParams (Task-10 contract) + TeamFinding + runTeamSpecialist. Empty-retrieval no-LLM path, spotlighted persona, tool branch (restricted catalog, fail-soft fallthrough verified), AnswerToolsParams fields verified vs answer_tools.go:461. 3 tests + full chat suite green.
  Minor (final triage): valid-but-empty {"analysis":""} response falls back to raw JSON blob as finding text.
Task 10: COMPLETE (build-only, review Approved — opus). chat/team_chat.go(+test): RunTeamChat + runTeamChatTestable + ErrTeamNoRoute. Opus review verified: concurrency correct (per-index writes, single-goroutine emit), no partial-finding leak, all 4 sentinel semantics Is-able/distinct, FinalChunks==Sources==Context source, prompt order per spec. 4 tests + full suite + race clean. StreamEvent.Content verified real.
  Minors (final triage): hop Step numbers non-contiguous when a middle specialist fails; empty-merged (tool-only) edge deserves a comment.
Task 11: COMPLETE (build-only, review Approved — opus). Dispatch integration: TeamLoader+WithTeamLoader (http.go), TeamID/AgentID request fields + resolveTeamSelection/persistChatSelection + gate wiring + team switch arm + mode="team" (http_send.go), ChatRow/chatDBRow + 4 query column lists + UpdateChatAgentSelection (store_pg.go, types.go), WithTeamLoader wired (routes.go), no-op stubs (publicapi adapter + 2 chat test fakes). Opus verified: gate byte-equivalence for non-team turns, nil-safety, fail-soft end-to-end (SSE commit after err check), agent→KB→global overlay layering via forKB receiver, struct-conversion lock-step. build/tests/race/vet clean.
  Minors (final triage): persistChatSelection = 1 extra UPDATE per turn on the hot path (brief-mandated); transform-follow-up early-return skips persist (benign); resolveTeamSelection loads even when streamMode false. Pre-existing gofmt issue in recency_classifier.go noted (NOT ours — parallel session file).
BACKEND (Tasks 1-11) COMPLETE.
Task 12: COMPLETE (build-only, review Approved). web agents/api.ts (12 endpoints verified vs routes.go byte-for-byte; types verified vs Go json tags), ChatEntry.teamId/agentId (correct adaptation — no `interface Chat` exists), 37 translation keys (parity green, no dups). tsc -b/lint/parity clean.
  Minor (final triage): agentTools uses "Werkzeuge" — house style keeps "Tools" as loanword (2 strings to adjust).
Task 13: COMPLETE (build-only, review Approved after fix wave). My Agents view: AgentConfigFields/AgentForm/TeamForm/AgentsView(+test), ViewType 'agents', AuthenticatedApp branch (before home), HomeView nav button (home-view__icon-button + Bot). Sanctioned adaptations: SafeAIConfig.chat_models flat list; vi.mock(ThemeContext) test pattern. Fix wave (verified): bool/enum selects now t('agentConfigInherit'/'agentConfigOn'/'agentConfigOff') + 3 keys. 249 tests green, tsc -b/lint/parity clean.
  Minors (final triage): bare .click() in Teams-tab test; potential duplicate <option key> if two configs expose same model name; window.confirm instead of ModalContext confirm (v2 polish).
Task 14: COMPLETE (build-only, review Approved after fix wave). KbAgentsSection.tsx + SettingsModal mount (line 73). Self-hide on owner assets, shared default-radio across kinds. Fix wave (verified): useToast + try/catch on both mutation sites, toast.error(t('settingsUpdateError')) reused, reload only on success. tsc/tests/lint clean.
  Minors (final triage): reload() GETs swallow errors (section vanishes on transient blip); attached-but-not-owned items not rendered (scope decision); toast is sole failure signal for radio.
Task 15: COMPLETE (build-only, review Approved after fix wave — opus). Chat picker + sticky selection: useKbAgents hook, AgentSelection threaded useKbSettings→useChat→useChatStream (dep-array live read, no stale closure) + KbChatContext, restore-on-chat-switch, new-chat default effect (team priority), picker after RAG-Mode select, chip w/ Users/Bot. Sanctioned addition: setAgentSelection({}) in handleNewChat. Fix wave (verified): useKbAgents clears options on every kbId change — cross-KB default-leak closed (ChatView remount makes residual window moot). tsc/tests(249)/lint/build clean.
  Minors (final triage): empty-name chip if selection detached meanwhile; send before default-effect commits goes Standard (accepted); 1 new instance of pre-existing set-state-in-effect lint-warning class.
ALL IMPLEMENTATION TASKS (1-15) COMPLETE — Task 16 verification + final whole-branch review next.
Task 16: COMPLETE (verification). GATES: go test ./... FULL SUITE green; race clean on chat/agents/agentteams/siteconfig; gofmt clean on our files (fetcher/kggraph/recency_classifier flags are NOT ours); tsc -b clean; 249 FE tests; lint 0 errors; npm run build OK.
  LIVE API SMOKE (real server on :3111 vs compose db/redis, minted superadmin JWT): agent CRUD 201s w/ JSONB config round-trip verified (Task-3 concern resolved); 3 negative gates fire (privileged tool 400, reingest key 400, unknown model 400); team create + foreign-member 400; registry endpoint 30 fields 0 reingest leaks; attach/default + CROSS-KIND single-default flip verified via picker payload; chat POST with teamId → will_run_team=true on enumeration-classified query (explicit selection overrides classification ✓), sticky chats.team_id persisted ✓, clean fail-soft error when LLM/embedding backend unreachable (expected locally — full chat smoke needs prod backend). Smoke data cleaned up (0 rows).
  NOTE: user committed the BACKEND (Tasks 1-11) inside 45a737b (bundled with parallel dims work). FRONTEND (Tasks 12-15) still uncommitted. docs/ is gitignored by design.
  REMAINING for user: commit frontend; live chat smoke against a reachable LLM backend (create team → chat → verify router trajectory + synthesis + citations).
FINAL WHOLE-BRANCH REVIEW (fable): "Ready to merge — Yes". 0 Critical, 3 Important (all judged safe-as-follow-up), 2 fix-before-commit trivials.
FINAL FIX WAVE (sonnet, all 4 controller-verified in source):
  - IMPORTANT #1 FIXED: answer-time tools disabled on team turns (useAnswerTools := !willRunTeam && ...) — synthesis prompt carries persona-influenced findings; per-team catalog restriction = follow-up.
  - IMPORTANT #2 FIXED: resolveTeamSelection returns degradation reason → team_unavailable trajectory decision event; persistChatSelection now persists RESOLVED selection (NULL/NULL on failure — stale sticky ids self-heal). Deviation (sound): resolution runs unconditionally so Enhance turns don't wipe persistence; dispatch still gates on Enhance=="".
  - TRIVIAL: agentTools de strings → "Tools" loanword; availableModels deduped via Set.
  IMPORTANT #3 DEFERRED (follow-up): serve agent-selectable tool names from backend (FE hardcodes list; table_query/memory_* unreachable; privileged flag no UI effect).
  Verify after fixes: go build/test/vet clean; tsc -b; 249 FE tests; lint 0 errors.
CLAUDE.md: feature bullet added to site_config quick reference.

=== AGENT TEAMS FEATURE COMPLETE (build-only; BACKEND already committed by user in 45a737b, FRONTEND uncommitted; user commits) ===
NEW BE: migrations/main/0061_agent_teams.sql; internal/agentteams/{types,store_pg,handler}(+test); internal/chat/{restricted_dispatcher,team_router,team_specialist,team_chat}(+tests); internal/prompts/team.go; internal/siteconfig/agent_test.go
MOD BE: internal/siteconfig/{registry,overlay}.go; internal/mcp/types.go; internal/chat/{siteconfig,http,http_send,types,store_pg}.go(+2 test fakes); internal/agents/supervisor.go; internal/app/routes.go; internal/publicapi/handler.go
NEW FE: web/src/components/agents/{api.ts,AgentConfigFields,AgentForm,TeamForm,AgentsView(+test),KbAgentsSection}.tsx; web/src/hooks/useKbAgents.ts
MOD FE: hooks/{useViewState,useKbSettings,useChat,useChatStream}.ts; contexts/KbChatContext.tsx; AuthenticatedApp.tsx; components/{HomeView,SettingsModal,ChatView}.tsx; types.ts; translations.ts (+40 keys)
REMAINING: user commits frontend; LIVE TEAM-ANSWER SMOKE vs reachable LLM backend (router→specialists→synthesis has never streamed a real answer); follow-ups: backend-served tool catalog, corpus-table gate reorder, minors table above.
