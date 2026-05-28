package fetcher

import (
	"regexp"
	"strings"
)

// emptyRootShellRe matches an SPA root container that is empty (only
// whitespace before the closing tag), which strongly suggests the page
// hasn't hydrated yet and Tier 1 will see no useful content.
var emptyRootShellRe = regexp.MustCompile(`<[^>]*id\s*=\s*"(?:root|app)"[^>]*>\s*</`)

// challengeMarkers is a list of substrings that, if present in a response
// body, indicate the page is gated by a JS challenge or anti-bot system
// that Tier 1 cannot bypass.
var challengeMarkers = []string{
	"Just a moment...", // Cloudflare interstitial
	"cf-mitigated",
	"__cf_chl",
	"challenge-platform",
	"anubis_version", // JustFind / Anubis
	"DataDome",
	"px-captcha", // PerimeterX
}

// shouldEscalate returns true if the Tier-1 result indicates we need to
// retry the page with the headless browser tier. Triggers:
//   - 403/503 with a known challenge marker
//   - 200 with very thin extracted content but JS-heavy DOM
//
// Does NOT escalate on 404/410 — those are dead URLs. Timeouts and other
// transport errors are represented by a nil Result and likewise do not
// escalate here.
func shouldEscalate(res *Result) bool {
	if res == nil {
		return false
	}
	// Hard-stop on dead URLs.
	switch res.StatusCode {
	case 404, 410:
		return false
	}
	// Status-code + body marker.
	if res.StatusCode == 403 || res.StatusCode == 503 {
		lowerHTML := strings.ToLower(res.HTML)
		for _, m := range challengeMarkers {
			if strings.Contains(lowerHTML, strings.ToLower(m)) {
				return true
			}
		}
	}
	// Empty extraction with JS-heavy body.
	if res.StatusCode == 200 && len(res.Markdown) < 200 {
		scriptCount := strings.Count(res.HTML, "<script")
		hasNextData := strings.Contains(res.HTML, "__NEXT_DATA__")
		hasEmptyRoot := emptyRootShellRe.MatchString(res.HTML)
		if scriptCount > 5 || hasNextData || hasEmptyRoot {
			return true
		}
	}
	return false
}
