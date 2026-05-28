package fetcher

import (
	"math/rand"
	"os"
	"time"
)

// userAgentPool contains current browser UA strings. Rotate per-request to
// reduce fingerprinting surface. Bump versions periodically.
var userAgentPool = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.2 Safari/605.1.15",
}

// randomUserAgent picks a random UA from the pool.
func randomUserAgent() string {
	return userAgentPool[rand.Intn(len(userAgentPool))]
}

// resolveUserAgent returns override if non-empty, otherwise a random pool UA.
func resolveUserAgent(override string) string {
	if override != "" {
		return override
	}
	return randomUserAgent()
}

// Mode selects which tier(s) the fetcher will try.
type Mode int

const (
	// ModeAuto: Tier 1 with automatic Tier 2 escalation on failure markers.
	ModeAuto Mode = iota
	// ModeHTTPOnly: Tier 1 only — no headless browser even on failure.
	ModeHTTPOnly
	// ModeBrowser: skip Tier 1, go straight to Tier 2.
	ModeBrowser
	// ModeJustFind: Tier 3 (persistent-context browser, Anubis-aware).
	ModeJustFind
)

// Options controls a single Fetch call.
type Options struct {
	Mode Mode
	// SkipExtraction disables readability extraction. The zero value is
	// false, so Options{} extracts content by default — this matches what
	// every RAG ingestion path actually wants. Set to true only when the
	// caller specifically needs raw HTML (e.g. JustFind result-list scrape).
	SkipExtraction bool
	Timeout        time.Duration // overall budget; 0 → Config.DefaultTimeout
	UserAgent      string        // override Config.UserAgent for this call
	MaxBytes       int64         // body cap; 0 → Config.MaxBytes
	// AllowPrivateHosts opts this request out of SSRF private-IP
	// rejection. **Only** trusted callers with a URL from site-config
	// (never from end-user input) should set this — e.g. academic search
	// hitting an intranet VuFind instance. Scheme and non-empty-host
	// validation still apply.
	AllowPrivateHosts bool
}

// Config configures the Fetcher itself; passed to New.
type Config struct {
	UserAgent          string
	AcceptLanguage     string
	DefaultTimeout     time.Duration
	DialTimeout        time.Duration
	HeaderTimeout      time.Duration
	MaxBytes           int64
	MaxRedirects       int
	PerHostRPS         float64 // requests per second per registrable domain
	PerHostConcurrency int
	GlobalConcurrency  int
	// JustFindUserDataDir is the host path for the Tier 3 persistent
	// browser profile. **Required** when ModeJustFind is used; no default.
	// Empty value causes Tier 3 to fail with a clear error rather than
	// silently writing Chrome state into the worker's CWD.
	JustFindUserDataDir string
	BrowserPoolSize     int           // Tier 2 incognito sessions per browser
	BrowserRecycleAfter int           // pages before recycling the browser
	BrowserRecycleEvery time.Duration // wall-clock recycle interval
	ProxyURL            string        // optional outbound proxy
	// BrowserBin is the absolute path to a system Chromium / Chrome
	// binary. When set, rod uses this instead of downloading its own
	// Chromium bundle into ~/.cache/rod on first call. The Dockerfile
	// ships chromium via apk; point this at /usr/bin/chromium so cold
	// starts don't hit the ~300 MB rod downloader. Empty = let rod fall
	// back to its bundled download (acceptable for local dev only).
	BrowserBin string
	// AllowNoSandbox enables Chromium's --no-sandbox flag for the rod
	// launcher. This disables the OS-level sandbox and is **only** safe in
	// containerised environments where the container itself provides the
	// isolation boundary (e.g. our K8s worker pods). It MUST NOT be enabled
	// when running on a developer host or any environment where untrusted
	// content could lead to arbitrary code execution escaping the browser
	// process. Opt-in only.
	AllowNoSandbox bool
}

// DefaultConfig returns a sensible default Config.
func DefaultConfig() Config {
	ua := os.Getenv("FETCHER_USER_AGENT")
	// If empty, resolveUserAgent() picks a random pool UA per request.
	// Resolve BrowserBin from env, then from common system paths. This
	// stops rod from downloading its own Chromium bundle on first Tier 2
	// call — the container ships one via apk, we just need to point at it.
	bin := os.Getenv("FETCHER_BROWSER_BIN")
	if bin == "" {
		for _, p := range []string{
			"/usr/bin/chromium",         // Alpine apk chromium, Debian chromium
			"/usr/bin/chromium-browser", // older Debian/Ubuntu
			"/usr/bin/google-chrome",    // Google Chrome
			"/usr/bin/chrome",           // misc
		} {
			if _, err := os.Stat(p); err == nil {
				bin = p
				break
			}
		}
	}
	return Config{
		UserAgent:           ua,
		AcceptLanguage:      "en-US,en;q=0.9,de;q=0.8",
		DefaultTimeout:      30 * time.Second,
		DialTimeout:         10 * time.Second,
		HeaderTimeout:       15 * time.Second,
		MaxBytes:            10 << 20, // 10 MB
		MaxRedirects:        10,
		PerHostRPS:          1,
		PerHostConcurrency:  2,
		GlobalConcurrency:   32,
		BrowserPoolSize:     6,
		BrowserRecycleAfter: 500,
		BrowserRecycleEvery: 1 * time.Hour,
		BrowserBin:          bin,
	}
}

// Result is what Fetch returns on success.
type Result struct {
	URL        string // final URL after redirects
	StatusCode int
	Title      string   // from extraction; may be empty
	Markdown   string   // extracted content as markdown (unless SkipExtraction)
	HTML       string   // raw HTML (always populated)
	Links      []string // outbound absolute http(s) links (Tier 1 and Tier 2; Tier 3 omits)
	Tier       int      // which tier produced this result: 1, 2, or 3
	// Headers holds the **first value** of selected response headers
	// (content-type, content-language, etc.). This is intentionally lossy:
	// multi-value headers like Set-Cookie and Link are not preserved here.
	// Callers needing the full http.Header should fetch raw via Tier 1.
	Headers map[string]string
	// FetchedAt is the wall-clock time the response was received, in UTC.
	// Always set via time.Now().UTC() so serialised timestamps are stable.
	FetchedAt time.Time
}
