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
		pages := pagesFromMetadata(c.Metadata)

		pageAnnotation := ""
		if len(pages) > 0 {
			if len(pages) == 1 {
				pageAnnotation = fmt.Sprintf(", p. %d", pages[0])
			} else {
				pageAnnotation = fmt.Sprintf(", p. %d-%d", pages[0], pages[len(pages)-1])
			}
		}

		annotation := renderSourceHeader(idx, c.FileName, pageAnnotation, c.NodeKind, c.TreeLevel)
		if p := strings.TrimSpace(c.ContextualPrefix); p != "" {
			annotation += "\nContext: " + p
		}
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
	return sources, strings.Join(ctxParts, "\n\n---\n\n")
}
