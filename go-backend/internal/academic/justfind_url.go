package academic

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/net/html"

	"github.com/justrag/go-backend/internal/fetcher"
)

// isJustFindURL returns true if the URL contains "justfind" or "edsrecord"
// (case-insensitive), indicating it points to a JustFind catalogue page
// rather than a direct PDF download.
func isJustFindURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.Contains(lower, "justfind") || strings.Contains(lower, "edsrecord")
}

// extractPDFFromJustFind uses the Fetcher's Tier 3 (browser) to load a
// JustFind page and extract the actual PDF download URL from the rendered
// content. It looks for links containing ".pdf", "pdfviewer", or "pdf-ft".
func extractPDFFromJustFind(ctx context.Context, f *fetcher.Fetcher, pageURL string) (string, error) {
	res, err := f.Fetch(ctx, pageURL, fetcher.Options{
		Mode:              fetcher.ModeJustFind,
		SkipExtraction:    true,
		AllowPrivateHosts: true,
	})
	// Fallback to plain HTTP if Tier 3 fails (no chromium, etc.)
	if err != nil {
		slog.Warn("justfind url: Tier 3 failed, trying plain HTTP", "url", pageURL, "err", err)
		res, err = f.Fetch(ctx, pageURL, fetcher.Options{
			Mode:              fetcher.ModeHTTPOnly,
			SkipExtraction:    true,
			AllowPrivateHosts: true,
		})
	}
	if err != nil {
		return "", fmt.Errorf("justfind url: fetch: %w", err)
	}

	// Collect candidate PDF links and score them to pick the best match.
	pdfMarkers := []string{".pdf", "pdfviewer", "pdf-ft"}

	var bestLink string
	bestScore := -1
	var firstRawMatch string

	for _, li := range extractLinks(res.HTML) {
		hrefLower := strings.ToLower(li.href)

		// Must contain at least one PDF marker to be a candidate.
		isPDF := false
		for _, marker := range pdfMarkers {
			if strings.Contains(hrefLower, marker) {
				isPDF = true
				break
			}
		}
		if !isPDF {
			continue
		}

		// Remember the first raw match as fallback.
		if firstRawMatch == "" {
			firstRawMatch = li.href
		}

		score := scorePDFLink(li)
		if score > bestScore {
			bestScore = score
			bestLink = li.href
		}
	}

	if bestLink != "" {
		slog.Info("justfind url: extracted PDF link", "pageURL", pageURL, "pdfURL", bestLink, "score", bestScore)
		return bestLink, nil
	}
	if firstRawMatch != "" {
		slog.Info("justfind url: extracted PDF link (fallback)", "pageURL", pageURL, "pdfURL", firstRawMatch)
		return firstRawMatch, nil
	}

	return "", fmt.Errorf("justfind url: no PDF link found on page %s", pageURL)
}

// linkInfo holds a parsed <a> element's href and visible text.
type linkInfo struct {
	href string
	text string
}

// extractLinks returns all <a> elements from raw HTML with their href and
// inner text. It uses a proper HTML tokenizer to avoid capturing links from
// comments, scripts, or malformed markup.
func extractLinks(rawHTML string) []linkInfo {
	var links []linkInfo
	z := html.NewTokenizer(strings.NewReader(rawHTML))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return links
		case html.StartTagToken:
			tn, hasAttr := z.TagName()
			if !hasAttr || string(tn) != "a" {
				continue
			}
			var href string
			for {
				key, val, more := z.TagAttr()
				if string(key) == "href" {
					href = strings.TrimSpace(string(val))
				}
				if !more {
					break
				}
			}
			if href == "" {
				continue
			}
			// Collect inner text until the closing </a>.
			var textParts []string
			for {
				inner := z.Next()
				if inner == html.ErrorToken {
					links = append(links, linkInfo{href: href, text: strings.Join(textParts, " ")})
					return links
				}
				if inner == html.EndTagToken {
					tn2, _ := z.TagName()
					if string(tn2) == "a" {
						break
					}
				}
				if inner == html.TextToken {
					if t := strings.TrimSpace(string(z.Text())); t != "" {
						textParts = append(textParts, t)
					}
				}
			}
			links = append(links, linkInfo{href: href, text: strings.Join(textParts, " ")})
		}
	}
}

// scorePDFLink assigns a priority score to a candidate PDF link.
// Higher scores indicate a more likely primary PDF download.
func scorePDFLink(li linkInfo) int {
	score := 0
	hrefLower := strings.ToLower(li.href)
	textLower := strings.ToLower(li.text)

	// Positive href signals.
	if strings.HasSuffix(hrefLower, ".pdf") {
		score += 10
	}
	for _, kw := range []string{"/fulltext", "/download"} {
		if strings.Contains(hrefLower, kw) {
			score += 5
		}
	}
	for _, kw := range []string{"paper", "article"} {
		if strings.Contains(hrefLower, kw) {
			score += 3
		}
	}

	// Positive anchor-text signals.
	for _, kw := range []string{"pdf", "download", "full text", "fulltext"} {
		if strings.Contains(textLower, kw) {
			score += 4
		}
	}

	// Negative signals — deprioritize thumbnails, supplements, figures.
	for _, kw := range []string{"thumbnail", "supplement", "figure"} {
		if strings.Contains(hrefLower, kw) || strings.Contains(textLower, kw) {
			score -= 10
		}
	}

	return score
}
