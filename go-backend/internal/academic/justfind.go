// Package academic implements the JustFind search client and HTTP handlers
// for academic research endpoints.
package academic

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/justrag/go-backend/internal/fetcher"
)

// AcademicPaper represents a single result from a JustFind search.
type AcademicPaper struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Authors         []string `json:"authors"`
	Year            int      `json:"year"`
	Journal         string   `json:"journal,omitempty"`
	Volume          string   `json:"volume,omitempty"`
	Issue           string   `json:"issue,omitempty"`
	Pages           string   `json:"pages,omitempty"`
	Abstract        string   `json:"abstract,omitempty"`
	DOI             string   `json:"doi,omitempty"`
	URL             string   `json:"url"`
	PdfURL          string   `json:"pdfUrl,omitempty"`
	CitationCount   int      `json:"citationCount,omitempty"`
	HarvardCitation string   `json:"harvardCitation,omitempty"`
}

// SearchOptions configures a JustFind search.
type SearchOptions struct {
	PeerReviewedOnly bool
	Limit            int
	// HTTPOnly forces Tier 1 HTTP fetch instead of Tier 3 JustFind mode.
	// This is intended for test environments where a chromium build is not
	// available and the target server does not require Anubis bot protection.
	HTTPOnly bool
}

// SearchJustFind queries a JustFind/VuFind instance for academic papers matching
// query and returns parsed results. baseURL should be the root of the instance
// (e.g. "https://library.example.com").
//
// f is the shared Fetcher; ModeJustFind is used so that Tier 3 (persistent
// context, Anubis-aware) handles the request. SkipExtraction is true because
// the result-list HTML is structured data that is scraped via goquery rather
// than being converted to markdown.
//
// In a non-chromium build, ModeJustFind returns ErrUnsupportedMode and the
// caller receives a wrapped error.
func SearchJustFind(ctx context.Context, f *fetcher.Fetcher, baseURL, query string, opts SearchOptions) ([]AcademicPaper, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	// Build the search URL.
	searchURL := fmt.Sprintf(
		"%s/EDS/Search?lookfor=%s&type=AllFields&limit=%d&filter[]=LIMIT%%7CFT:%%22y%%22",
		strings.TrimRight(baseURL, "/"),
		url.QueryEscape(query),
		limit,
	)
	if opts.PeerReviewedOnly {
		searchURL += "&filter[]=LIMIT%7CRV:y"
	}

	mode := fetcher.ModeJustFind
	if opts.HTTPOnly {
		mode = fetcher.ModeHTTPOnly
	}
	res, err := f.Fetch(ctx, searchURL, fetcher.Options{
		Mode:           mode,
		SkipExtraction: true, // we scrape specific selectors; markdown extraction is not useful here
		// baseURL comes from trusted site config (academic_search_base_url),
		// not end-user input. Opt out of SSRF private-IP rejection so
		// intranet/self-hosted JustFind instances remain reachable.
		AllowPrivateHosts: true,
	})
	// Fallback path: retry with plain HTTP on ANY Tier 3 failure. The old
	// path only caught ErrUnsupportedMode (missing chromium build tag), but
	// Tier 3 can also fail at runtime for a variety of infrastructure
	// reasons in a chromium-enabled build — Chromium binary missing on the
	// host, JUSTFIND_USER_DATA_DIR unwritable, rod launcher/connect flakes,
	// sandbox denied, etc. In all of those cases plain HTTP is still a
	// viable path for JustFind instances that don't require Anubis, and
	// silently falling back preserves the pre-rebuild behaviour for
	// non-container `go run -tags chromium` and bare-metal deployments.
	//
	// If the URL itself was invalid (SSRF rejection, parse error), Tier 1
	// will fail with the same error, so the fallback is safe. An upstream
	// JustFind server error is detected later via hasJustFindMarker, not
	// by the fetch call returning a non-nil error.
	if err != nil && mode == fetcher.ModeJustFind {
		slog.Warn("justfind: Tier 3 fetch failed, falling back to plain HTTP",
			"query", query, "err", err)
		res, err = f.Fetch(ctx, searchURL, fetcher.Options{
			Mode:              fetcher.ModeHTTPOnly,
			SkipExtraction:    true,
			AllowPrivateHosts: true,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("justfind: fetch: %w", err)
	}

	// Tier 1 exposes the real upstream HTTP status; reject non-200 here so
	// a templated 500/404 page that still ships shared VuFind layout
	// (e.g. .mainbody on the error template) doesn't get parsed as an
	// empty successful search.
	//
	// This check is scoped to Tier 1 only. Tier 3 (rod) does not reliably
	// expose the main-frame HTTP status: it falls back to 0 when the
	// network event is missed, and redirect/challenge flows can leave
	// non-200 codes even when the final HTML is a valid JustFind page.
	// The structural-marker check below handles Tier 3 — rejecting on
	// status there would make production academic search fail
	// intermittently on warmed profiles or redirecting deployments.
	if res.Tier == 1 && res.StatusCode != 200 {
		return nil, fmt.Errorf("justfind: upstream HTTP %d", res.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(res.HTML))
	if err != nil {
		return nil, fmt.Errorf("justfind: parse html: %w", err)
	}

	// Tier 3 (rod-based) always reports StatusCode=200 for a successful
	// navigation — it doesn't surface the upstream HTTP status. So a 404,
	// 500, or Anubis-challenge page would otherwise be parsed as an empty
	// result set, silently converting production failures into
	// false-successes. Guard against that by requiring a recognisable
	// VuFind/JustFind structural marker in the response before we trust it.
	//
	// Valid pages contain at least one of:
	//   - .result-list-item (actual results)
	//   - .no-results / .no-records-found (zero-results markers)
	//   - .mainbody, #search-home, .search-home (empty search / landing)
	if !hasJustFindMarker(doc) {
		return nil, fmt.Errorf("justfind: unexpected page content (no VuFind markers — upstream error or challenge page)")
	}

	return parseResults(doc, baseURL), nil
}

// hasJustFindMarker reports whether doc contains any selector that signals
// a real VuFind/JustFind response (either a result list, a zero-results
// marker, or the search-home container). Used to distinguish a valid empty
// search from an upstream error page that happens to navigate successfully.
func hasJustFindMarker(doc *goquery.Document) bool {
	selectors := []string{
		".result-list-item",
		".no-results",
		".no-records-found",
		"#search-home",
		".search-home",
		".mainbody",
	}
	for _, sel := range selectors {
		if doc.Find(sel).Length() > 0 {
			return true
		}
	}
	return false
}

// ParseResultsHTML parses a JustFind search-result HTML string and returns the
// extracted papers. baseURL is used to resolve relative hrefs. This is the
// exported counterpart of parseResults, available for testing and alternative
// callers that have already obtained the HTML by other means.
func ParseResultsHTML(html, baseURL string) ([]AcademicPaper, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("justfind: parse html: %w", err)
	}
	if !hasJustFindMarker(doc) {
		return nil, fmt.Errorf("justfind: unexpected page content (no VuFind markers — upstream error or challenge page)")
	}
	return parseResults(doc, baseURL), nil
}

// parseResults walks the parsed HTML document and extracts AcademicPaper entries.
func parseResults(doc *goquery.Document, baseURL string) []AcademicPaper {
	var papers []AcademicPaper

	doc.Find(".result-list-item").Each(func(i int, s *goquery.Selection) {
		paper := AcademicPaper{}

		// Extract title.
		if t := strings.TrimSpace(s.Find(".title a, h3 a, .result-title a").First().Text()); t != "" {
			paper.Title = t
		}

		// Extract URL.
		if href, exists := s.Find(".title a, h3 a, .result-title a").First().Attr("href"); exists {
			if strings.HasPrefix(href, "http") {
				paper.URL = href
			} else {
				paper.URL = strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(href, "/")
			}
		}

		// Extract COinS metadata from Z3988 span.
		if coinsTitle, exists := s.Find("span.Z3988").Attr("title"); exists {
			mergeCOinS(&paper, coinsTitle)
		}

		// Fall back to visible author text when COinS has none.
		if len(paper.Authors) == 0 {
			if authorText := strings.TrimSpace(s.Find(".author, .authors").First().Text()); authorText != "" {
				paper.Authors = splitAuthors(authorText)
			}
		}

		// Generate a stable ID when none came from metadata.
		if paper.ID == "" {
			if paper.URL != "" {
				paper.ID = stableID(paper.URL)
			} else {
				paper.ID = stableID(paper.Title)
			}
		}

		// Only include entries that have at least a title.
		if paper.Title != "" {
			papers = append(papers, paper)
		}
	})

	return papers
}

// mergeCOinS parses the URL-encoded COinS title attribute and copies fields
// into paper, overwriting only zero values.
func mergeCOinS(paper *AcademicPaper, coinsTitle string) {
	decoded, err := url.QueryUnescape(coinsTitle)
	if err != nil {
		// Try raw split without unescaping.
		decoded = coinsTitle
	}

	vals := make(map[string][]string)
	for _, part := range strings.Split(decoded, "&") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			k := strings.TrimSpace(kv[0])
			v := strings.TrimSpace(kv[1])
			if k != "" && v != "" {
				vals[k] = append(vals[k], v)
			}
		}
	}

	// rft.atitle — article title (takes priority over journal title for the
	// paper's Title field).
	if v := firstVal(vals, "rft.atitle"); v != "" && paper.Title == "" {
		paper.Title = v
	}

	// rft.jtitle / rft.btitle — journal / book title.
	if v := firstVal(vals, "rft.jtitle", "rft.btitle"); v != "" {
		paper.Journal = v
	}

	// Authors: rft.au (may appear multiple times), rft.aufirst+rft.aulast.
	for _, a := range vals["rft.au"] {
		if a != "" {
			paper.Authors = append(paper.Authors, a)
		}
	}
	if len(paper.Authors) == 0 {
		first := firstVal(vals, "rft.aufirst")
		last := firstVal(vals, "rft.aulast")
		if last != "" {
			if first != "" {
				paper.Authors = append(paper.Authors, first+" "+last)
			} else {
				paper.Authors = append(paper.Authors, last)
			}
		}
	}

	// Year / date.
	if paper.Year == 0 {
		if v := firstVal(vals, "rft.date"); v != "" {
			if y, err := strconv.Atoi(v[:min(4, len(v))]); err == nil {
				paper.Year = y
			}
		}
	}

	// Volume, issue, pages.
	if v := firstVal(vals, "rft.volume"); v != "" {
		paper.Volume = v
	}
	if v := firstVal(vals, "rft.issue"); v != "" {
		paper.Issue = v
	}
	if v := firstVal(vals, "rft.pages", "rft.spage"); v != "" {
		paper.Pages = v
	}

	// DOI / ID.
	if v := firstVal(vals, "rft_id"); v != "" {
		if strings.HasPrefix(v, "info:doi/") {
			paper.DOI = strings.TrimPrefix(v, "info:doi/")
			if paper.ID == "" {
				paper.ID = paper.DOI
			}
		} else if paper.ID == "" {
			paper.ID = v
		}
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func firstVal(m map[string][]string, keys ...string) string {
	for _, k := range keys {
		if vs := m[k]; len(vs) > 0 && vs[0] != "" {
			return vs[0]
		}
	}
	return ""
}

func splitAuthors(text string) []string {
	// Authors are commonly separated by semicolons, " and ", or commas.
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var parts []string
	for _, p := range strings.Split(text, ";") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) > 1 {
		return parts
	}
	// Try "and" separator.
	for _, p := range strings.Split(text, " and ") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) > 1 {
		return parts
	}
	return []string{text}
}

// stableID returns a short deterministic identifier for a string (URL or title).
// We use a simple djb2-style hash represented as hex.
func stableID(s string) string {
	var h uint64 = 5381
	for i := 0; i < len(s); i++ {
		h = h*33 + uint64(s[i])
	}
	return fmt.Sprintf("%016x", h)
}

// min is a convenience function (Go < 1.21 compat).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
