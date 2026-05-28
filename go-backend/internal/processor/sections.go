package processor

import (
	"strings"
)

// headingEntry records a single markdown heading found in the text.
type headingEntry struct {
	offset int    // byte offset in the full text where the heading line starts
	depth  int    // heading level: 1 for #, 2 for ##, etc.
	title  string // heading text without the leading # characters
}

// SectionIndex stores all markdown headings found in a document, in order.
type SectionIndex struct {
	headings []headingEntry
}

// ExtractMarkdownSections scans text for markdown headings (#, ##, ### etc.)
// and returns a SectionIndex that can be queried for breadcrumbs at any offset.
func ExtractMarkdownSections(text string) *SectionIndex {
	idx := &SectionIndex{}
	offset := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			depth := 0
			for _, ch := range trimmed {
				if ch == '#' {
					depth++
				} else {
					break
				}
			}
			if depth > 0 && depth <= 6 {
				title := strings.TrimSpace(trimmed[depth:])
				idx.headings = append(idx.headings, headingEntry{
					offset: offset,
					depth:  depth,
					title:  title,
				})
			}
		}
		offset += len(line) + 1 // +1 for the newline
	}
	return idx
}

// At returns the breadcrumb hierarchy (e.g. ["Chapter", "Section", "Subsection"])
// at the given byte offset. It walks headings up to offset and maintains a stack
// where shallower headings pop off deeper/equal entries.
func (s *SectionIndex) At(offset int) []string {
	if s == nil || len(s.headings) == 0 {
		return nil
	}

	// stack stores headings as (depth, title) pairs in order.
	type entry struct {
		depth int
		title string
	}
	var stack []entry

	for _, h := range s.headings {
		if h.offset > offset {
			break
		}
		// Pop entries that are at equal or deeper level.
		for len(stack) > 0 && stack[len(stack)-1].depth >= h.depth {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, entry{depth: h.depth, title: h.title})
	}

	if len(stack) == 0 {
		return nil
	}
	result := make([]string, len(stack))
	for i, e := range stack {
		result[i] = e.title
	}
	return result
}

// SectionsForChunk finds the chunk's position in the full text and returns
// the heading breadcrumb hierarchy at that position. searchFrom is a byte
// offset hint: the search for chunkContent starts at this position so that
// duplicated content maps to the correct section. Pass 0 to search from the
// beginning. The function returns the updated offset (position found + 1)
// that callers should pass for the next chunk to enable sequential scanning.
func SectionsForChunk(index *SectionIndex, fullText, chunkContent string, searchFrom int) (sections []string, nextOffset int) {
	if index == nil || fullText == "" || chunkContent == "" {
		return nil, searchFrom
	}
	if searchFrom < 0 || searchFrom >= len(fullText) {
		return nil, searchFrom
	}
	pos := strings.Index(fullText[searchFrom:], chunkContent)
	if pos < 0 {
		// Fall back to searching from the beginning.
		pos = strings.Index(fullText, chunkContent)
		if pos < 0 {
			return nil, searchFrom
		}
		return index.At(pos), pos + 1
	}
	absPos := searchFrom + pos
	return index.At(absPos), absPos + 1
}
