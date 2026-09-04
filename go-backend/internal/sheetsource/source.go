// Package sheetsource streams spreadsheet files as rows of typed cells. See the 2026-09-04 spreadsheet-ingest spec §3.1.
package sheetsource

import (
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
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
	Index    int
	Name     string
	Hidden   bool // hidden or veryHidden
	RowCount int  // declared row count from the sheet's <dimension ref="A1:Z1234"/> (xlsx only); 0 = unknown. Known BEFORE any read, unlike SheetExtras.RowCount below.
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

// recoverToErr turns a panic into an error on *err. Every exported entry point
// of every Source installs it: the files reaching these readers are untrusted
// uploads decoded by hand-rolled parsers (and, for .xls, by a fork of an
// upstream library that was never written with hostile input in mind), so a
// malformed file must fail its own ingest and nothing else. Callers that need
// to distinguish a parser bug from a corrupt file have the op and the panic
// value in the message.
//
// Note this also converts a panic raised inside the caller's own RowFunc; a
// RowFunc must therefore not rely on panicking through ReadSheet.
func recoverToErr(err *error, op string) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("sheetsource: panic in %s: %v", op, r)
	}
}

// maxPartBytes caps how many bytes a single decompressed archive part may
// yield. A zip entry's declared uncompressed size is attacker-controlled
// metadata, so the only real bound is on what is actually read. Package-level
// var rather than const so tests can shrink it.
var maxPartBytes int64 = 256 << 20

// capReader fails the read that would take the total past cap, so a zip bomb
// surfaces as a clear error instead of an out-of-memory worker. It reads one
// byte past cap before failing, which is what makes "exactly cap bytes" (fine)
// distinguishable from "more than cap bytes" (refused).
type capReader struct {
	r    io.Reader
	name string
	cap  int64
	read int64
}

func (c *capReader) tooLarge() error {
	return fmt.Errorf("sheetsource: %s exceeds the %d byte limit", c.name, c.cap)
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.read > c.cap {
		return 0, c.tooLarge()
	}
	if room := c.cap + 1 - c.read; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.r.Read(p)
	c.read += int64(n)
	if c.read > c.cap {
		return n, c.tooLarge()
	}
	return n, err
}

func cappedPart(r io.Reader, name string) io.Reader {
	return &capReader{r: r, name: name, cap: maxPartBytes}
}

// percentRaw canonicalises a stored fraction as its percentage value. Every
// reader must use it: f*100 is not exact in float64 (0.07*100 is
// 7.000000000000001, 0.365*100 is 36.499999999999996), so a plain
// FormatFloat produced different Raw strings per file format for the same
// displayed percentage. Rounding to 10 decimals is far below any spreadsheet's
// own precision and removes the noise.
func percentRaw(f float64) string {
	return strconv.FormatFloat(math.Round(f*100*1e10)/1e10, 'f', -1, 64)
}
