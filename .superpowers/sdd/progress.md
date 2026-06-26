# Chat-answer export — Progress Ledger

Branch: main (NO COMMITS — all changes stay in working tree, per user choice)
Plan: docs/superpowers/plans/2026-06-26-chat-answer-export.md
Review model: per-task working-tree diff via `git diff --no-index` against pre-task file-copy baselines (no per-task commits)
NOTE: this ledger was clobbered once mid-run (reverted to old DRIFT content); recovered from conversation + working-tree code. Source of truth = the code in web/src/ + this conversation.

- Task 1: buildAnswerMarkdown pure core — COMPLETE (5/5 tests; review clean. Minor deferred: `||` vs `??` for sourcesHeading default, brief-mandated)
- Task 2: copy + markdown-download wrappers — COMPLETE (7/7 tests; review clean. Minors deferred: no beforeEach mock-reset in copy describe; revokeObjectURL unasserted)
- Task 3: DOCX export + PDF print helpers — COMPLETE (10/10 tests; review clean. Minor deferred: printAnswerPdf `title` not HTML-escaped in doc.write, brief-mandated)
- Task 4: i18n keys — COMPLETE (7 keys added, lint clean; review clean. CONTROLLER FIX: brief's `exportDocx` collided with pre-existing key@370 → removed duplicate, menu reuses existing key. Only exportDocx collided (grep-verified all 8))
- Task 5: AnswerExportMenu component — COMPLETE (4/4 tests; review clean. Impl made 2 sound a11y lint fixes to menu div: removed unused eslint-disable, added tabIndex=-1 + onKeyDown Escape)
- Task 6: wire into MessageActions — COMPLETE (14/14 tests, lint clean; review clean. Additive; existing branches untouched; mocks extended not duplicated)
- Task 7: thread props through MessageBubble + ChatView — COMPLETE (29/29 suites, 200/200 tests, lint clean; build: 2 pre-existing TS errors in exportAnswer.test.ts unrelated to this task; 0 new errors from Task-7 code)
- Task 7 build-fix: CONTROLLER fixed 2 TS errors in exportAnswer.test.ts (Task 2 mock types) that only surfaced under `npm run build` tsc (createObjectURL param Blob|MediaSource; click spy `as unknown as () => void`). Build now exits 0, 200/200 tests, lint clean. Reviewed clean.
- Task 8: manual verification — PENDING (human/controller)

FINAL GATE STATUS: npm run build ✓, npm run test 200/200 ✓, npm run lint ✓.

## Final whole-feature review (opus) — DONE
- Verdict: READY AFTER FIXES → fix applied, now READY.
- IMPORTANT (FIXED inline by controller): printAnswerPdf injected `title` + `node.innerHTML` raw into the print iframe; sibling AcademicResearchMode.downloadPdf escapes title + DOMPurify's body. Self-XSS, low severity (per-user chats, no cross-user view). FIX: `title.replace(/</g,'&lt;')` + `DOMPurify.sanitize(node.innerHTML)` + `import DOMPurify from 'dompurify'`. Build/test/lint re-verified green.
- Reviewer confirmed end-to-end wiring correct; exportDocx i18n key reuse correct (not missing); streaming gated; resources cleaned up; body already rehype-sanitized (DOMPurify = defense-in-depth).
- Minor (NEW, deferred to human Task-8 QA): (1) PDF prints the Chain-of-Thought reasoning <details> since contentRef wraps the whole answer div — likely undesirable; (2) export button renders in BOTH top+bottom action bars (consistent w/ existing fork/feedback buttons, pre-existing pattern, shared contentRef — no action).
- FEATURE COMPLETE (Tasks 1-7 reviewed clean + final review clean post-fix). Task 8 = human manual QA. NO COMMITS — all changes in working tree.

## Minor findings (deferred to final-review triage)
- Task 1 / exportAnswer.ts:16: `opts.sourcesHeading || 'Sources'` uses `||` not `??`. Brief-mandated; no practical impact.
- Task 2 / exportAnswer.test.ts: copy describe block has no `beforeEach(vi.clearAllMocks)`; mock call history can accumulate if later tests assert on copyToClipboard.
- Task 2 / exportAnswer.test.ts: `revokeObjectURL` spy set up but never asserted in the download test.
