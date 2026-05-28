package splitter

import (
	"strings"
)

// Config holds parameters for text splitting.
type Config struct {
	ChunkSize    int      // target size in tokens (default 512)
	ChunkOverlap int      // overlap in tokens (default 100)
	Separators   []string // separator hierarchy
}

// DefaultSeparators splits on paragraphs, lines, words, then characters.
var DefaultSeparators = []string{"\n\n", "\n", " ", ""}

// MarkdownSeparators respects markdown heading hierarchy before falling back to defaults.
var MarkdownSeparators = []string{"\n# ", "\n## ", "\n### ", "\n#### ", "\n\n", "\n", " ", ""}

// CodeSeparators respects common code structure boundaries.
var CodeSeparators = []string{"\nfunction ", "\nasync function ", "\nclass ", "\ndef ", "\nfn ", "\n\n", "\n", " ", ""}

// DefaultConfig returns a Config with sensible defaults for plain text.
func DefaultConfig() Config {
	return Config{
		ChunkSize:    512,
		ChunkOverlap: 100,
		Separators:   DefaultSeparators,
	}
}

// MarkdownConfig returns a Config tuned for markdown documents.
func MarkdownConfig() Config {
	return Config{
		ChunkSize:    512,
		ChunkOverlap: 100,
		Separators:   MarkdownSeparators,
	}
}

// estimateTokens returns token count using tiktoken (cl100k_base) when available,
// falling back to a rough 4-chars-per-token estimate otherwise.
func estimateTokens(text string) int {
	return CountTokens(text)
}

// Split splits text into chunks according to cfg. Returns a single-element
// slice when the text fits in one chunk, and an empty slice for empty input.
func Split(text string, cfg Config) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if estimateTokens(text) <= cfg.ChunkSize {
		return []string{text}
	}
	return splitRecursive(text, cfg.Separators, cfg.ChunkSize, cfg.ChunkOverlap)
}

// splitRecursive implements the recursive character text split algorithm.
func splitRecursive(text string, separators []string, chunkSize, chunkOverlap int) []string {
	if t := strings.TrimSpace(text); t == "" {
		return nil
	}
	if estimateTokens(text) <= chunkSize {
		if t := strings.TrimSpace(text); t != "" {
			return []string{t}
		}
		return nil
	}

	// Find the first separator in the hierarchy that actually splits the text.
	var sep string
	var parts []string
	foundSep := false

	for _, s := range separators {
		if s == "" {
			// Character-level split as last resort.
			return splitByChars(text, chunkSize, chunkOverlap)
		}
		p := strings.Split(text, s)
		if len(p) > 1 {
			sep = s
			parts = p
			foundSep = true
			break
		}
	}

	if !foundSep {
		// No separator produced a split; try remaining separators or char-split.
		if len(separators) > 1 {
			return splitRecursive(text, separators[1:], chunkSize, chunkOverlap)
		}
		return splitByChars(text, chunkSize, chunkOverlap)
	}

	var nextSeparators []string
	// Determine the remaining separators to use for recursive calls on oversized splits.
	for i, s := range separators {
		if s == sep {
			nextSeparators = separators[i+1:]
			break
		}
	}

	return mergeSplits(parts, sep, chunkSize, chunkOverlap, nextSeparators)
}

// mergeSplits merges a list of split pieces back into chunks, respecting
// chunkSize (in tokens) and inserting chunkOverlap tokens at the start of
// each new chunk for context continuity.
func mergeSplits(parts []string, sep string, chunkSize, chunkOverlap int, nextSeparators []string) []string {
	chunks := make([]string, 0, len(parts))
	var current strings.Builder
	currentTokens := 0

	// sep is constant across the loop; compute its token cost once.
	sepTokenCost := 0
	if sep != "" {
		sepTokenCost = estimateTokens(sep)
	}

	flush := func() {
		if s := strings.TrimSpace(current.String()); s != "" {
			chunks = append(chunks, s)
		}
	}

	for _, part := range parts {
		// Ignore empty parts produced by leading/trailing separators.
		if strings.TrimSpace(part) == "" {
			continue
		}

		partTokens := estimateTokens(part)

		// If this single part is too large on its own, recursively split it.
		if partTokens > chunkSize {
			// First flush whatever we have accumulated.
			flush()
			current.Reset()
			currentTokens = 0

			var sub []string
			if len(nextSeparators) > 0 {
				sub = splitRecursive(part, nextSeparators, chunkSize, chunkOverlap)
			} else {
				sub = splitByChars(part, chunkSize, chunkOverlap)
			}
			chunks = append(chunks, sub...)
			continue
		}

		// Separator token cost (only added when current is non-empty).
		sepTokens := 0
		if current.Len() > 0 {
			sepTokens = sepTokenCost
		}

		// Would adding this part exceed the chunk size?
		if currentTokens+sepTokens+partTokens > chunkSize && currentTokens > 0 {
			flush()

			// Start the new chunk with an overlap tail from the previous chunk.
			overlapText := tailByTokens(current.String(), chunkOverlap)
			current.Reset()
			current.WriteString(overlapText)
			currentTokens = estimateTokens(overlapText)
		}

		// Append separator + part.
		if current.Len() > 0 && sep != "" {
			current.WriteString(sep)
			currentTokens += sepTokenCost
		}
		current.WriteString(part)
		currentTokens += partTokens
	}

	flush()
	return chunks
}

// tailByTokens returns the last n tokens (by estimated count) of text,
// preserving word boundaries where possible.
func tailByTokens(text string, n int) string {
	if n <= 0 {
		return ""
	}
	targetChars := n * 4
	if targetChars >= len(text) {
		return text
	}
	tail := text[len(text)-targetChars:]
	// Advance to the next space boundary to avoid cutting mid-word.
	if idx := strings.Index(tail, " "); idx >= 0 {
		tail = tail[idx+1:]
	}
	return tail
}

// splitByChars splits text into character-level chunks of chunkSize tokens
// with chunkOverlap token overlap. This is the last-resort splitter.
func splitByChars(text string, chunkSize, chunkOverlap int) []string {
	chunkChars := chunkSize * 4
	overlapChars := chunkOverlap * 4

	if chunkChars <= 0 {
		return []string{text}
	}

	estChunks := (len(text) + chunkChars - 1) / chunkChars
	chunks := make([]string, 0, estChunks)
	for start := 0; start < len(text); {
		end := start + chunkChars
		if end > len(text) {
			end = len(text)
		}
		chunk := strings.TrimSpace(text[start:end])
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end == len(text) {
			break
		}
		// Advance by chunkSize minus overlap.
		advance := chunkChars - overlapChars
		if advance <= 0 {
			advance = chunkChars
		}
		start += advance
	}
	return chunks
}
