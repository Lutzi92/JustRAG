package splitter

import (
	"strings"
	"testing"
)

// FuzzSplit verifies the splitter never panics on arbitrary input and always
// produces chunks that are bounded in length.
func FuzzSplit(f *testing.F) {
	// Seed corpus.
	f.Add("Hello world. This is a simple test.")
	f.Add("# Heading\n\nParagraph one.\n\nParagraph two.")
	f.Add("")
	f.Add("\n\n\n\n\n")
	f.Add("a")
	f.Add(strings.Repeat("word ", 2000))                   // large realistic input
	f.Add("これはテストです。日本語のテキストを分割できますか。")                    // CJK only — no ASCII word boundaries
	f.Add(strings.Repeat("token ", 512))                   // ~ChunkSize tokens (boundary)
	f.Add(strings.Repeat("a", 4096))                       // single very long word, no split point
	f.Add("valid \xff\xfe mixed \xc3\xa4 bytes \x00 more") // mixed valid + invalid UTF-8

	cfg := DefaultConfig()

	f.Fuzz(func(t *testing.T, input string) {
		chunks := Split(input, cfg)

		// Every chunk must be non-empty.
		for i, c := range chunks {
			if c == "" {
				t.Errorf("chunk %d is empty", i)
			}
		}

		// Token count of each chunk should not wildly exceed ChunkSize.
		// Use the same tiktoken-backed counter the production splitter uses
		// — measuring with len/4 here would defeat the purpose of the
		// invariant, since the chunker no longer makes decisions on bytes.
		maxTokens := cfg.ChunkSize * 2
		for i, c := range chunks {
			if tokens := CountTokens(c); tokens > maxTokens {
				t.Errorf("chunk %d has %d tokens, max expected %d", i, tokens, maxTokens)
			}
		}
	})
}
