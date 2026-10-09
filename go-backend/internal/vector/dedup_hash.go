package vector

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// NormalizeContent returns a canonical form of text suitable for content-based
// duplicate detection: lowercased and with all runs of whitespace collapsed
// into single spaces. Empty / whitespace-only input returns "".
//
// Implemented as a single rune-level pass to avoid the intermediate
// allocations from ToLower → Fields → Join.
func NormalizeContent(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	needSpace := false
	seenContent := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if seenContent {
				needSpace = true
			}
			continue
		}
		if needSpace {
			b.WriteByte(' ')
			needSpace = false
		}
		seenContent = true
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// HashContent returns a SHA256 hex digest of NormalizeContent(text). Empty
// input returns "" — an empty hash is stored as-is (the chunk is still inserted)
// and never collapses with another chunk in the in-batch dedup.
func HashContent(text string) string {
	normalized := NormalizeContent(text)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
