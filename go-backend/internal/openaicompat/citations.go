package openaicompat

import (
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/chat"
)

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

// fileCitation mirrors OpenAI's file_citation annotation payload. Index is a
// CHARACTER offset into the message content, per OpenAI's definition — not a
// byte offset (see buildAnnotations).
type fileCitation struct {
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	Index    int    `json:"index"`
}

// annotation is one entry of an assistant message's `annotations` array. Only
// file_citation is emitted: KB chunks are files, never URLs.
type annotation struct {
	Type         string       `json:"type"`
	FileCitation fileCitation `json:"file_citation"`
}

// citation is one retrieved chunk, shaped like Azure OpenAI "On Your Data"
// so OpenWebUI-family clients recognise it without bespoke handling.
type citation struct {
	Content  string  `json:"content"`
	Title    string  `json:"title"`
	Filepath string  `json:"filepath"`
	FileID   string  `json:"file_id"`
	ChunkID  string  `json:"chunk_id,omitempty"`
	Score    float64 `json:"score"`
	Pages    []int   `json:"pages,omitempty"`
}

// messageContext carries the retrieved chunk bodies alongside the answer.
type messageContext struct {
	Citations []citation `json:"citations"`
}

// ---------------------------------------------------------------------------
// Builders
// ---------------------------------------------------------------------------

// buildAnnotations converts the [n] citation markers in answer into
// OpenAI-shaped file_citation annotations, in document order. A multi-cite
// marker ("[1, 2]") yields one annotation per referenced source, all sharing
// the marker's index.
//
// Two deliberate behaviours:
//
//   - Index is a rune offset, because OpenAI defines annotations[].index in
//     characters. Byte and rune offsets diverge on the first umlaut, and this
//     deployment answers mostly in German.
//   - Markers referencing a source that was never retrieved are dropped. This
//     endpoint runs no citation validator, so the model can emit "[9]" against
//     five sources; a dangling annotation would be worse than none.
func buildAnnotations(answer string, sources []chat.ChatSource) []annotation {
	if len(sources) == 0 {
		return nil
	}
	spans := chat.ExtractCitationSpans(answer)
	if len(spans) == 0 {
		return nil
	}

	byIndex := make(map[int]chat.ChatSource, len(sources))
	for _, s := range sources {
		byIndex[s.Index] = s
	}

	out := make([]annotation, 0, len(spans))
	for _, span := range spans {
		// Marker offsets are byte offsets; OpenAI wants characters.
		runeIdx := utf8.RuneCountInString(answer[:span.Start])
		for _, n := range span.Numbers {
			src, ok := byIndex[n]
			if !ok {
				continue
			}
			out = append(out, annotation{
				Type: "file_citation",
				FileCitation: fileCitation{
					FileID:   src.FileID,
					Filename: src.FileName,
					Index:    runeIdx,
				},
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildCitations maps the retrieved sources onto the Azure-shaped citation
// list. Returns nil for an empty source list so the field stays absent rather
// than serialising as an empty array.
func buildCitations(sources []chat.ChatSource) []citation {
	if len(sources) == 0 {
		return nil
	}
	out := make([]citation, 0, len(sources))
	for _, s := range sources {
		out = append(out, citation{
			Content:  s.Content,
			Title:    s.FileName,
			Filepath: s.FileName,
			FileID:   s.FileID,
			ChunkID:  s.ChunkID,
			Score:    s.Score,
			Pages:    s.Pages,
		})
	}
	return out
}
