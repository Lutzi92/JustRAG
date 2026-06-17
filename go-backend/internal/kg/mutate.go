package kg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// DeleteKGForFile removes exactly one file's contribution to the KB graph:
// its edges (by file_id), its entity-file links, and then any entity whose
// last contributing file was this one (orphan GC). Edges referencing a GC'd
// entity are removed by the existing kg_edges.*_entity_id ON DELETE CASCADE.
//
// Files ingested before migration 0055 have NULL file_id and no
// kg_entity_files rows, so deleting them is a no-op here — their contribution
// can only be cleared by a full per-KB graph rebuild. Best-effort by contract:
// the caller (file-delete handler) treats errors as non-fatal.
func (s *PgStore) DeleteKGForFile(ctx context.Context, kbID, fileID string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("kg: DeleteKGForFile: no pool")
	}
	if kbID == "" || fileID == "" {
		return fmt.Errorf("kg: DeleteKGForFile: empty kbID/fileID")
	}
	return pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM kg_edges WHERE kb_id = $1::uuid AND file_id = $2::uuid`,
			kbID, fileID); err != nil {
			return fmt.Errorf("delete edges: %w", err)
		}
		// Delete this file's entity links, then GC only those entities that
		// have no link to ANY OTHER file. The GC subquery must exclude the
		// file being deleted (f.file_id <> $2): a single statement's CTE and
		// subquery share one snapshot, so the rows the `affected` CTE deletes
		// are still visible to the subquery — without the `<>` filter NOT
		// EXISTS would always be false and nothing would ever be GC'd. Scoping
		// the candidate set to `affected` keeps legacy pre-0055 entities (no
		// kg_entity_files rows at all) untouched, per spec.
		if _, err := tx.Exec(ctx, `
			WITH affected AS (
			    DELETE FROM kg_entity_files
			     WHERE kb_id = $1::uuid AND file_id = $2::uuid
			    RETURNING entity_id
			)
			DELETE FROM kg_entities e
			 WHERE e.id IN (SELECT entity_id FROM affected)
			   AND NOT EXISTS (
			       SELECT 1 FROM kg_entity_files f
			        WHERE f.entity_id = e.id AND f.file_id <> $2::uuid
			   )`, kbID, fileID); err != nil {
			return fmt.Errorf("delete entity_files + gc orphans: %w", err)
		}
		return nil
	})
}

// KBHasActiveIngestion reports whether the KB has any file still pending or
// processing — the "still building" signal the mindmap spinner uses.
func (s *PgStore) KBHasActiveIngestion(ctx context.Context, kbID string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, fmt.Errorf("kg: KBHasActiveIngestion: no pool")
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM files
		     WHERE kb_id = $1::uuid AND status IN ('pending', 'processing')
		)`, kbID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("kg: KBHasActiveIngestion: %w", err)
	}
	return exists, nil
}
