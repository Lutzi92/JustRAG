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
