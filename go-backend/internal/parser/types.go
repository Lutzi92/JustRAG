package parser

import "context"

// ParseContext holds metadata about the file to be parsed.
type ParseContext struct {
	FilePath  string
	FileName  string
	MimeType  string
	FileSize  int64
	KbID      string
	ChunkSize int // target chunk size in tokens (default 512)
}

// ParseResult contains the extracted text content from a parsed file.
type ParseResult struct {
	Text string // full extracted text content
	// Pages holds per-page text extracted from paginated formats (e.g. PDF).
	// Each entry is {PageNumber (1-based), Text (that page's content)}.
	// When non-empty, the processor splits each page independently so
	// chunks reliably know which page they belong to. When empty, the
	// processor splits the full Text as a single unpaginated document.
	Pages      []PageText
	IsMarkdown bool // true when Text/Pages content is markdown (headings, lists, etc.)
}

// PageText holds the extracted text for a single page.
type PageText struct {
	PageNumber int // 1-based
	Text       string
}

// Parser is the interface implemented by all format-specific parsers.
type Parser interface {
	Name() string
	CanParse(mimeType, fileName string) bool
	Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error)
}

const (
	TextMaxChars = 5_000_000
	ExcelMaxRows = 10_000
	PDFMaxPages  = 500
)

// EstimateTokens returns a rough token count estimate (4 chars per token).
func EstimateTokens(text string) int {
	return len(text) / 4
}
