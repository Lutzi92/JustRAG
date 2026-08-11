package parser

import "strings"

// PagesForChunk resolves which source pages a chunk covers, for parsers that
// report page boundaries as offsets into one continuous text (PageSpans)
// rather than as pre-split per-page text.
//
// chunkContent is located in fullText by a monotonic forward scan starting at
// searchFrom — the same technique SectionsForChunk uses — so repeated
// boilerplate resolves to the occurrence that matches the caller's position in
// the document rather than always the first one. The returned nextOffset is
// meant to be threaded into the following call.
//
// Returns nil pages when there are no spans or the chunk cannot be located;
// callers then write no page metadata at all rather than guessing.
func PagesForChunk(spans []PageSpan, fullText, chunkContent string, searchFrom int) (pages []int, nextOffset int) {
	if len(spans) == 0 || fullText == "" || chunkContent == "" {
		return nil, searchFrom
	}

	pos := -1
	if searchFrom > 0 && searchFrom < len(fullText) {
		if p := strings.Index(fullText[searchFrom:], chunkContent); p >= 0 {
			pos = searchFrom + p
		}
	}
	if pos < 0 {
		// Either the first call, a stale offset, or the chunk sits earlier in
		// the document (splitter overlap) — scan from the beginning.
		p := strings.Index(fullText, chunkContent)
		if p < 0 {
			return nil, searchFrom
		}
		pos = p
	}

	end := pos + len(chunkContent)
	for i, s := range spans {
		spanEnd := len(fullText)
		if i+1 < len(spans) {
			spanEnd = spans[i+1].Start
		}
		// Overlap test between [pos,end) and [s.Start,spanEnd).
		if s.Start < end && spanEnd > pos {
			if len(pages) == 0 || pages[len(pages)-1] != s.Page {
				pages = append(pages, s.Page)
			}
		}
	}
	return pages, pos + 1
}
