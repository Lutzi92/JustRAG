package biffxls

// FORK ADDITION (not upstream): typed cell access.

// CellValue is the decoded content of a single cell. Upstream only exposes
// display strings via contentHandler.String, which loses the number/date/bool
// distinction and drops formula results entirely.
type CellValue struct {
	Text      string
	Number    float64
	IsNumber  bool
	IsBool    bool
	IsError   bool
	IsFormula bool
	IsDate    bool // XF number format classified as date
	IsPercent bool
	Unit      string // literal unit from the number-format code ("€", "m²", …)
}

// valuer is implemented by every content handler that can report a typed
// value. i is the offset from the handler's FirstCol (always 0 except for the
// MULRK/MULBLANK multi-cell records).
type valuer interface {
	ValueAt(wb *WorkBook, i int) CellValue
}
