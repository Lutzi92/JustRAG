package parser

import (
	"context"
	"io"
	"os"
	"strings"
)

// TextParser handles plain text and markdown files, and acts as a catch-all
// parser of last resort for any file type not handled by a more specific parser.
type TextParser struct{}

func (p *TextParser) Name() string { return "TextParser" }

// CanParse returns true for any text/* MIME type, a wide list of common text
// extensions, and unconditionally as a catch-all.
func (p *TextParser) CanParse(mimeType, fileName string) bool {
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	textExts := []string{
		".txt", ".md", ".markdown", ".mdx",
		".json", ".xml", ".html", ".htm",
		".log", ".yaml", ".yml",
		".ini", ".cfg", ".conf", ".toml", ".env",
		".sh", ".bash", ".zsh", ".fish",
		".ps1", ".bat", ".cmd",
	}
	for _, ext := range textExts {
		if strings.HasSuffix(fileName, ext) {
			return true
		}
	}
	// Catch-all: accept anything as last resort.
	return true
}

// Parse reads the file at pctx.FilePath and returns its content as text.
// Reads at most TextMaxChars bytes so a huge file (URL ingest and crawler
// paths allow up to 100 MB on disk) never gets buffered whole just to be
// truncated — same pattern as the PDF parser's stdout LimitReader.
func (p *TextParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	f, err := os.Open(pctx.FilePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(TextMaxChars)))
	if err != nil {
		return nil, err
	}
	return &ParseResult{Text: string(data)}, nil
}
