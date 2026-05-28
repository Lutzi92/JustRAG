package fetcher

import (
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// extractLinks parses html and returns absolute http(s) links found in
// <a href> elements, resolved against base. Fragments are stripped and the
// list is deduplicated. Non-http(s) schemes (mailto:, javascript:, etc.)
// and bare-fragment hrefs are skipped.
func extractLinks(html, base string) []string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		raw, _ := s.Attr("href")
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "#") {
			return
		}
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		// Skip non-http(s) schemes on the parsed href first to avoid
		// resolving e.g. "mailto:" against the base.
		if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
			return
		}
		abs := baseURL.ResolveReference(u)
		// Re-check the resolved scheme: if baseURL itself was non-http(s)
		// (relative href + weird base), the resolved URL could carry that
		// scheme through. Enforce the http(s)-only contract here too.
		if abs.Scheme != "http" && abs.Scheme != "https" {
			return
		}
		abs.Fragment = ""
		absURL := abs.String()
		if _, ok := seen[absURL]; ok {
			return
		}
		seen[absURL] = struct{}{}
		out = append(out, absURL)
	})
	return out
}
