package vector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
// input returns "" — callers should skip empty hashes (no DB lookup, no insert).
func HashContent(text string) string {
	normalized := NormalizeContent(text)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// GetExistingChunkHashes returns the subset of input hashes that already
// exist in the chunk table for the given KB. Empty hashes in the input are
// silently skipped. When the input is empty after filtering, returns an
// empty map and nil error (no DB round-trip).
func (s *ChunkService) GetExistingChunkHashes(ctx context.Context, kbID string, dimensions int, hashes []string) (map[string]struct{}, error) {
	out := make(map[string]struct{})

	seen := make(map[string]struct{}, len(hashes))
	hashSlice := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		hashSlice = append(hashSlice, h)
	}
	if len(hashSlice) == 0 {
		return out, nil
	}

	table := GetVectorTableName(dimensions)
	q := fmt.Sprintf(
		`SELECT DISTINCT content_hash FROM "%s" WHERE kb_id = $1::uuid AND content_hash = ANY($2::text[])`,
		table,
	)

	rows, err := s.vectorDB.Query(ctx, q, kbID, hashSlice)
	if err != nil {
		return nil, fmt.Errorf("GetExistingChunkHashes query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("GetExistingChunkHashes scan: %w", err)
		}
		out[h] = struct{}{}
	}
	return out, rows.Err()
}
