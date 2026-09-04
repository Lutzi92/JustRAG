// Package sheetsource streams spreadsheet files as rows of typed cells. See the 2026-09-04 spreadsheet-ingest spec §3.1.
package sheetsource

import (
	"errors"
	"strings"
)

type CellKind uint8

const (
	KindEmpty CellKind = iota
	KindText
	KindNumber
	KindBool
	KindDate
	KindError
)

type CellStyle struct {
	Bold         bool
	Filled       bool
	BottomBorder bool
	Percent      bool
	Unit         string // "€", "m²", "%" … taken from the number-format code, may be ""
}

type Cell struct {
	Raw       string // canonical: number "2143.28" (no grouping), percent already ×100 ("12.5"), date "2026-09-04" or "2026-09-04T15:04:05", bool "true"/"false", text verbatim, error "#N/A"
	Formatted string // display-oriented string; phase 1: Raw plus "%" for percent cells
	Kind      CellKind
	IsFormula bool
	Formula   string // formula text without leading "=", "" when !IsFormula
	Style     CellStyle
}

type Range struct{ FromRow, FromCol, ToRow, ToCol int } // 0-based, inclusive

type Validation struct {
	Sqref  []Range
	Ref    string   // formula1 verbatim: `"Ja,Nein"` or `Dropdown!$C$6:$C$7` or a defined name
	Values []string // resolved list values; nil when unresolved
}

type SheetInfo struct {
	Index  int
	Name   string
	Hidden bool // hidden or veryHidden
}

type SheetExtras struct {
	Merged      []Range
	Validations []Validation
	MaxCol      int // widest row (number of cells)
	RowCount    int // rows delivered, gaps included
}

// RowFunc receives 0-based row indices in ascending order; gap rows are
// delivered with cells == nil. Return ErrStop to stop early (extras may then
// be incomplete because merges/validations follow sheetData in the XML).
type RowFunc func(rowIdx int, cells []Cell) error

var ErrStop = errors.New("sheetsource: stop")

type Source interface {
	Sheets() []SheetInfo
	ReadSheet(index int, fn RowFunc) (SheetExtras, error)
	Close() error
}

func (c Cell) IsEmpty() bool {
	return c.Kind == KindEmpty || (c.Kind == KindText && strings.TrimSpace(c.Raw) == "")
}
