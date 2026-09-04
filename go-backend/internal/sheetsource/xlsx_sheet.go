package sheetsource

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

const maxGapRows = 100_000

type XLSXSource struct{ wb *xlsxWorkbook }

func OpenXLSX(path string) (src *XLSXSource, err error) {
	defer recoverToErr(&err, "OpenXLSX")
	wb, err := openXLSX(path)
	if err != nil {
		return nil, err
	}
	return &XLSXSource{wb: wb}, nil
}

func (s *XLSXSource) Close() error { return s.wb.Close() }

func (s *XLSXSource) Sheets() []SheetInfo {
	out := make([]SheetInfo, len(s.wb.sheets))
	for i, sh := range s.wb.sheets {
		out[i] = SheetInfo{Index: i, Name: sh.Name, Hidden: sh.Hidden, RowCount: s.wb.sheetRowCount(sh.Part)}
	}
	return out
}

func canonicalNumber(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func serialToISO(serial float64, date1904 bool) string {
	t, err := excelize.ExcelDateToTime(serial, date1904)
	if err != nil {
		return canonicalNumber(serial)
	}
	if serial == math.Trunc(serial) {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02T15:04:05")
}

type xlsxCellState struct {
	ref, typ                        string
	styleIdx                        int
	v, formula                      strings.Builder
	inV, inF, inIS, inT, hasV, hasF bool
	inline                          strings.Builder
}

func (s *XLSXSource) cellFromState(st *xlsxCellState) Cell {
	c := Cell{IsFormula: st.hasF, Formula: strings.TrimSpace(st.formula.String())}
	if st.hasF && c.Formula == "" {
		c.Formula = "(shared)"
	}
	if st.styleIdx >= 0 && st.styleIdx < len(s.wb.xfs) {
		x := s.wb.xfs[st.styleIdx]
		c.Style = CellStyle{Bold: x.Bold, Filled: x.Filled, BottomBorder: x.BottomBorder, Percent: x.Percent, Unit: x.Unit}
	}
	v := st.v.String()
	// A value-bearing type (everything but inlineStr, which reads <is>
	// instead) with no <v> content is an uncached formula result or a
	// genuinely empty cell: "Empty <v> and no <is> -> KindEmpty".
	if st.typ != "inlineStr" && (!st.hasV || strings.TrimSpace(v) == "") {
		c.Kind = KindEmpty
		return c
	}
	switch st.typ {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil && idx >= 0 && idx < len(s.wb.sst) {
			c.Raw = s.wb.sst[idx]
		}
		c.Kind = KindText
	case "inlineStr":
		c.Raw, c.Kind = st.inline.String(), KindText
	case "str":
		c.Raw, c.Kind = v, KindText
	case "b":
		c.Kind = KindBool
		c.Raw = "false"
		if strings.TrimSpace(v) == "1" || strings.EqualFold(strings.TrimSpace(v), "true") {
			c.Raw = "true"
		}
	case "e":
		c.Raw, c.Kind = v, KindError
	default:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			c.Raw, c.Kind = v, KindText
			break
		}
		x := xfInfo{}
		if st.styleIdx >= 0 && st.styleIdx < len(s.wb.xfs) {
			x = s.wb.xfs[st.styleIdx]
		}
		switch {
		case x.Date:
			c.Kind, c.Raw = KindDate, serialToISO(f, s.wb.date1904)
		case x.Percent:
			c.Kind, c.Raw = KindNumber, percentRaw(f)
			c.Formatted = c.Raw + "%"
		default:
			c.Kind, c.Raw = KindNumber, canonicalNumber(f)
		}
	}
	if c.Kind == KindText && c.Raw == "" && !st.hasF {
		c.Kind = KindEmpty
	}
	if c.Formatted == "" {
		c.Formatted = c.Raw
	}
	return c
}

// readSheetRaw reads sheet data without resolving validations. The returned
// bool is true if the read was stopped early (ErrStop from callback).
func (s *XLSXSource) readSheetRaw(index int, fn RowFunc) (SheetExtras, bool, error) {
	var ex SheetExtras
	if index < 0 || index >= len(s.wb.sheets) {
		return ex, false, fmt.Errorf("sheetsource: sheet %d out of range", index)
	}
	rc, err := s.wb.openPart(s.wb.sheets[index].Part)
	if err != nil {
		return ex, false, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	nextRow := 0
	var row []Cell
	rowIdx, nextCol := -1, 0
	var st *xlsxCellState
	var dv *Validation
	inFormula1 := false
	deliver := func(idx int, cells []Cell) error {
		for nextRow < idx {
			if idx-nextRow > maxGapRows {
				return fmt.Errorf("sheetsource: gap of %d empty rows before row %d", idx-nextRow, idx+1)
			}
			if err := fn(nextRow, nil); err != nil {
				return err
			}
			nextRow++
		}
		if len(cells) > ex.MaxCol {
			ex.MaxCol = len(cells)
		}
		nextRow = idx + 1
		return fn(idx, cells)
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ex, false, fmt.Errorf("sheetsource: sheet xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				rowIdx = atoiAttr(t, "r") - 1
				if rowIdx < 0 {
					rowIdx = nextRow
				}
				row, nextCol = nil, 0
			case "c":
				st = &xlsxCellState{ref: attr(t, "r"), typ: attr(t, "t"), styleIdx: atoiAttr(t, "s")}
				if ref := attr(t, "r"); ref != "" {
					if _, col, err := ParseCellRef(ref); err == nil {
						nextCol = col
					}
				}
			case "v":
				if st != nil {
					st.inV, st.hasV = true, true
				}
			case "f":
				if st != nil {
					st.inF, st.hasF = true, true
				}
			case "is":
				if st != nil {
					st.inIS = true
				}
			case "t":
				if st != nil && st.inIS {
					st.inT = true
				}
			case "mergeCell":
				if r, err := ParseRange(attr(t, "ref")); err == nil {
					ex.Merged = append(ex.Merged, r)
				}
			case "dataValidation":
				if attr(t, "type") == "list" {
					sq, _ := ParseSqref(attr(t, "sqref"))
					dv = &Validation{Sqref: sq}
				}
			case "formula1":
				inFormula1 = dv != nil
			}
		case xml.CharData:
			switch {
			case st != nil && st.inV:
				st.v.Write(t)
			case st != nil && st.inF:
				st.formula.Write(t)
			case st != nil && st.inT:
				st.inline.Write(t)
			case inFormula1:
				dv.Ref += string(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				if st != nil {
					st.inV = false
				}
			case "f":
				if st != nil {
					st.inF = false
				}
			case "t":
				if st != nil {
					st.inT = false
				}
			case "is":
				if st != nil {
					st.inIS = false
				}
			case "c":
				if st != nil {
					for len(row) < nextCol {
						row = append(row, Cell{})
					}
					row = append(row, s.cellFromState(st))
					nextCol++
					st = nil
				}
			case "row":
				if rowIdx >= 0 {
					if err := deliver(rowIdx, row); err != nil {
						if errors.Is(err, ErrStop) {
							ex.RowCount = nextRow
							return ex, true, nil
						}
						return ex, false, err
					}
				}
				rowIdx = -1
			case "formula1":
				inFormula1 = false
			case "dataValidation":
				if dv != nil {
					dv.Ref = strings.TrimSpace(dv.Ref)
					ex.Validations = append(ex.Validations, *dv)
					dv = nil
				}
			}
		}
	}
	ex.RowCount = nextRow
	return ex, false, nil
}

// ReadSheet reads a sheet and resolves list validation values (unless the read
// was stopped early by the callback returning ErrStop).
func (s *XLSXSource) ReadSheet(index int, fn RowFunc) (ex SheetExtras, err error) {
	defer recoverToErr(&err, "XLSXSource.ReadSheet")
	ex, stopped, err := s.readSheetRaw(index, fn)
	if err != nil {
		return ex, err
	}
	if !stopped {
		s.resolveValidations(&ex, index)
	}
	return ex, nil
}
