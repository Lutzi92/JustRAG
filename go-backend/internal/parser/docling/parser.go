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

	pages := make([]parser.PageText, 0, len(res.Pages))
	for _, dp := range res.Pages {
		pages = append(pages, parser.PageText{PageNumber: dp.Number, Text: dp.Text})
	}

	return &parser.ParseResult{
		Text:       primary,
		Pages:      pages,
		IsMarkdown: true,
	}, nil
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
