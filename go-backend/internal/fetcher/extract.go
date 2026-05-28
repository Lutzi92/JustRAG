package fetcher

import (
	"fmt"
	"net/url"
	"strings"

	mdconv "github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	mdbase "github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	mdplugins "github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	readability "github.com/go-shiori/go-readability"
	trafilatura "github.com/markusmobius/go-trafilatura"
)

// Extract runs primary (trafilatura) extraction and falls back to
// go-readability if trafilatura returns no usable text. The HTML fragment
// returned by extraction is then converted to markdown.
//
// On success it returns (title, markdown, nil).
func Extract(html, baseURL string) (string, string, error) {
	if strings.TrimSpace(html) == "" {
		return "", "", fmt.Errorf("extract: empty HTML")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil {
		return "", "", fmt.Errorf("extract: parse base URL %q: %w", baseURL, err)
	}

	// Primary: trafilatura.
	tOpts := trafilatura.Options{
		IncludeImages: false,
		IncludeLinks:  true,
		OriginalURL:   parsed,
	}
	tRes, err := trafilatura.Extract(strings.NewReader(html), tOpts)
	if err == nil && tRes != nil {
		// Prefer ContentNode (returns HTML) for markdown conversion; fall back to ContentText.
		var fragment string
		if tRes.ContentNode != nil {
			fragment = renderHTMLNode(tRes.ContentNode)
		}
		if strings.TrimSpace(fragment) == "" {
			fragment = tRes.ContentText
		}
		if strings.TrimSpace(fragment) != "" {
			md, mdErr := htmlToMarkdown(fragment, baseURL)
			if mdErr == nil && strings.TrimSpace(md) != "" {
				title := tRes.Metadata.Title
				return title, md, nil
			}
		}
	}

	// Fallback: go-readability.
	rRes, rErr := readability.FromReader(strings.NewReader(html), parsed)
	if rErr != nil {
		return "", "", fmt.Errorf("extract: readability fallback: %w", rErr)
	}
	md, mdErr := htmlToMarkdown(rRes.Content, baseURL)
	if mdErr != nil {
		return "", "", fmt.Errorf("extract: markdown convert: %w", mdErr)
	}
	if strings.TrimSpace(md) == "" {
		return "", "", fmt.Errorf("extract: readability produced empty markdown")
	}
	return rRes.Title, md, nil
}

// htmlToMarkdown converts an HTML fragment to markdown.
func htmlToMarkdown(htmlFragment, baseURL string) (string, error) {
	conv := mdconv.NewConverter(
		mdconv.WithPlugins(
			mdbase.NewBasePlugin(),
			mdplugins.NewCommonmarkPlugin(),
		),
	)
	var opts []mdconv.ConvertOptionFunc
	if strings.TrimSpace(baseURL) != "" {
		opts = append(opts, mdconv.WithDomain(baseURL))
	}
	md, err := conv.ConvertString(htmlFragment, opts...)
	if err != nil {
		return "", err
	}
	return md, nil
}
