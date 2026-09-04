package profile

import "github.com/justrag/go-backend/internal/sheetsource"

type SheetKind string

const (
	KindTable SheetKind = "table"
	KindForm  SheetKind = "form"
	KindProse SheetKind = "prose"
	KindEmpty SheetKind = "empty"
)

type Role string

const (
	RoleID       Role = "id"
	RoleMeasure  Role = "measure"
	RoleDate     Role = "date"
	RoleCategory Role = "category"
	RoleBool     Role = "bool"
	RoleText     Role = "text"
)

// Region is a dense block of the sample grid, 0-based inclusive sheet coordinates.
// OpenEnded means the block reaches the last sample row, i.e. data continues.
type Region struct {
	Top, Left, Bottom, Right int
	OpenEnded                bool
}

type ColumnProfile struct {
	Index        int         `json:"index"`  // absolute sheet column
	Header       string      `json:"header"` // joined multi-row header; "" when none
	Role         Role        `json:"role"`
	Description  string      `json:"description,omitempty"`
	ListValues   []string    `json:"list_values,omitempty"`
	DecimalComma bool        `json:"decimal_comma,omitempty"`
	Unit         string      `json:"unit,omitempty"`
	Stats        ColumnStats `json:"stats"`
}

type ColumnStats struct {
	NonEmpty, Numeric, Dates, Bools, Texts, Distinct int
	LeadingZero, LongDigits, IDPattern               int
	AvgLen                                           float64
}

type RegionProfile struct {
	Region      Region          `json:"region"`
	Kind        SheetKind       `json:"kind"`
	HeaderRows  []int           `json:"header_rows,omitempty"`
	DataStart   int             `json:"data_start"`
	Columns     []ColumnProfile `json:"columns,omitempty"`
	Dropped     []int           `json:"dropped_columns,omitempty"`
	IndexRow    int             `json:"index_row"` // -1 when none
	ProseAbove  []string        `json:"prose_above,omitempty"`
	DerivedRows []int           `json:"derived_rows,omitempty"` // absolute row indices within the sample
	Confidence  float64         `json:"confidence"`
	UsedLLM     bool            `json:"used_llm"`
	Diagnostics map[string]any  `json:"diagnostics,omitempty"`
}

type SheetProfile struct {
	Sheet   sheetsource.SheetInfo `json:"sheet"`
	Kind    SheetKind             `json:"kind"` // table if any region is a table
	Regions []RegionProfile       `json:"regions"`
}

type Options struct {
	MaxHeaderRows int // default 3
}
