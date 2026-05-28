// Package tabular materializes uploaded spreadsheets into native-typed
// Postgres tables so the table_query MCP tool can answer lookups,
// aggregations, and filter/sort queries with deterministic SQL instead of
// embedding-based retrieval. See
// docs/superpowers/specs/2026-05-28-tabular-data-qa-design.md.
package tabular

// ColumnType is the narrow set of SQL types the inferrer targets. Anything
// it cannot prove uniformly numeric/temporal/boolean falls back to Text.
type ColumnType string

const (
	TypeBigint ColumnType = "bigint"
	TypeFloat  ColumnType = "double precision"
	TypeDate   ColumnType = "date"
	TypeBool   ColumnType = "boolean"
	TypeText   ColumnType = "text"
)

// ColumnSpec describes one materialized column. Original is the spreadsheet
// header as the user sees it; Name is the sanitized SQL identifier the LLM
// must use in queries. Embedded is true when Phase-2 flagged this column as
// free text to embed for fuzzy search.
type ColumnSpec struct {
	Original string     `json:"original"`
	Name     string     `json:"name"`
	Type     ColumnType `json:"type"`
	Embedded bool       `json:"embedded,omitempty"`
}

// SemanticOptions controls Phase-2 free-text embedding. Enabled gates the whole
// path (adds _rowid + emits row-chunks); the thresholds drive the heuristic.
// A threshold of 0 disables that filter.
type SemanticOptions struct {
	Enabled          bool
	MinAvgLen        int
	MinDistinctRatio float64
}

// RowChunk is one embeddable row: RowID is the synthetic _rowid; Text is the
// full chunk content (source header + labeled flagged-column values).
type RowChunk struct {
	RowID int64
	Text  string
}

// SheetData is one sheet's raw content as read from a file. Rows includes the
// header row; header detection + column typing happen later in BuildColumnSpecs.
type SheetData struct {
	SheetName string
	Rows      [][]string
}
