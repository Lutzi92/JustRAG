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
	Pages []PageText
	// PageSpans maps byte offsets in Text to the page they originate from,
	// for parsers that produce one continuous document (e.g. Docling
	// markdown) but still know where each page starts. Sorted ascending by
	// Start, first entry starting at 0. Only consulted when Pages is empty;
	// the processor then splits Text as a whole and derives each chunk's
	// page numbers from its offset. Empty when the parser has no page
	// information at all — chunks then carry no page metadata, which is
	// deliberately better than a fabricated page 1.
	PageSpans  []PageSpan
	IsMarkdown bool // true when Text/Pages content is markdown (headings, lists, etc.)
}

// PageText holds the extracted text for a single page.
type PageText struct {
	PageNumber int // 1-based
	Text       string
}

// PageSpan marks the byte offset in ParseResult.Text at which a page's
// content begins. The span runs until the next PageSpan.Start (or the end of
// the text for the last entry).
type PageSpan struct {
	Start int // byte offset into ParseResult.Text
	Page  int // 1-based page number
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
