package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ParentChunkInput holds the data needed to insert a parent chunk row.
// Parents have content but no embedding and no tsvector — they're never
// searched directly, only swapped in for their children at retrieval time.
type ParentChunkInput struct {
	KbID             string
	FileID           string
	Content          string
	ContextualPrefix string         // optional; populated by enrichment after insert
	Metadata         map[string]any // optional; serialized as JSONB
}

// ParentChunkRow is the read-side shape for LookupParentContent. Only the
// fields the search pipeline needs at swap time are loaded — KbID/FileID
// already appear on the matching child row, and metadata stays on the
// child for compatibility with downstream pages-extraction logic.
type ParentChunkRow struct {
	ID               string
	Content          string
	ContextualPrefix string
}

// nullableUUIDString maps *string holding a UUID to either the string
// (for non-NULL FK references) or nil (for NULL — the legacy flat-chunk
// case). Used by insertBatch to write parent_chunk_id.
func nullableUUIDString(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

// AddParentChunks inserts a batch of parents into document_chunk_parents
// and returns the generated UUIDs in the same order as the input slice.
// Caller uses the returned IDs to set ChunkInput.ParentChunkID on each
// child before inserting children via AddDocumentChunks.
//
// Insert is a single multi-row INSERT ... VALUES ... RETURNING id round-trip
// (PostgreSQL preserves the VALUES order in RETURNING, so positional pairing
// with the input slice is safe). The statement itself is implicitly atomic
// — either every row commits or none does — so no explicit transaction is
// needed.
func (s *ChunkService) AddParentChunks(ctx context.Context, parents []ParentChunkInput) ([]string, error) {
	if len(parents) == 0 {
		return nil, nil
	}

	const colsPerRow = 5 // kb_id, file_id, content, contextual_prefix, metadata
	const headerLen = 128
	const rowLen = 64
	var sb strings.Builder
	sb.Grow(headerLen + len(parents)*rowLen)
	sb.WriteString(`INSERT INTO document_chunk_parents (kb_id, file_id, content, contextual_prefix, metadata) VALUES `)

	args := make([]any, 0, len(parents)*colsPerRow)
	for i, p := range parents {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * colsPerRow
		fmt.Fprintf(&sb,
			"($%d::uuid, $%d::uuid, $%d, $%d, $%d::jsonb)",
			base+1, base+2, base+3, base+4, base+5,
		)

		var meta any
		if p.Metadata != nil {
			b, mErr := json.Marshal(p.Metadata)
			if mErr != nil {
				return nil, fmt.Errorf("AddParentChunks: marshal metadata at %d: %w", i, mErr)
			}
			meta = string(b)
		}
		args = append(args,
			p.KbID,
			p.FileID,
			sanitizeUTF8(p.Content),
			nullableString(sanitizeUTF8(p.ContextualPrefix)),
			meta,
		)
	}
	sb.WriteString(" RETURNING id")

	rows, err := s.vectorDB.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("AddParentChunks: query: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, len(parents))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("AddParentChunks: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("AddParentChunks: rows: %w", err)
	}
	if len(ids) != len(parents) {
		return nil, fmt.Errorf("AddParentChunks: expected %d returning rows, got %d", len(parents), len(ids))
	}
	return ids, nil
}

// UpdateParentContextualPrefix sets the contextual_prefix column for one
// parent row. Used by the async enrichment path: insert parents without
// a prefix, then update each as the enrichment LLM call returns. No-op
// if prefix is empty (insert path already wrote NULL).
func (s *ChunkService) UpdateParentContextualPrefix(ctx context.Context, parentID, prefix string) error {
	if parentID == "" || prefix == "" {
		return nil
	}
	const updateSQL = `UPDATE document_chunk_parents SET contextual_prefix = $1 WHERE id = $2`
	_, err := s.vectorDB.Exec(ctx, updateSQL, sanitizeUTF8(prefix), parentID)
	if err != nil {
		return fmt.Errorf("UpdateParentContextualPrefix: %w", err)
	}
	return nil
}

// LookupParentContent fetches parent rows for a set of IDs in one query.
// Returns a map keyed by parent ID for O(1) child→parent swap at search
// time. Missing IDs (parent was deleted between search and lookup) are
// silently absent from the map; the search caller treats absence as
// "skip the swap, keep child content" — fail-open.
func (s *ChunkService) LookupParentContent(ctx context.Context, ids []string) (map[string]ParentChunkRow, error) {
	if len(ids) == 0 {
		return map[string]ParentChunkRow{}, nil
	}
	const querySQL = `
		SELECT id, content, COALESCE(contextual_prefix, '')
		FROM document_chunk_parents
		WHERE id = ANY($1::uuid[])`
	rows, err := s.vectorDB.Query(ctx, querySQL, ids)
	if err != nil {
		return nil, fmt.Errorf("LookupParentContent: query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]ParentChunkRow, len(ids))
	for rows.Next() {
		var r ParentChunkRow
		if err := rows.Scan(&r.ID, &r.Content, &r.ContextualPrefix); err != nil {
			return nil, fmt.Errorf("LookupParentContent: scan: %w", err)
		}
		out[r.ID] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("LookupParentContent: rows: %w", err)
	}
	return out, nil
}

// DeleteParentChunksByFileID removes all parent rows associated with a
// file. Called by the processor before re-ingesting (alongside the
// existing DeleteChunksByFileID for the dim-versioned children) so a
// re-ingestion doesn't leave orphan parents.
func (s *ChunkService) DeleteParentChunksByFileID(ctx context.Context, fileID string) error {
	const deleteSQL = `DELETE FROM document_chunk_parents WHERE file_id = $1::uuid`
	_, err := s.vectorDB.Exec(ctx, deleteSQL, fileID)
	if err != nil {
		return fmt.Errorf("DeleteParentChunksByFileID: %w", err)
	}
	return nil
}
