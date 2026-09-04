package tabular

import (
	"context"
	"errors"
	"strings"
)

// This file exists only to keep internal/processor (and any other
// not-yet-migrated caller) compiling between Task 4 (this materialiser
// rewrite) and Task 7 (which rewires ingest onto MaterializeRegion and
// deletes this file). Nothing here is exercised by a live code path: the
// real Materialize below always errors, so a build that still calls it
// falls back to normal text ingestion for the file, exactly as a Phase-1
// materializer failure always did.

// IsSpreadsheet reports whether a file should route through the tabular
// materializer, by MIME type or extension. Mirrors the parser CanParse
// checks.
//
// Deprecated: Phase-2 compat stub, deleted in Task 7.
func IsSpreadsheet(mimeType, fileName string) bool {
	switch mimeType {
	case "text/csv",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-excel":
		return true
	}
	lower := strings.ToLower(fileName)
	return strings.HasSuffix(lower, ".csv") ||
		strings.HasSuffix(lower, ".xlsx") ||
		strings.HasSuffix(lower, ".xls")
}

// SemanticOptions controls Phase-1 free-text embedding. Superseded by the
// profiler's per-column Role/Embedded classification in Phase 2.
//
// Deprecated: Phase-2 compat stub, deleted in Task 7.
type SemanticOptions struct {
	Enabled          bool
	MinAvgLen        int
	MinDistinctRatio float64
}

// RowChunk is one Phase-1 embeddable row.
//
// Deprecated: Phase-2 compat stub, deleted in Task 7.
type RowChunk struct {
	RowID int64
	Text  string
}

// SheetResult is one Phase-1 materialized sheet.
//
// Deprecated: Phase-2 compat stub, deleted in Task 7.
type SheetResult struct {
	SheetName string
	TableName string
	Columns   []ColumnSpec
	RowCount  int64
	RowChunks []RowChunk
}

// Result is the Phase-1 Materialize return shape.
//
// Deprecated: Phase-2 compat stub, deleted in Task 7.
type Result struct {
	Sheets []SheetResult
}

// BuildSummaryCard renders the Phase-1 one-chunk discoverability text.
//
// Deprecated: Phase-2 compat stub, deleted in Task 7.
func BuildSummaryCard(fileName, sheetName, tableName string, cols []ColumnSpec, rowCount int64) string {
	var b strings.Builder
	b.WriteString("Structured spreadsheet sheet \"" + sheetName + "\" from file \"" + fileName + "\".\n")
	b.WriteString("Queryable via the table_query tool as table \"" + TabularSchema + "\".\"" + tableName + "\".\n")
	return b.String()
}

// Materialize is the Phase-1 entrypoint. Removed in Task 4: the streaming
// MaterializeRegion replaces it. Always errors so a caller still on this
// path falls back to normal text ingestion for the file, same as any other
// Phase-1 materializer failure.
//
// Deprecated: Phase-2 compat stub, deleted in Task 7.
func (m *Materializer) Materialize(_ context.Context, _, _, _, _ string, _ SemanticOptions) (*Result, error) {
	return nil, errors.New("tabular: Phase-1 Materialize removed")
}
