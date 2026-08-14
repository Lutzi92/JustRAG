package vector

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
)

// NeighborFetcher retrieves neighboring chunks from the same file by their
// chunkIndex values.
type NeighborFetcher interface {
	FetchChunksByIndices(ctx context.Context, tableName, kbID, fileID string, indices []int) ([]rawRow, error)
}

// ExpandNeighbors enriches each SearchChunk by fetching ±windowSize neighboring
// chunks (by chunkIndex) from the same file and merging their content. Page
// metadata is left alone: the citation names the page the retriever actually
// matched, not the span of the surrounding context. If windowSize <= 0 or
// fetcher is nil the chunks are returned unchanged (fail-open).
func ExpandNeighbors(
	ctx context.Context,
	chunks []SearchChunk,
	windowSize int,
	fetcher NeighborFetcher,
	tableName, kbID string,
) []SearchChunk {
	if windowSize <= 0 || fetcher == nil || len(chunks) == 0 {
		return chunks
	}

	// Build map of already-matched chunks keyed by (fileID, chunkIndex).
	type fileIdx struct {
		fileID string
		index  int
	}
	matched := make(map[fileIdx]SearchChunk)
	// Group chunks that have chunkIndex by fileID.
	byFile := make(map[string][]int) // fileID -> list of matched chunk positions in `chunks`

	for i, c := range chunks {
		// Phase 3 §D: parent-child rows carry their neighborhood in the
		// parent that's already swapped in upstream. Skip them so the
		// neighbor expansion doesn't re-fetch and stitch unrelated child
		// chunks from the same file.
		if c.ParentChunkID != nil && *c.ParentChunkID != "" {
			continue
		}
		ci, ok := extractInt(c.Metadata, "chunkIndex")
		if !ok {
			continue
		}
		key := fileIdx{c.FileID, ci}
		if existing, dup := matched[key]; dup {
			slog.Warn("neighbor expansion: duplicate (fileID, chunkIndex), keeping first",
				"fileID", c.FileID, "chunkIndex", ci,
				"keptID", existing.ID, "skippedID", c.ID)
			continue
		}
		matched[key] = c
		byFile[c.FileID] = append(byFile[c.FileID], i)
	}

	if len(byFile) == 0 {
		return chunks
	}

	// For each file, determine needed neighbor indices and fetch them.
	// neighborContent maps (fileID, chunkIndex) -> rawRow content.
	type part struct {
		index   int
		content string
	}
	neighborParts := make(map[fileIdx]part)

	for fileID, positions := range byFile {
		// Collect all chunkIndices for this file.
		var indices []int
		for _, pos := range positions {
			ci, _ := extractInt(chunks[pos].Metadata, "chunkIndex")
			indices = append(indices, ci)
		}

		// Determine total chunks if available.
		totalChunks := -1
		for _, pos := range positions {
			if tc, ok := extractInt(chunks[pos].Metadata, "totalChunks"); ok {
				totalChunks = tc
				break
			}
		}

		// Build set of needed neighbor indices.
		needed := make(map[int]struct{})
		for _, ci := range indices {
			lo := ci - windowSize
			if lo < 0 {
				lo = 0
			}
			hi := ci + windowSize
			if totalChunks > 0 && hi >= totalChunks {
				hi = totalChunks - 1
			}
			for idx := lo; idx <= hi; idx++ {
				// Skip indices we already have from matched chunks.
				if _, ok := matched[fileIdx{fileID, idx}]; ok {
					continue
				}
				needed[idx] = struct{}{}
			}
		}

		if len(needed) == 0 {
			continue
		}

		// Convert to sorted slice.
		fetchIndices := make([]int, 0, len(needed))
		for idx := range needed {
			fetchIndices = append(fetchIndices, idx)
		}
		slices.Sort(fetchIndices)

		rows, err := fetcher.FetchChunksByIndices(ctx, tableName, kbID, fileID, fetchIndices)
		if err != nil {
			slog.Warn("neighbor expansion: fetch failed, skipping file",
				"fileID", fileID, "error", err)
			continue
		}

		for _, r := range rows {
			var meta map[string]any
			if len(r.Metadata) > 0 {
				// Parse metadata to get chunkIndex.
				meta = parseMetadataJSON(r.Metadata)
			}
			ci, ok := extractInt(meta, "chunkIndex")
			if !ok {
				continue
			}
			neighborParts[fileIdx{fileID, ci}] = part{
				index:   ci,
				content: r.Content,
			}
		}
	}

	// Build expanded chunks.
	result := make([]SearchChunk, len(chunks))
	for i, c := range chunks {
		// Phase 3 §D: parent-child rows skip neighbor expansion (the
		// parent already provides the neighborhood). Pass through unchanged.
		if c.ParentChunkID != nil && *c.ParentChunkID != "" {
			result[i] = c
			continue
		}
		ci, ok := extractInt(c.Metadata, "chunkIndex")
		if !ok {
			result[i] = c
			continue
		}

		// Determine range of indices to merge.
		lo := ci - windowSize
		if lo < 0 {
			lo = 0
		}
		hi := ci + windowSize
		if tc, ok := extractInt(c.Metadata, "totalChunks"); ok && hi >= tc {
			hi = tc - 1
		}

		// Collect parts in order.
		var parts []string
		for idx := lo; idx <= hi; idx++ {
			fi := fileIdx{c.FileID, idx}
			if idx == ci {
				// Use the matched chunk itself.
				parts = append(parts, c.Content)
			} else if mc, ok := matched[fi]; ok {
				// Another matched chunk from the same file.
				parts = append(parts, mc.Content)
			} else if np, ok := neighborParts[fi]; ok {
				// Fetched neighbor.
				parts = append(parts, np.content)
			}
			// If not found, skip (don't leave gaps).
		}

		expanded := c // copy
		expanded.Content = strings.Join(parts, "\n\n")

		// Page metadata deliberately stays that of the matched chunk. The
		// neighbours are reading context for the answer model, not evidence
		// the retriever found — merging their pages in turned a hit on one
		// page into a citation spanning the whole ±windowSize neighbourhood
		// (at the default window of 3, up to seven chunks), which is what
		// made citations read "S. 5-11" for a passage that lives on page 8.

		result[i] = expanded
	}

	return result
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractInt extracts an integer from metadata, handling float64/int/int64 types
// that result from JSON unmarshalling.
func extractInt(meta map[string]any, key string) (int, bool) {
	v, ok := meta[key]
	if !ok {
		return 0, false
	}
	return toInt(v)
}

// toInt converts a value to int, supporting float64, int, and int64.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

// extractPages extracts page numbers from metadata "page" (single) and "pages" (slice).
func extractPages(meta map[string]any) []int {
	var pages []int
	if v, ok := meta["page"]; ok {
		if n, ok := toInt(v); ok {
			pages = append(pages, n)
		}
	}
	if v, ok := meta["pages"]; ok {
		switch p := v.(type) {
		case []any:
			for _, item := range p {
				if n, ok := toInt(item); ok {
					pages = append(pages, n)
				}
			}
		case []int:
			pages = append(pages, p...)
		}
	}
	return pages
}

// parseMetadataJSON is a tiny wrapper around json.Unmarshal for metadata bytes.
func parseMetadataJSON(raw json.RawMessage) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}
