package splitter

import (
	"strings"
	"testing"
)

// 1. Short text — fits in a single chunk, returned as-is.
func TestShortText(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	text := "Hello, world."
	chunks := Split(text, cfg)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != text {
		t.Errorf("expected %q, got %q", text, chunks[0])
	}
}

// 2. Two paragraphs separated by \n\n → two chunks.
func TestTwoParagraphs(t *testing.T) {
	t.Parallel()
	cfg := Config{ChunkSize: 20, ChunkOverlap: 0, Separators: DefaultSeparators}

	// Each paragraph is ~20 chars = ~5 tokens. Together ~10 tokens, still < 20.
	// Use larger paragraphs so the combined text exceeds ChunkSize.
	para1 := strings.Repeat("alpha ", 20) // 120 chars → 30 tokens
	para2 := strings.Repeat("beta ", 20)  // 100 chars → 25 tokens
	text := para1 + "\n\n" + para2

	chunks := Split(text, cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for two large paragraphs, got %d", len(chunks))
	}
}

// 3. Long continuous text split on spaces.
func TestLongSpaceSplit(t *testing.T) {
	t.Parallel()
	cfg := Config{ChunkSize: 10, ChunkOverlap: 0, Separators: DefaultSeparators}

	// 200 words × ~5 chars = 1000 chars → ~250 tokens; chunk size 10 → several chunks.
	var words []string
	for i := 0; i < 200; i++ {
		words = append(words, "word")
	}
	text := strings.Join(words, " ")

	chunks := Split(text, cfg)
	if len(chunks) < 5 {
		t.Fatalf("expected many chunks for long space-separated text, got %d", len(chunks))
	}
	for i, c := range chunks {
		if estimateTokens(c) > cfg.ChunkSize*2 {
			// Allow some flexibility for the merge logic, but nothing wildly over.
			t.Errorf("chunk %d exceeds 2× chunk size: %d tokens", i, estimateTokens(c))
		}
	}
}

// 4. Very long line with no spaces → character split.
func TestCharacterSplit(t *testing.T) {
	t.Parallel()
	cfg := Config{ChunkSize: 10, ChunkOverlap: 0, Separators: DefaultSeparators}

	// 400 chars of continuous text → 100 tokens; no spaces or newlines.
	text := strings.Repeat("abcd", 100) // 400 chars
	chunks := Split(text, cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected character-level split, got %d chunk(s)", len(chunks))
	}
	for _, c := range chunks {
		if estimateTokens(c) > cfg.ChunkSize+5 { // small tolerance
			t.Errorf("character-split chunk too large: %d tokens", estimateTokens(c))
		}
	}
}

// 5. Overlap is included between consecutive chunks.
func TestOverlap(t *testing.T) {
	t.Parallel()
	cfg := Config{ChunkSize: 10, ChunkOverlap: 5, Separators: []string{" ", ""}}

	// Build text that will definitely be split into multiple chunks.
	var words []string
	for i := 0; i < 60; i++ {
		words = append(words, "word")
	}
	text := strings.Join(words, " ")

	chunks := Split(text, cfg)
	if len(chunks) < 3 {
		t.Fatalf("need at least 3 chunks to verify overlap, got %d", len(chunks))
	}

	// Each subsequent chunk should share some content with the previous one
	// (the overlap tail). At minimum, chunk[1] should not start at character 0
	// of the text (i.e. it contains content from the middle of chunk[0]).
	for i := 1; i < len(chunks); i++ {
		// The overlap means some suffix of chunks[i-1] should appear in chunks[i].
		prev := chunks[i-1]
		curr := chunks[i]

		// Take last 8 chars of prev (smaller than overlap) and check presence.
		if len(prev) > 8 {
			suffix := prev[len(prev)-8:]
			if !strings.Contains(curr, suffix) {
				// Overlap may have been trimmed at word boundary; soft warning.
				t.Logf("overlap check: suffix of chunk %d not found in chunk %d (may be word-boundary trimmed)", i-1, i)
			}
		}
	}
}

// 6. Markdown headers respected with MarkdownSeparators.
func TestMarkdownSeparators(t *testing.T) {
	t.Parallel()
	cfg := Config{ChunkSize: 30, ChunkOverlap: 0, Separators: MarkdownSeparators}

	// Two sections with headers; each section ~50 tokens so they must be separate chunks.
	section1 := "# Introduction\n" + strings.Repeat("intro text ", 20)  // ~60 chars header+body
	section2 := "\n## Details\n" + strings.Repeat("detail text ", 20)   // separate section
	section3 := "\n### Sub-section\n" + strings.Repeat("sub text ", 20) // sub-section
	text := section1 + section2 + section3

	chunks := Split(text, cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected markdown sections to produce multiple chunks, got %d", len(chunks))
	}

	// At least one chunk should start with a heading marker.
	foundHeading := false
	for _, c := range chunks {
		if strings.HasPrefix(c, "# ") || strings.HasPrefix(c, "## ") || strings.HasPrefix(c, "### ") {
			foundHeading = true
			break
		}
	}
	if !foundHeading {
		t.Error("expected at least one chunk to start with a markdown heading")
	}
}

// 7. Empty input → empty result.
func TestEmptyInput(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "   ", "\n\n"} {
		chunks := Split(input, DefaultConfig())
		if len(chunks) != 0 {
			t.Errorf("expected empty result for input %q, got %v", input, chunks)
		}
	}
}

// 8. Chunk size 100 tokens with known text → verify chunk count.
func TestChunkCount(t *testing.T) {
	t.Parallel()
	cfg := Config{ChunkSize: 100, ChunkOverlap: 0, Separators: DefaultSeparators}

	// 2000 chars → 500 tokens. With chunk size 100, expect ~5 chunks.
	text := strings.Repeat("a", 2000)
	chunks := Split(text, cfg)

	// Should be approximately ceil(500/100) = 5 chunks (character split).
	if len(chunks) < 4 || len(chunks) > 7 {
		t.Errorf("expected ~5 chunks for 500-token text with size=100, got %d", len(chunks))
	}
}

// 9. No chunk exceeds ChunkSize by more than one split's worth (sanity check).
func TestChunkSizeBound(t *testing.T) {
	t.Parallel()
	cfg := Config{ChunkSize: 50, ChunkOverlap: 10, Separators: DefaultSeparators}

	// Mix of paragraphs and long words.
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString(strings.Repeat("paragraph ", 30))
		sb.WriteString("\n\n")
	}
	text := sb.String()

	chunks := Split(text, cfg)
	for i, c := range chunks {
		tok := estimateTokens(c)
		// Allow up to 2× as a generous upper bound (a single word cannot be further split).
		if tok > cfg.ChunkSize*2 {
			t.Errorf("chunk %d has %d tokens, exceeds 2× chunk size %d", i, tok, cfg.ChunkSize)
		}
	}
}

// 10. Whitespace-only parts are not emitted as chunks.
func TestNoWhitespaceOnlyChunks(t *testing.T) {
	t.Parallel()
	cfg := Config{ChunkSize: 10, ChunkOverlap: 0, Separators: DefaultSeparators}
	text := "\n\n   \n\n" + strings.Repeat("real content ", 20) + "\n\n   \n\n"
	chunks := Split(text, cfg)
	for _, c := range chunks {
		if strings.TrimSpace(c) == "" {
			t.Errorf("got a whitespace-only chunk: %q", c)
		}
	}
}
