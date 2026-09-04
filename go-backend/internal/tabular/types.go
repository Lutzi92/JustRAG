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
	TypeBigint    ColumnType = "bigint"
	TypeFloat     ColumnType = "double precision"
	TypeDate      ColumnType = "date"
	TypeBool      ColumnType = "boolean"
	TypeText      ColumnType = "text"
	TypeNumeric   ColumnType = "numeric"
	TypeTimestamp ColumnType = "timestamp"
)

// ColumnSpec describes one materialized column. Original is the spreadsheet
// header as the user sees it; Name is the sanitized SQL identifier the LLM
// must use in queries. Embedded is true when Phase-2 flagged this column as
// free text to embed for fuzzy search.
//
// Role/Description/ShadowOf are the profiler's (Phase-2) view of a column:
// Role is the profiled semantic role as a string (e.g. "measure",
// "dimension"); Description is a short human-readable gloss; ShadowOf names
// the primary column this one shadows, e.g. "baujahr" for the coerced
// "baujahr_num" column. All three are omitted by a Phase-1 writer and decode
// to their zero value when reading Phase-1 JSON.
type ColumnSpec struct {
	Original    string     `json:"original"`
	Name        string     `json:"name"`
	Type        ColumnType `json:"type"`
	Embedded    bool       `json:"embedded,omitempty"`
	Role        string     `json:"role,omitempty"`
	Description string     `json:"description,omitempty"`
	ShadowOf    string     `json:"shadow_of,omitempty"`
}

// ColumnStat is the per-column entry of tabular_catalog.column_stats: the
// profiler's summary of one materialized column, stored alongside the
// (unchanged) Phase-1 `columns` shape for readers that only know that one.
//
// ShadowColumn is the inverse of ColumnSpec.ShadowOf: set on the PRIMARY
// column's stat entry, it names the shadow numeric-coercion column (e.g.
// "baujahr_num" on the "baujahr" entry). ColumnSpec.ShadowOf is the only
// ShadowOf in this package; ColumnStat carries ShadowColumn instead, on
// ruling R4.
type ColumnStat struct {
	Name            string   `json:"name"`
	Original        string   `json:"original"`
	Type            string   `json:"type"`
	Role            string   `json:"role"`
	Description     string   `json:"description,omitempty"`
	ShadowColumn    string   `json:"shadow_column,omitempty"`
	ValueSet        []string `json:"value_set,omitempty"`
	Min             string   `json:"min,omitempty"`
	Max             string   `json:"max,omitempty"`
	NullCount       int64    `json:"null_count"`
	NullTokens      int64    `json:"null_tokens"`
	DistinctCount   int64    `json:"distinct_count"`
	HighCardinality bool     `json:"high_cardinality,omitempty"`
	Samples         []string `json:"samples,omitempty"`
	CoercionFailed  int64    `json:"coercion_failed"`
}

// SheetReport is one sheet's entry in a file's ParseReport (files.parse_report).
type SheetReport struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Hidden    bool   `json:"hidden"`
	HeaderRow int    `json:"header_row"`
	Columns   int    `json:"columns"`

	RowsRead         int `json:"rows_read"`
	RowsMaterialised int `json:"rows_materialised"`
	RowsEmbedded     int `json:"rows_embedded"`
	RowsPastCap      int `json:"rows_past_cap"`

	FormulaCellsEmpty int  `json:"formula_cells_empty"`
	CoercionFailures  int  `json:"coercion_failures"`
	UsedLLM           bool `json:"used_llm"`
	DroppedColumns    int  `json:"dropped_columns"`

	Notes  []string `json:"notes,omitempty"`
	Tables []string `json:"tables,omitempty"`
}

// ParseReport is the per-file ingest report persisted to files.parse_report
// (spec §4.6): what the parser/materializer found and did, one entry per
// sheet, for the ingest-detail UI.
type ParseReport struct {
	Version      int           `json:"version"`
	Materialised bool          `json:"materialised"`
	Sheets       []SheetReport `json:"sheets"`
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
