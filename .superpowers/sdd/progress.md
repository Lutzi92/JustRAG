# Git Repo Source — SDD Progress Ledger

Plan: docs/superpowers/plans/2026-06-29-git-repo-source.md
Mode: subagent-driven, working on main, STAGE-ONLY (no commits), user commits.
Base (main HEAD at start): see BASE_SHA below.

BASE_SHA faab4c1e4fa438b597cfebdd5669ef6c13bcf6fa

Task 1: complete (migration 0060 applied to main DB; review clean, Approved).
  - Minor (deferred to final triage): created_at/last_synced_at use TIMESTAMP not TIMESTAMPTZ; matches files/confluence_sources sibling convention, diverges from 0059. Defensible.
  - Resolved ⚠️: vector:60 is the vector DB's independent goose counter; migration file lives only in migrations/main/.
  - NOT committed (harness blocks git add); file: go-backend/migrations/main/0060_git_repo_sources.sql

Task 2: complete (internal/gitrepo/store_pg.go + store_pg_test.go; review Approved, fix applied: added mapper test (3 passing), UpdateGitRepoSource comment).
  - Minor (kept, matches confluence sibling): GetGitRepoSourceFileProgress counts 'error' status as done.
  - NOT committed (harness blocks git).

Task 3: complete (internal/gitrepo/filter.go + filter_test.go; review Approved, fix applied: 20 subtests pass incl segment-boundary + mime assertions, comment corrected).
  - Minor open (not fixed): strings.Split could be SplitSeq (Go1.24 perf hint). Defer to final triage.
  - NOT committed.

Task 4: IN REVIEW. Security review flagged 2 MEDIUM (clone.go): (1) file:// allowed in prod -> gate to tests only + reject userinfo; (2) fail-open when Transport nil -> require safe transport for https clones. To fold into Task 4 fix.

Task 4: complete (internal/gitrepo/clone.go + clone_test.go + go.mod/go.sum go-git v5.12.0). Review Approved; security fixes S1 (file:// test-only + userinfo reject) + S2 (fail-closed SSRF transport) applied & re-reviewed clean. 29 tests pass.
  - Minor open (final triage): safeTransportInstalled bool could be atomic.Bool for go test -race; benign in startup-install model.
  - go-git v5.12.0 added (report's v5.19.1 was inaccurate). NOT committed.

Task 5: complete (internal/gitrepo/sync.go + sync_test.go; jobs.go + payloads.go got TypeGitRepoSync/timeout/GitRepoSyncPayload). Review Approved; fix applied: sanitize token in error log (Important security) + created++ success-only. 9 tests pass.
  - Minor open (final triage): intPtr could be new(int).
  - Task 6 now ONLY needs worker.go registration + InstallSafeGitTransport (jobs plumbing done here). NOT committed.

Task 6: complete (worker.go registers TypeGitRepoSync handler + InstallSafeGitTransport via fetcher.SafeHTTPClient(0).Transport). Review Approved, no issues. Builds clean (server+worker+./...). Backend clone->sync->ingest fully wired. NOT committed.

Task 7: IN REVIEW. Security review flagged 2 HIGH IDOR (http.go): UpdateSource + DeleteSource mutate by sourceId without verifying source.KbID == path kbID (TriggerSync does check). To fold into Task 7 fix: add ownership check mirroring TriggerSync before update/delete.

Task 7: complete (internal/gitrepo/http.go + http_test.go). Review found Critical IDOR (matched security review HIGH x2); fix applied + re-reviewed clean: UpdateSource/DeleteSource now enforce KbID ownership (404), empty-host URL rejected, marshal log, 9 tests incl cross-KB 404. Token never echoed (hasToken bool only). NOT committed.

Task 8: complete (routes.go: 5 git-repo routes, list=kbViewChain, rest=kbEditChain). Review Approved. BACKEND COMPLETE (Tasks 1-8). go build ./... clean. NOT committed.
--- Frontend phase (Tasks 9-14): tsc/build will show cross-task exhaustiveness errors after Task 12 until Task 14 wires everything. Scope per-task verification. ---

Task 10: complete (web/src/hooks/useGitRepoSources.ts; axios+contexts, exact 7-name contract, poll-while-syncing). Review Approved, only pattern-parity Minors. NOT committed.

Task 11: complete (web/src/components/sidebar/GitRepoModal.tsx; named export {GitRepoModal}, useTheme, public/private toggle + url + conditional password token + optional branch). Review Approved.
  - Minor open (batch UI polish): M1 Enter-key submit on URL input (RssModal has it); M2 autofocus token field when private toggled. + Task10 minors.
NOTE for Task 14: import { GitRepoModal } (named, like RssModal). NOT committed.

Task 12: complete (SourcesGrid.tsx: GitBranch import, 'gitrepo' union, gridItem, git_repo_enabled gate default-off). Review Approved. tsc clean. NOT committed.

CRITICAL TOOLING FINDING (Task 13): `npx tsc --noEmit` from web/ is a NO-OP (tsconfig.json is solution-style: files:[]+references). ALL frontend "tsc --noEmit clean" reports were FALSE SIGNALS. Real typecheck = `npx tsc -b` (what `npm run build` runs).
Baseline `tsc -b` is ALREADY RED from pre-existing WIP: web/src/MessageBubble.tsx (~13 errors: VerificationBadge undefined, sourceElements undefined, unused vars) — MODIFIED vs HEAD, NOT my feature. So build was broken before this feature.
My feature's real type errors after Task 13: SidebarLeft.tsx:200 (missing 4 props -> Task 14 fixes) + SourcesSection.test.tsx x4 (existing test broke on new required props -> fixing now as Task 13 completion).
GOING FORWARD: verify frontend with `npx tsc -b` and FILTER to my files (ignore pre-existing MessageBubble baseline errors).

Task 13: complete (SourcesSection.tsx git-repo group + test baseProps). Review found Important double-confirm (fixed: delete delegates to hook showConfirm) + Minor pause-during-sync (fixed: disabled when syncing). tsc -b clean for SourcesSection, vitest 4/4.
  ENTANGLEMENT NOTE: Task 13 diff also contains a PRE-EXISTING "Add Source CTA" feature (onAddSource prop + button + addSourceCta key) that is NOT part of git-repo — it pre-existed in SidebarLeft/translations WIP; implementer wired the SourcesSection side to keep types consistent. KEPT (removing re-breaks SidebarLeft). Flag to user at handoff. NOT committed.

*** USER COMMITTED MID-STREAM ***: reflog shows HEAD advanced faab4c1 -> 40cecfb "feat: implement Git repository indexing support and add customizable answer temperature settings". This commit bundled Tasks 1-13 + pre-existing WIP (MessageBubble answer-temperature, now completed & compiling). My earlier "MessageBubble reverted" alarm was a FALSE ALARM — HEAD just advanced to include it. No WIP destroyed.
NEW BASE = 40cecfb. Remaining uncommitted = Task 14's 3 files (AuthenticatedApp, SidebarLeft, KbDataContext) + other pre-existing WIP (AnchoredPopover, ChatView, MessageContent, useAnchoredPosition).
tsc -b --force = CLEAN across entire frontend (0 errors) -> Task 14 integration verified at type level; whole frontend build green.

Task 14: implemented (KbDataContext 7 fields, AuthenticatedApp hook+dataValue+lifecycle, SidebarLeft destructure+handleSelect+4 props+GitRepoModal mount). tsc -b clean. Reviewing.

Task 14: complete (KbDataContext/AuthenticatedApp/SidebarLeft wired). Review Approved, no swaps/issues. Feature wired end-to-end. tsc -b clean.

INTEGRATION CHECKPOINT (post-Task14): go build ./... clean; gitrepo+jobs tests pass; frontend `npm run build` (tsc -b && vite build) rc=0. WHOLE FEATURE GREEN.

Task 15: complete (CLAUDE.md migration/flag/recipe-table/package-map rows + docs/feature-recipes.md "Git repository source" section). Verified aligned. Memory written (project_git_repo_source_feature.md + MEMORY.md pointer).
ALL 15 TASKS COMPLETE. Final whole-feature review dispatched (opus) over 2912-line scoped package.

FINAL REVIEW (opus): Verdict "needs fixes". 3 Important found (cross-cutting, missed by per-task reviews):
  1. storage key = blob SHA only -> identical-content files collide -> delete orphans survivor. FIX: key includes sha256(path)[:16].
  2. git_repo_enabled gate UI-only, not server-side (confluence enforces in handler). FIX: GetSiteConfigValue on Store + 403 gate in CreateSource+TriggerSync.
  3. f.Contents() reads every blob before size/skip-dir/ext filter -> memory DoS. FIX: ShouldIngestPath (path/size first) + IsBinaryContent (content sniff after).
  New obs (NOT fixed, inherited-from-confluence/cosmetic): source-delete orphans vector chunks+storage (same as confluence; KB-delete fine); partial-ingest + same-SHA no-op = failed files not retried until HEAD moves; sync_progress never advances (dead bar); stuck 'syncing' on outer asynq timeout.
  Minor triage: GetGitRepoSourceFileProgress = DEAD CODE (uncalled); intPtr->new(int) trivial; rest keep.
Dispatched batched fix for the 3 Important.

Final-review fix: DONE (3 Important). Fix1 unique storage key sha256(path)[:16]; Fix2 server-side git_repo_enabled gate (GetSiteConfigValue on Store + 403 in CreateSource+TriggerSync + TestCreateSourceDisabledReturns403); Fix3 ShouldIngestPath/IsBinaryContent split (path/size before blob read). 30 gitrepo tests pass, build server+worker+./... clean, vet clean. Re-review dispatched.

Final-fix re-review: APPROVED, all 3 fixes correct, no issues.
=== FEATURE COMPLETE ===
FINAL VERIFICATION: go build ./... clean + vet clean + gitrepo/jobs tests pass; frontend npm run build (tsc -b && vite) rc=0; SourcesSection vitest 4/4.
All 15 tasks done; all per-task reviews passed (fixes applied); final whole-feature review's 3 Important findings fixed + re-reviewed clean.
COMMIT STATE: Tasks 1-13 committed by USER in 40cecfb (bundled w/ unrelated answer-temp work). UNCOMMITTED: Task 14 wiring (KbDataContext/AuthenticatedApp/SidebarLeft), final-review fixes (internal/gitrepo/*), Task 15 docs (CLAUDE.md, feature-recipes.md), + unrelated pre-existing WIP (AnchoredPopover, ChatView, MessageContent, useAnchoredPosition).
