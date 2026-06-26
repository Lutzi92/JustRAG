package vector

import (
	"context"
	"fmt"
)

// InsertCommunitySummary stores a KG community summary as a dim-keyed
// document_chunks row tagged node_kind='community_summary'. dim is derived from
// the embedding length (the KB embedder's dimension). fileID is a synthetic
// per-KB id (community summaries are KB-global; file_id is a soft ref with no
// FK). Returns the new chunk id. Mirrors the RAPTOR summary-insert path
// (AddDocumentChunks is the canonical insert with embedding_low derivation,
// tsvector building, and content_hash; we recover the id with a follow-up
// lookup rather than duplicating that path into an INSERT ... RETURNING).
func (s *ChunkService) InsertCommunitySummary(ctx context.Context, kbID, fileID, content string, embedding []float64, pgConfig string) (string, error) {
	if len(embedding) == 0 {
		return "", fmt.Errorf("vector: community summary: empty embedding")
	}
	dim := len(embedding)
	input := ChunkInput{
		Content:   content,
		Embedding: embedding,
		KbID:      kbID,
		FileID:    fileID,
		NodeKind:  "community_summary",
		TreeLevel: 0,
		Metadata:  map[string]any{"chunkKind": "community_summary"},
	}
	if err := s.AddDocumentChunks(ctx, fileID, []ChunkInput{input}, dim, pgConfig); err != nil {
		return "", fmt.Errorf("vector: insert community summary: %w", err)
	}
	id, err := s.lookupCommunitySummaryID(ctx, kbID, fileID, content, dim)
	if err != nil {
		return "", fmt.Errorf("vector: lookup community summary id: %w", err)
	}
	return id, nil
}

// lookupCommunitySummaryID recovers the id of a just-inserted community-summary
// chunk. Mirrors LookupSummaryID but filters node_kind='community_summary'
// (LookupSummaryID hardcodes node_kind='summary' for the RAPTOR path).
func (s *ChunkService) lookupCommunitySummaryID(ctx context.Context, kbID, fileID, content string, dimensions int) (string, error) {
	table := GetVectorTableName(dimensions)
	query := fmt.Sprintf(
		`SELECT id::text FROM "%s" WHERE kb_id=$1::uuid AND file_id=$2::uuid AND content=$3 AND node_kind='community_summary' ORDER BY created_at DESC LIMIT 1`,
		table,
	)
	var id string
	row := s.vectorDB.QueryRow(ctx, query, kbID, fileID, sanitizeUTF8(content))
	if err := row.Scan(&id); err != nil {
		return "", fmt.Errorf("lookup community summary id: %w", err)
	}
	return id, nil
}

// DeleteCommunitySummaries removes all community-summary chunks for a KB across
// every dim-keyed table (clear-then-rebuild). Uses ListChunkTableDimensions so
// it tracks whatever embedding dimensions actually exist on this deployment,
// including the legacy 1536 "document_chunks" table.
func (s *ChunkService) DeleteCommunitySummaries(ctx context.Context, kbID string) error {
	dims, err := s.ListChunkTableDimensions(ctx)
	if err != nil {
		return fmt.Errorf("vector: list chunk tables: %w", err)
	}
	for _, dim := range dims {
		table := GetVectorTableName(dim)
		if _, err := s.vectorDB.Exec(ctx, fmt.Sprintf(
			`DELETE FROM "%s" WHERE kb_id = $1::uuid AND node_kind = 'community_summary'`, table), kbID); err != nil {
			return fmt.Errorf("vector: delete community summaries from %s: %w", table, err)
		}
	}
	return nil
}
