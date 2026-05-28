package fetcher

import (
	"log/slog"
	"strings"

	"golang.org/x/net/html"
)

// renderHTMLNode serialises an *html.Node back to an HTML string.
// Returns "" if the input is not an *html.Node or is nil.
func renderHTMLNode(n *html.Node) string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	if err := html.Render(&sb, n); err != nil {
		slog.Debug("fetcher: html.Render failed for node", "err", err)
		return ""
	}
	return sb.String()
}
