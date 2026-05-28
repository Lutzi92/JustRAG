// Package fetcher implements a tiered HTTP fetcher for JustRAG: a stdlib
// HTTP client with SSRF protection and readability extraction (Tier 1),
// a headless browser fallback for JS-rendered sites (Tier 2, build tag
// "chromium"), and a persistent-context fetcher for sites behind PoW
// challenges like Anubis (Tier 3, build tag "chromium").
//
// Build tag behavior: Tier 2 and Tier 3 are compiled in only when the
// "chromium" build tag is set. Without the tag, browser.go and justfind.go
// are excluded and the stubs in browser_stub.go / justfind_stub.go take
// their place — both fetchTier2 and fetchTier3 then return
// ErrUnsupportedMode at runtime (no graceful degradation inside the stub
// itself). Concretely, in a non-chromium build:
//   - ModeBrowser and ModeJustFind always fail with ErrUnsupportedMode.
//   - ModeHTTPOnly is unaffected and runs Tier 1 normally.
//   - ModeAuto runs Tier 1 first; if Tier 1 succeeds and shouldEscalate
//     is false, it returns that result. If escalation is attempted, the
//     Tier 2 stub fails immediately, and Fetch falls back to the Tier 1
//     result when Tier 1 had produced one. Only if both Tier 1 errored
//     and Tier 2 errored does the caller see a joined error. So ModeAuto
//     degrades gracefully to Tier 1 without chromium; explicit Browser /
//     JustFind modes do not.
//
// All outbound HTML/text fetches from crawl, websearch, and academic
// JustFind flow through this package — never construct an http.Client
// for HTML fetching elsewhere.
//
// Known exception: binary downloads (PDF papers from academic search)
// currently bypass this package because the Result type returns decoded
// text, not raw bytes. Adding a raw-bytes fetch mode is a planned follow-up.
package fetcher
