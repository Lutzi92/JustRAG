# CERT-Bund / WID Enrichment — SDD Progress Ledger

Plan: docs/superpowers/plans/2026-06-30-cert-bund-wid-enrichment.md
Mode: subagent-driven, working on main (user consent), BUILD-ONLY (no commits; user commits).
BASE_SHA: c9d6221e649e02f1a21d47bb4d1e380edb8aa267
(Previous git-repo-source ledger archived/overwritten; that feature was COMPLETE.)

Task 1 (spike + doc.go): COMPLETE.
  - Fetched live WID advisory WID-SEC-2026-2038 (Angular) -> testdata/advisory.json (7341 bytes).
  - SPIKE FINDINGS (corrected the Python script's assumptions):
    * Top-level children carry stable `type` discriminator: scoreListe, cveIdListe,
      documentReferenceListe, productReferenceListe (also cveListe, revisionsListe unused).
    * Leaf values under each section-child's `properties`: basescore/temporalscore/classification,
      cveId, url, name.
    * CVSS scores are JSON NUMBERS (basescore:98 ~ 9.8 stored x10). Kept raw (faithful to script). FLAG to user.
    * initialreleasedate is ISO timestamp -> formatter trims to YYYY-MM-DD.
  - doc.go written by controller (Host, path consts, section* type consts). Parser keys on `type` (not content-probe).
  - Plan Task 3 updated to type-based parser.

Task 2-5 (internal/widcert package: name/advisory/format/client + tests): COMPLETE.
  - 8 files; 14 tests pass; build+vet clean (controller verified independently).
  - Review (sonnet): Spec ✅, Quality Approved. No Critical/Important. 2 Minors (acceptable, deferred):
    * non-200 body not drained before Close (connection-reuse only; low-freq use). 
    * fixture test asserts BaseScore!="" not =="98" (specific value covered by NumericScores test).
  - NOT committed.

Task 6-7 (worker integration + app wiring + CLAUDE.md): COMPLETE.
  - rsspoll.go (WID branch + kill switch + isWIDLink), rsspoll_test.go (4 new tests + 5 call sites fixed),
    app/worker.go (WIDClient+SiteConfig wired via chatStore), CLAUDE.md (flag bullet).
  - 36 worker tests pass; build ./... + vet clean (controller verified).
  - Review (sonnet): Spec PASS, Quality Approved. No Critical/Important. 1 Minor: misleading log on (nil,nil)
    Fetch (unreachable via real client). FIXED by controller (switch: log "no advisory" vs error). Re-tested green.
  - NOT committed.

ALL TASKS COMPLETE.

FINAL WHOLE-BRANCH REVIEW (opus): Verdict "Needs fixes: 0 Critical, 1 Important".
  - IMPORTANT (FIXED): widcert.NewClient used httpclient.New (shared transport, NO private-IP block,
    follows redirects) -> SSRF: a 302 from WID host to 169.254.169.254 metadata would be followed.
    FIX: swapped to fetcher.SafeHTTPClient(fetchTimeout) (safeDialContext blocks RFC1918/link-local/
    loopback at dial, redirect-aware). client_test.go updated to use srv.Client() (safe transport
    blocks httptest loopback). Re-verified: build ./... + vet + test (count=1) all green.
  - MINOR (USER CHOSE DECIMAL, DONE): formatCVSSScore divides integer scores /10 -> "CVSS Base: 9.8 |
    Temporal: 8.5" (decimal/unparseable pass through). +TestFormatMarkdown_CVSSDecimal. Real fixture
    confirmed rendering 9.8. 15 widcert tests now.
  - All other cross-cutting checks PASSED: e2e flow (origin=rss/text-markdown/GUID-dedup unchanged),
    fail-open complete, kill-switch default-ON, parser matches real fixture, name/UUID injection-safe,
    8MB body cap, 15s timeout, SanitizeError no-leak.

=== FEATURE COMPLETE (build-only, uncommitted) ===
Files: NEW go-backend/internal/widcert/ (9 files + testdata/advisory.json);
  MODIFIED go-backend/internal/{worker/rsspoll.go, worker/rsspoll_test.go, app/worker.go}, CLAUDE.md.
Verification: go build ./... clean; go vet clean; go test ./internal/widcert ./internal/worker green
  (14 widcert + 36 worker tests). NOT committed (user commits).
