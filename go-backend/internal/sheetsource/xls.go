package sheetsource

import (
	"errors"
	"fmt"
	"os"

	"github.com/justrag/go-backend/internal/sheetsource/biffxls"
)

// XLSSource reads legacy BIFF8 .xls workbooks through the in-tree biffxls
// fork. Unlike the xlsx reader it has no validation records to offer, and the
// formula text itself is not decoded — only the cached result stored next to
// each FORMULA record.
type XLSSource struct {
	wb *biffxls.WorkBook
	f  *os.File
}

var _ Source = (*XLSSource)(nil)

// OpenXLS opens a .xls workbook. The whole workbook stream is parsed eagerly
// (BIFF has no per-sheet index that could be read lazily), but individual
// sheets are only decoded on first access.
func OpenXLS(path string) (*XLSSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	wb, err := biffxls.OpenReader(f, "utf-8")
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("sheetsource: open xls: %w", err)
	}
	if wb == nil {
		f.Close()
		return nil, errors.New("sheetsource: open xls: no workbook stream")
	}
	return &XLSSource{wb: wb, f: f}, nil
}

func (s *XLSSource) Close() error { return s.f.Close() }

func (s *XLSSource) Sheets() []SheetInfo {
	out := make([]SheetInfo, 0, s.wb.NumSheets())
	for i := 0; i < s.wb.NumSheets(); i++ {
		sh := s.wb.GetSheet(i)
		if sh == nil {
			continue
		}
		out = append(out, SheetInfo{Index: i, Name: sh.Name, Hidden: sh.Hidden()})
	}
	return out
}

func (s *XLSSource) ReadSheet(index int, fn RowFunc) (SheetExtras, error) {
	var ex SheetExtras
	sh := s.wb.GetSheet(index)
	if sh == nil {
		return ex, fmt.Errorf("sheetsource: sheet %d out of range", index)
	}
	for _, m := range sh.Merged {
		ex.Merged = append(ex.Merged, Range{FromRow: m[0], FromCol: m[1], ToRow: m[2], ToCol: m[3]})
	}
	date1904 := s.wb.DateMode1904()
	for r := 0; r <= int(sh.MaxRow); r++ {
		row := sh.Row(r)
		last := -1
		if row != nil {
			last = row.LastDefinedCol()
		}
		if last < 0 {
			if err := fn(r, nil); err != nil {
				return ex, stopOrErr(err, &ex, r)
			}
			continue
		}
		// A fresh slice per row: RowFunc callers are allowed to retain it.
		cells := make([]Cell, last+1)
		for c := 0; c <= last; c++ {
			if cv, ok := sh.CellAt(r, c); ok {
				cells[c] = cellFromXLS(cv, date1904)
			}
		}
		if len(cells) > ex.MaxCol {
			ex.MaxCol = len(cells)
		}
		if err := fn(r, cells); err != nil {
			return ex, stopOrErr(err, &ex, r)
		}
	}
	ex.RowCount = int(sh.MaxRow) + 1
	return ex, nil
}

func stopOrErr(err error, ex *SheetExtras, r int) error {
	if errors.Is(err, ErrStop) {
		ex.RowCount = r + 1
		return nil
	}
	return err
}

func cellFromXLS(cv biffxls.CellValue, date1904 bool) Cell {
	c := Cell{IsFormula: cv.IsFormula}
	switch {
	case cv.IsError:
		c.Kind, c.Raw = KindError, cv.Text
	case cv.IsBool:
		c.Kind, c.Raw = KindBool, cv.Text
	case cv.IsNumber && cv.IsDate:
		c.Kind, c.Raw = KindDate, serialToISO(cv.Number, date1904)
	case cv.IsNumber && cv.IsPercent:
		c.Kind, c.Raw = KindNumber, canonicalNumber(cv.Number*100)
		c.Formatted, c.Style.Percent = c.Raw+"%", true
	case cv.IsNumber:
		c.Kind, c.Raw = KindNumber, canonicalNumber(cv.Number)
	case cv.Text == "":
		c.Kind = KindEmpty
	default:
		c.Kind, c.Raw = KindText, cv.Text
	}
	if c.Formatted == "" {
		c.Formatted = c.Raw
	}
	return c
}
