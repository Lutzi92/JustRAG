# Full Iterative DRIFT — Progress Ledger

Branch: feat/reingest-kg-staleness (NO COMMITS — all changes stay in working tree)
Plan: docs/superpowers/plans/2026-06-26-full-iterative-drift.md
Review model: per-task working-tree diff via snapshots (no SHAs, since uncommitted)

- Task 1: config readers — COMPLETE (review clean; Minor: 'clamp' doc wording matches existing siteconfig.go convention, not fixed)
- Task 2: community-primer content fetch — COMPLETE (review clean; implementer added necessary nil-guard preserving MVP nil contract)
- Task 3: drift follow-up generator (ai+prompts) — COMPLETE (review clean; impl correctly uses resolver.structuredCompletionFn seam per package convention. Minor: confusing test-failure msg, not fixed)
- Task 4: RunDriftChat orchestrator + metric — COMPLETE (review clean; impl renamed test helper chunk()->driftChunk() to avoid same-package collision)
- Task 5: dispatch wiring + docs — COMPLETE (review clean; 2 Minor doc/log nits fixed inline by controller: drift_enabled log pair + chat_drift_model in per-task e.g. list)

## Minor findings (deferred to final-review triage)
- Task 4 / drift_chat.go: no `rag.drift_chat.completed` Info log (agentic template has one). Add between RecordDriftRun and the answer emit.
- Task 4 / drift_chat_test.go: no test asserts the clamp paths (e.g. MaxFollowups=9 -> 4).
- Task 4 / drift_chat_test.go: no test for genFollowups returning empty-slice-no-error (only the error path is covered; same branch).
- Task 4 / drift_chat.go: explicit `Findings: 0` on the skip-path emit is cosmetic (zero value).

## Final whole-branch review (opus) — DONE
- Verdict: READY after one Important fix.
- I-1 (Important): DRIFT turns mislabelled mode="standard" in agent_decisions (mode switch lacked willRunDrift case). FIXED inline by controller (http_send.go: `case willRunDrift: mode = "drift"`). Build + chat/ai/observability suites pass.
- 4 Minors all triaged DEFER (logging parity, 2 redundant test gaps, cosmetic zero-value emit). None must-fix.
- FEATURE COMPLETE — all 5 tasks reviewed clean, final review clean post-I-1.
