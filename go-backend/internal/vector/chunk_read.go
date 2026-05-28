package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ChunkRow is the minimal shape the AP-B1 chunk_read and
// document_outline tools surface to the LLM. Includes the JSONB
// metadata blob so document_outline can read the per-chunk
// `sections` breadcrumb the processor populates during ingestion.
type ChunkRow struct {
	ID               string
	FileID           string
	Content          string
	ContextualPrefix string
	Metadata         map[string]any
}

// LookupChunkNeighbors returns the chunk identified by chunkID plus
// up to `depth` chunks before and after it in the same file. The
// "before/after" ordering uses `created_at` ASC because the chunk
// table doesn't carry an explicit position column — chunks are
// inserted sequentially during ingestion, so created_at is a
// reliable proxy for source order. Documented soft contract: a
// re-ingestion that reshuffles chunks would invalidate this; in
// practice ingestion is sequential per file.
//
// direction is "prev_sibling" | "next_sibling" | "both" (case-
// insensitive). depth is clamped to [1, 10].
//
// The target chunk itself is always included as the first element
// (or the middle, when direction=both). Returns an empty slice + nil
// error when chunkID is not found — the caller surfaces "no chunk".
func (s *SearchService) LookupChunkNeighbors(ctx context.Context, kbID, chunkID, direction string, depth int) ([]ChunkRow, error) {
	if kbID == "" || chunkID == "" {
		return nil, fmt.Errorf("chunk_read: kb_id and chunk_id required")
	}
	if depth < 1 {
		depth = 1
	}
	if depth > 10 {
		depth = 10
	}
	dir := strings.ToLower(strings.TrimSpace(direction))
	switch dir {
	case "prev_sibling", "next_sibling", "both", "":
		if dir == "" {
			dir = "both"
		}
	default:
		return nil, fmt.Errorf("chunk_read: invalid direction %q (want prev_sibling|next_sibling|both)", direction)
	}

	tableName, err := s.resolveKBChunkTable(ctx, kbID)
	if err != nil {
		return nil, err
	}

	// Locate the target's file_id + created_at. Two queries instead of
	// one window-function query because the latter is more fragile
	// across the dim-keyed tables (each is a separate physical table).
	var fileID string
	var createdAt any // pgx scans timestamp into time.Time but we treat it opaquely
	err = s.vectorDB.QueryRow(ctx,
		fmt.Sprintf(`SELECT file_id::text, created_at FROM "%s" WHERE id = $1::uuid AND kb_id = $2::uuid`, tableName),
		chunkID, kbID,
	).Scan(&fileID, &createdAt)
	if err != nil {
		// pgx returns ErrNoRows here when the chunk doesn't exist.
		// Surface as empty result rather than error — the LLM should
		// see a clean "not found" instead of a stack trace.
		return nil, nil
	}

	out := []ChunkRow{}
	if dir == "prev_sibling" || dir == "both" {
		prev, err := s.fetchNeighbors(ctx, tableName, kbID, fileID, createdAt, depth, true /*before*/)
		if err != nil {
			return nil, err
		}
		out = append(out, prev...)
	}

	// Self
	self, err := s.fetchByID(ctx, tableName, kbID, chunkID)
	if err == nil {
		out = append(out, self)
	}

	if dir == "next_sibling" || dir == "both" {
		next, err := s.fetchNeighbors(ctx, tableName, kbID, fileID, createdAt, depth, false /*after*/)
		if err != nil {
			return nil, err
		}
		out = append(out, next...)
	}
	return out, nil
}

// fetchNeighbors returns up to `depth` rows before or after the
// target chunk's created_at, ordered chronologically (so the result
// reads in source order regardless of which side we're fetching).
func (s *SearchService) fetchNeighbors(ctx context.Context, tableName, kbID, fileID string, pivot any, depth int, before bool) ([]ChunkRow, error) {
	cmp := ">"
	order := "ASC"
	if before {
		cmp = "<"
		order = "DESC"
	}
	rows, err := s.vectorDB.Query(ctx, fmt.Sprintf(
		`SELECT id::text, file_id::text, content, COALESCE(contextual_prefix, ''), COALESCE(metadata, '{}'::text)
		 FROM %s
		 WHERE kb_id = $1::uuid AND file_id = $2::uuid AND created_at %s $3
		 ORDER BY created_at %s
		 LIMIT $4`,
		tableName, cmp, order),
		kbID, fileID, pivot, depth)
	if err != nil {
		return nil, fmt.Errorf("chunk_read: fetch neighbors: %w", err)
	}
	defer rows.Close()

	out := make([]ChunkRow, 0, depth)
	for rows.Next() {
		var r ChunkRow
		var metaRaw string
		if err := rows.Scan(&r.ID, &r.FileID, &r.Content, &r.ContextualPrefix, &metaRaw); err != nil {
			return nil, fmt.Errorf("chunk_read: scan: %w", err)
		}
		r.Metadata = parseMetadataString(metaRaw)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chunk_read: rows: %w", err)
	}
	// "before" results were fetched DESC for the LIMIT to grab the
	// closest siblings; reverse so the final order is chronological
	// (same as "after" results), giving the LLM a left-to-right read.
	if before {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

func (s *SearchService) fetchByID(ctx context.Context, tableName, kbID, chunkID string) (ChunkRow, error) {
	var r ChunkRow
	var metaRaw string
	err := s.vectorDB.QueryRow(ctx, fmt.Sprintf(
		`SELECT id::text, file_id::text, content, COALESCE(contextual_prefix, ''), COALESCE(metadata, '{}'::text)
		 FROM %s WHERE id = $1::uuid AND kb_id = $2::uuid`, tableName),
		chunkID, kbID,
	).Scan(&r.ID, &r.FileID, &r.Content, &r.ContextualPrefix, &metaRaw)
	if err != nil {
		return ChunkRow{}, err
	}
	r.Metadata = parseMetadataString(metaRaw)
	return r, nil
}

// DocumentOutline returns a synthetic outline of the file built from
// the per-chunk `sections` breadcrumbs the processor populates
// during ingestion (see internal/processor/sections.go). Each entry
// is one heading line as the LLM would read it ("# Chapter 1 / ##
// Section 1.2"); duplicates from successive chunks under the same
// section are collapsed.
//
// This is NOT the original markdown outline — that's not persisted.
// It's an outline reconstructed from breadcrumbs, accurate enough to
// let the agent navigate "go to the section about X" in long
// documents without re-parsing the source file.
func (s *SearchService) DocumentOutline(ctx context.Context, kbID, fileID string) ([]string, error) {
	if kbID == "" || fileID == "" {
		return nil, fmt.Errorf("document_outline: kb_id and file_id required")
	}
	tableName, err := s.resolveKBChunkTable(ctx, kbID)
	if err != nil {
		return nil, err
	}
	rows, err := s.vectorDB.Query(ctx, fmt.Sprintf(
		`SELECT COALESCE(metadata, '{}'::text)
		 FROM %s
		 WHERE kb_id = $1::uuid AND file_id = $2::uuid
		 ORDER BY created_at ASC`, tableName),
		kbID, fileID)
	if err != nil {
		return nil, fmt.Errorf("document_outline: query: %w", err)
	}
	defer rows.Close()

	var seen []string
	var lastBreadcrumb string
	for rows.Next() {
		var metaRaw string
		if err := rows.Scan(&metaRaw); err != nil {
			return nil, fmt.Errorf("document_outline: scan: %w", err)
		}
		meta := parseMetadataString(metaRaw)
		sections, ok := meta["sections"].([]any)
		if !ok || len(sections) == 0 {
			continue
		}
		parts := make([]string, 0, len(sections))
		for _, s := range sections {
			if str, ok := s.(string); ok {
				parts = append(parts, str)
			}
		}
		breadcrumb := strings.Join(parts, " / ")
		if breadcrumb == "" || breadcrumb == lastBreadcrumb {
			continue
		}
		seen = append(seen, breadcrumb)
		lastBreadcrumb = breadcrumb
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document_outline: rows: %w", err)
	}
	return seen, nil
}

// parseMetadataString tolerates both empty / null payloads and
// genuinely malformed JSON. A bad blob just yields an empty map.
// Distinct from neighbors.go's parseMetadataJSON which takes
// json.RawMessage; this one starts from the SQL-driver's text-mode
// scan target.
func parseMetadataString(raw string) map[string]any {
	out := make(map[string]any)
	if raw == "" || raw == "null" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
