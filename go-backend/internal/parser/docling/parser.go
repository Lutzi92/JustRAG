package docling

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/justrag/go-backend/internal/parser"
)

// convertAndMap performs the Docling HTTP call and maps the result onto a
// parser.ParseResult. Used by both PDF and DOCX Docling parsers.
func convertAndMap(ctx context.Context, c *Client, pctx parser.ParseContext) (*parser.ParseResult, error) {
	if c == nil {
		return nil, fmt.Errorf("docling parser: client not configured")
	}
	f, err := os.Open(pctx.FilePath)
	if err != nil {
		return nil, fmt.Errorf("docling parser: open file: %w", err)
	}
	defer f.Close()

	res, err := c.Convert(ctx, pctx.FileName, f)
	if err != nil {
		return nil, err
	}

	primary := res.Markdown
	if primary == "" {
		primary = res.Text
	}

	return &parser.ParseResult{
		Text:       primary,
		PageSpans:  buildPageSpans(primary, res.Items),
		IsMarkdown: true,
	}, nil
}

// buildPageSpans locates where each page starts inside the converted text.
//
// Docling returns the document as one markdown blob plus a DoclingDocument
// whose items carry page provenance, so page boundaries have to be recovered
// by finding each item's text back in the markdown. Items are scanned in
// reading order with a monotonically advancing offset; the first item of each
// new page that can be located anchors that page's span. Items that cannot be
// matched (markdown escaping, table rendering) are skipped — the next item on
// the same page anchors it instead.
//
// Returns nil when no page could be anchored, which makes the caller emit no
// page metadata rather than a fabricated page 1.
func buildPageSpans(text string, items []DocItem) []parser.PageSpan {
	if text == "" || len(items) == 0 {
		return nil
	}

	var spans []parser.PageSpan
	searchFrom := 0
	currentPage := 0

	for _, it := range items {
		if it.Text == "" {
			continue
		}
		pos := -1
		if searchFrom < len(text) {
			if p := strings.Index(text[searchFrom:], it.Text); p >= 0 {
				pos = searchFrom + p
			}
		}
		if pos < 0 {
			continue
		}
		// Advance past this item's start so repeated boilerplate (headers,
		// footers) resolves forward through the document.
		searchFrom = pos + len(it.Text)

		if it.Page <= 0 || it.Page == currentPage {
			continue
		}
		// Keep spans strictly increasing; out-of-order provenance (rare, but
		// possible with multi-column layouts) must not corrupt the mapping.
		if len(spans) > 0 && pos <= spans[len(spans)-1].Start {
			continue
		}
		spans = append(spans, parser.PageSpan{Start: pos, Page: it.Page})
		currentPage = it.Page
	}

	if len(spans) == 0 {
		return nil
	}
	// Anything before the first anchor (a title, a leading image caption)
	// belongs to that first page.
	spans[0].Start = 0
	return spans
}

// DoclingPDFParser is a parser.Parser that routes PDFs through a Docling
// Serve sidecar for layout-aware extraction.
type DoclingPDFParser struct {
	Client *Client
}

// Name returns the parser name (used in logs).
func (p *DoclingPDFParser) Name() string { return "docling-pdf" }

// CanParse returns true for PDFs.
func (p *DoclingPDFParser) CanParse(mimeType, fileName string) bool {
	return mimeType == "application/pdf" || strings.HasSuffix(strings.ToLower(fileName), ".pdf")
}

// Parse uploads the file to Docling Serve and converts the response into a
// parser.ParseResult. Per-page text is preserved when Docling returns it.
func (p *DoclingPDFParser) Parse(ctx context.Context, pctx parser.ParseContext) (*parser.ParseResult, error) {
	return convertAndMap(ctx, p.Client, pctx)
}

// DoclingDocxParser routes DOCX files through a Docling Serve sidecar.
// Preserves headings, tables (as markdown), and footnotes — the biggest
// quality wins vs. the built-in DocxParser.
type DoclingDocxParser struct {
	Client *Client
}

// Name returns the parser name (used in logs).
func (p *DoclingDocxParser) Name() string { return "docling-docx" }

// CanParse returns true for DOCX files.
func (p *DoclingDocxParser) CanParse(mimeType, fileName string) bool {
	return mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
		strings.HasSuffix(strings.ToLower(fileName), ".docx")
}

// Parse uploads the DOCX to Docling Serve and converts the response.
func (p *DoclingDocxParser) Parse(ctx context.Context, pctx parser.ParseContext) (*parser.ParseResult, error) {
	return convertAndMap(ctx, p.Client, pctx)
}

// DoclingPptxParser routes PPTX files through a Docling Serve sidecar.
// Preserves slide titles (as headings), tables, and speaker notes.
type DoclingPptxParser struct {
	Client *Client
}

// Name returns the parser name (used in logs).
func (p *DoclingPptxParser) Name() string { return "docling-pptx" }

// CanParse returns true for PPTX files.
func (p *DoclingPptxParser) CanParse(mimeType, fileName string) bool {
	return mimeType == "application/vnd.openxmlformats-officedocument.presentationml.presentation" ||
		strings.HasSuffix(strings.ToLower(fileName), ".pptx")
}

// Parse uploads the PPTX to Docling Serve and converts the response.
func (p *DoclingPptxParser) Parse(ctx context.Context, pctx parser.ParseContext) (*parser.ParseResult, error) {
	return convertAndMap(ctx, p.Client, pctx)
}
