package vector

import (
	"context"
	"fmt"

	"github.com/justrag/go-backend/internal/logctx"
)

// shouldWarnDimMismatch reports whether to emit a dimension-mismatch warning:
// the query's target chunk table has no rows for this KB, yet some other
// chunk table does — i.e. the corpus is indexed under a different dimension.
func shouldWarnDimMismatch(targetHasRows, anyOtherTableHasRows bool) bool {
	return !targetHasRows && anyOtherTableHasRows
}

// checkDimMismatch probes whether the query's target vector table is empty for
// this KB while some other document_chunks_* table holds rows for it. Returns
// a non-empty detail string when a warning should fire, else "".
//
// The method is best-effort: any DB error is treated as "table has rows" (no
// warning) rather than surfacing a secondary failure during a live query.
//
// Safety note: tableName is always produced by GetVectorTableName (allowlisted
// to document_chunks_<digits>) so Sprintf-ing it directly into SQL is safe —
// same pattern as fetchChunksByIDs and FetchChunksByIndices in this package.
func (s *SearchService) checkDimMismatch(ctx context.Context, tableName, kbID string) string {
	if s.vectorDB == nil {
		return ""
	}

	// Probe target table for any row for this KB.
	targetHasRows := true // default: assume rows exist so errors are silent
	var exists bool
	// tableName is allowlisted (GetVectorTableName → document_chunks_<digits>).
	targetSQL := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %q WHERE kb_id=$1 LIMIT 1)`, tableName)
	if err := s.vectorDB.QueryRow(ctx, targetSQL, kbID).Scan(&exists); err == nil {
		targetHasRows = exists
	}

	if targetHasRows {
		// No need to check other tables; healthy path.
		return ""
	}

	// Target table is empty for this KB. List all document_chunks_* tables.
	const listTablesSQL = `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name LIKE 'document_chunks_%'`

	rows, err := s.vectorDB.Query(ctx, listTablesSQL)
	if err != nil {
		// Best-effort: if we can't enumerate tables, skip the warning.
		return ""
	}
	defer rows.Close()

	anyOther := false
	for rows.Next() {
		var otherTable string
		if err := rows.Scan(&otherTable); err != nil {
			continue
		}
		if otherTable == tableName {
			continue
		}
		// tableName variants are allowlisted via pg_catalog; same safety as above.
		otherSQL := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %q WHERE kb_id=$1 LIMIT 1)`, otherTable)
		var otherExists bool
		if err := s.vectorDB.QueryRow(ctx, otherSQL, kbID).Scan(&otherExists); err == nil && otherExists {
			anyOther = true
			break
		}
	}
	_ = rows.Err() // best-effort; errors don't suppress the warning

	if shouldWarnDimMismatch(false, anyOther) {
		return fmt.Sprintf("target table %s empty for kb but other chunk tables hold rows", tableName)
	}
	return ""
}

// warnDimMismatch checks for a dimension mismatch and logs a rate-limited
// warning the first time a given (kbID, tableName) pair is seen with a
// mismatch. Called immediately after tableName is derived from the query
// embedding dimension.
func (s *SearchService) warnDimMismatch(ctx context.Context, tableName, kbID string, dimensions int) {
	if _, seen := s.dimWarnOnce.LoadOrStore(kbID+":"+tableName, struct{}{}); seen {
		return
	}
	if detail := s.checkDimMismatch(ctx, tableName, kbID); detail != "" {
		logctx.From(ctx).Warn("search: query embedding dimension may not match indexed corpus",
			"dimensions", dimensions, "table", tableName, "detail", detail)
	}
}
