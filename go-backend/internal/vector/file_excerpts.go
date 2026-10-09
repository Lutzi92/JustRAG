package vector

import (
	"context"
	"fmt"
	"sort"
)

// fileExcerptSQLTemplate picks one row per file, deliberately:
//
//  1. the RAPTOR root — node_kind='summary' at the file's highest
//     tree_level. RAPTOR trees are built per file (summary rows carry the
//     file's file_id, tree_level 1..N); the highest level is the summary of
//     the whole document. When the builder stopped at MaxLevels with several
//     summaries on the top level, the earliest-written one wins (created_at,
//     then id), so the pick is stable across calls;
//  2. otherwise the file's first leaf in document order, i.e. the lowest
//     metadata chunkIndex. created_at is NOT an order here: one ingest batch
//     writes every leaf of a file in the same transaction, so they all share
//     one now(). Rows without a numeric chunkIndex sort last.
//
// Other node kinds (community_summary rows with their synthetic file ids) are
// excluded. Served by the (kb_id, file_id, node_kind) index. %s is a table
// name from GetVectorTableName, never request data.
const fileExcerptSQLTemplate = `
	SELECT DISTINCT ON (file_id) file_id::text, left(content, $3)
	  FROM "%s"
	 WHERE kb_id = $1::uuid
	   AND file_id = ANY($2::uuid[])
	   AND node_kind IN ('summary', 'leaf')
	 ORDER BY file_id,
	          (node_kind = 'summary') DESC,
	          tree_level DESC,
	          CASE WHEN (metadata::jsonb->>'chunkIndex') ~ '^[0-9]{1,9}$'
	               THEN (metadata::jsonb->>'chunkIndex')::int END ASC NULLS LAST,
	          created_at,
	          id`

// FileExcerpts returns one excerpt of at most maxLen characters per file of
// kbID (see fileExcerptSQLTemplate for which row is chosen). Files without
// chunks are absent from the map.
//
// preferredDim is the KB's current embedding dimension
// (ai.ResolvedConfig.EmbeddingDimensions; 0 = unknown). Its table is read
// first, and the remaining chunk tables — in ascending dimension order — only
// for files it does not cover, so a KB re-embedded under a new model shows
// its current chunks rather than a stale copy left in an old table.
func (s *ChunkService) FileExcerpts(ctx context.Context, kbID string, fileIDs []string, maxLen, preferredDim int) (map[string]string, error) {
	out := make(map[string]string, len(fileIDs))
	if len(fileIDs) == 0 {
		return out, nil
	}
	dims, err := s.ListChunkTableDimensions(ctx)
	if err != nil {
		return nil, fmt.Errorf("file excerpts: %w", err)
	}
	missing := fileIDs
	for _, d := range excerptTableOrder(dims, preferredDim) {
		table := GetVectorTableName(d)
		rows, err := s.vectorDB.Query(ctx, fmt.Sprintf(fileExcerptSQLTemplate, table), kbID, missing, maxLen)
		if err != nil {
			return nil, fmt.Errorf("file excerpts from %q: %w", table, err)
		}
		for rows.Next() {
			var id, content string
			if err := rows.Scan(&id, &content); err != nil {
				rows.Close()
				return nil, fmt.Errorf("file excerpts: scan %q: %w", table, err)
			}
			out[id] = content
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("file excerpts: iterate %q: %w", table, err)
		}
		missing = missing[:0:0]
		for _, id := range fileIDs {
			if _, ok := out[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			break
		}
	}
	return out, nil
}

// excerptTableOrder returns dims de-duplicated, ascending, with preferredDim
// moved to the front when it is one of them.
func excerptTableOrder(dims []int, preferredDim int) []int {
	seen := make(map[int]bool, len(dims))
	var rest []int
	hasPreferred := false
	for _, d := range dims {
		if seen[d] {
			continue
		}
		seen[d] = true
		if d == preferredDim {
			hasPreferred = true
			continue
		}
		rest = append(rest, d)
	}
	sort.Ints(rest)
	if hasPreferred {
		return append([]int{preferredDim}, rest...)
	}
	return rest
}
