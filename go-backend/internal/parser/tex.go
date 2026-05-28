package parser

import (
	"context"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// TexParser handles LaTeX (.tex) files, stripping comments and comment
// environments before returning the remaining content.
type TexParser struct{}

func (p *TexParser) Name() string { return "tex" }

// CanParse returns true for application/x-tex, text/x-tex MIME types or .tex extension.
func (p *TexParser) CanParse(mimeType, fileName string) bool {
	if mimeType == "application/x-tex" || mimeType == "text/x-tex" {
		return true
	}
	return strings.HasSuffix(strings.ToLower(fileName), ".tex")
}

// reCommentBlock matches \begin{comment}...\end{comment} blocks (dotall).
var reCommentBlock = regexp.MustCompile(`(?s)\\begin\{comment\}.*?\\end\{comment\}`)

// Parse reads a .tex file, removes comment environments and %-prefixed comment
// lines, then returns the cleaned text truncated to TextMaxChars.
func (p *TexParser) Parse(_ context.Context, pctx ParseContext) (*ParseResult, error) {
	data, err := os.ReadFile(pctx.FilePath)
	if err != nil {
		return nil, err
	}

	text := string(data)

	// Remove \begin{comment}...\end{comment} blocks.
	text = reCommentBlock.ReplaceAllString(text, "")

	// Strip %-prefixed comment lines and inline trailing comments.
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if idx := strings.Index(line, "%"); idx >= 0 {
			line = line[:idx]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		kept = append(kept, line)
	}
	text = strings.Join(kept, "\n")

	if len(text) > TextMaxChars {
		// Truncate at a valid UTF-8 rune boundary to avoid splitting
		// multi-byte characters. Walk back from the cut point until we
		// find the start of a complete rune.
		cut := TextMaxChars
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}

	return &ParseResult{Text: text}, nil
}
