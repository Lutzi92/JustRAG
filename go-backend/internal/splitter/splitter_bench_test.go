package splitter

import (
	"strings"
	"testing"
)

func BenchmarkSplitDefault(b *testing.B) {
	// ~10 KB of text, typical document chunk.
	text := strings.Repeat("This is a sample sentence for testing the text splitter. ", 200)
	cfg := DefaultConfig()

	b.ReportAllocs()
	for b.Loop() {
		Split(text, cfg)
	}
}

func BenchmarkSplitMarkdown(b *testing.B) {
	text := strings.Repeat("# Heading\n\nSome paragraph text here.\n\n- Item one\n- Item two\n\n", 200)
	cfg := MarkdownConfig()

	b.ReportAllocs()
	for b.Loop() {
		Split(text, cfg)
	}
}

func BenchmarkSplitLargeDocument(b *testing.B) {
	// ~100 KB — large document.
	text := strings.Repeat("A longer paragraph of text that simulates a real document. ", 2000)
	cfg := DefaultConfig()

	b.ReportAllocs()
	for b.Loop() {
		Split(text, cfg)
	}
}
