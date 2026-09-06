package chat

import (
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/vector"
)

// buildChatSourcesAndContext renders retrieved chunks into the prompt context
// string and the parallel []ChatSource list returned to the client. Extracted
// from the five orchestrator paths that previously inlined identical loops.
// The ingestion-time ContextualPrefix is surfaced into the annotation (empty
// for non-enriched chunks).
func buildChatSourcesAndContext(chunks []vector.SearchChunk) ([]ChatSource, string) {
	var ctxParts []string
	sources := make([]ChatSource, len(chunks))
	for i, c := range chunks {
		idx := i + 1
		annotation, pages := renderChunkAnnotation(idx, c)
		ctxParts = append(ctxParts, annotation+"\n"+c.Content)

		sources[i] = ChatSource{
			Index:     idx,
			FileName:  c.FileName,
			FileID:    c.FileID,
			Content:   c.Content,
			Score:     c.Score,
			Pages:     pages,
			ChunkID:   c.ID,
			NodeKind:  c.NodeKind,
			TreeLevel: c.TreeLevel,
		}
	}
	return sources, strings.Join(ctxParts, chunkBlockSeparator)
}

// chunkBlockSeparator joins rendered chunk blocks in the prompt CONTEXT
// section. Shared with the long-context map stage so a per-group render is
// byte-identical to the corresponding slice of the full context text.
const chunkBlockSeparator = "\n\n---\n\n"

// renderChunkAnnotation builds the `[N] [Source: file, p. X]` header line for
// one chunk (plus the optional ingestion-time `Context:` line) and returns the
// page list it derived, so callers that also need the pages do not parse the
// metadata twice. idx is 1-based and is the citation number the answer LLM is
// expected to use.
func renderChunkAnnotation(idx int, c vector.SearchChunk) (string, []int) {
	annotation, pages := renderChunkHeaderLine(idx, c)
	if p := strings.TrimSpace(c.ContextualPrefix); p != "" {
		annotation += "\nContext: " + p
	}
	return annotation, pages
}

// renderChunkHeaderLine is renderChunkAnnotation without the optional
// ingestion-time `Context:` line, i.e. guaranteed to be exactly ONE line.
// Used where the caller renders a list of sources with no bodies (the
// long-context map-reduce SOURCES block), where a multi-line entry would
// make the list unreadable and let an enrichment prefix masquerade as a
// separate source line.
func renderChunkHeaderLine(idx int, c vector.SearchChunk) (string, []int) {
	pages := pagesFromMetadata(c.Metadata)

	pageAnnotation := ""
	if len(pages) > 0 {
		if len(pages) == 1 {
			pageAnnotation = fmt.Sprintf(", p. %d", pages[0])
		} else {
			pageAnnotation = fmt.Sprintf(", p. %d-%d", pages[0], pages[len(pages)-1])
		}
	}
	return renderSourceHeader(idx, c.FileName, pageAnnotation, c.NodeKind, c.TreeLevel), pages
}
