package chat

import (
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

func TestBuildChatSourcesAndContext(t *testing.T) {
	chunks := []vector.SearchChunk{
		{
			ID:               "chunk-uuid-1",
			Content:          "First chunk content.",
			ContextualPrefix: "This is about project Alpha.",
			FileID:           "file-uuid-1",
			FileName:         "doc1.pdf",
			Metadata:         map[string]any{"pages": []any{float64(3), float64(4)}},
			Score:            0.95,
			NodeKind:         "",
			TreeLevel:        0,
		},
		{
			ID:               "chunk-uuid-2",
			Content:          "Second chunk content.",
			ContextualPrefix: "",
			FileID:           "file-uuid-2",
			FileName:         "doc2.pdf",
			Metadata:         map[string]any{},
			Score:            0.80,
			NodeKind:         "",
			TreeLevel:        0,
		},
	}

	sources, contextText := buildChatSourcesAndContext(chunks)

	// len(sources) == 2
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}

	// 1-based Index
	if sources[0].Index != 1 {
		t.Errorf("sources[0].Index: want 1, got %d", sources[0].Index)
	}
	if sources[1].Index != 2 {
		t.Errorf("sources[1].Index: want 2, got %d", sources[1].Index)
	}

	// FileName mapped correctly
	if sources[0].FileName != "doc1.pdf" {
		t.Errorf("sources[0].FileName: want doc1.pdf, got %q", sources[0].FileName)
	}
	if sources[1].FileName != "doc2.pdf" {
		t.Errorf("sources[1].FileName: want doc2.pdf, got %q", sources[1].FileName)
	}

	// ChunkID == c.ID
	if sources[0].ChunkID != "chunk-uuid-1" {
		t.Errorf("sources[0].ChunkID: want chunk-uuid-1, got %q", sources[0].ChunkID)
	}
	if sources[1].ChunkID != "chunk-uuid-2" {
		t.Errorf("sources[1].ChunkID: want chunk-uuid-2, got %q", sources[1].ChunkID)
	}

	// FileID mapped correctly
	if sources[0].FileID != "file-uuid-1" {
		t.Errorf("sources[0].FileID: want file-uuid-1, got %q", sources[0].FileID)
	}
	if sources[1].FileID != "file-uuid-2" {
		t.Errorf("sources[1].FileID: want file-uuid-2, got %q", sources[1].FileID)
	}

	// Pages extracted from metadata
	if len(sources[0].Pages) != 2 || sources[0].Pages[0] != 3 || sources[0].Pages[1] != 4 {
		t.Errorf("sources[0].Pages: want [3 4], got %v", sources[0].Pages)
	}
	if len(sources[1].Pages) != 0 {
		t.Errorf("sources[1].Pages: want [], got %v", sources[1].Pages)
	}

	// Score mapped correctly
	if sources[0].Score != 0.95 {
		t.Errorf("sources[0].Score: want 0.95, got %f", sources[0].Score)
	}

	// "Context: <prefix>" appears exactly once (only for chunk[0])
	contextLineCount := strings.Count(contextText, "\nContext: ")
	if contextLineCount != 1 {
		t.Errorf("expected exactly 1 'Context: ' line, got %d; contextText=%q", contextLineCount, contextText)
	}
	if !strings.Contains(contextText, "\nContext: This is about project Alpha.") {
		t.Errorf("contextText missing expected context prefix line; got: %q", contextText)
	}

	// The --- separator is present exactly once (between 2 chunks)
	sepCount := strings.Count(contextText, "\n\n---\n\n")
	if sepCount != 1 {
		t.Errorf("expected 1 separator, got %d", sepCount)
	}

	// Page annotation appears in context for chunk[0] (pages 3-4 → "p. 3-4")
	if !strings.Contains(contextText, "p. 3-4") {
		t.Errorf("contextText missing page annotation 'p. 3-4'; got: %q", contextText)
	}

	// Content of both chunks appears in contextText
	if !strings.Contains(contextText, "First chunk content.") {
		t.Errorf("contextText missing first chunk content")
	}
	if !strings.Contains(contextText, "Second chunk content.") {
		t.Errorf("contextText missing second chunk content")
	}
}
