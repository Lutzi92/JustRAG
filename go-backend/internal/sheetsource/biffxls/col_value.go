package biffxls

// FORK ADDITION (not upstream): typed access for the content handlers, plus
// the number-format classifier the typed accessors need. Each ValueAt mirrors
// the corresponding String method but keeps the number/date/bool distinction.

import (
	"strconv"
	"strings"
)

// value decodes an RK number together with the date/percent classification of
// its XF.
func (xf *XfRk) value(wb *WorkBook) CellValue {
	i, f, isFloat := xf.Rk.number()
	if !isFloat {
		f = float64(i)
	}
	cv := CellValue{IsNumber: true, Number: f, Text: strconv.FormatFloat(f, 'f', -1, 64)}
	cv.IsDate, cv.IsPercent, cv.Unit = wb.FormatInfo(xf.Index)
	return cv
}

func (c *MulrkCol) ValueAt(wb *WorkBook, i int) CellValue {
	if i < 0 || i >= len(c.Xfrks) {
		return CellValue{}
	}
	return c.Xfrks[i].value(wb)
}

func (c *MulBlankCol) ValueAt(_ *WorkBook, _ int) CellValue { return CellValue{} }

func (c *NumberCol) ValueAt(wb *WorkBook, _ int) CellValue {
	cv := CellValue{IsNumber: true, Number: c.Float, Text: strconv.FormatFloat(c.Float, 'f', -1, 64)}
	cv.IsDate, cv.IsPercent, cv.Unit = wb.FormatInfo(c.Index)
	return cv
}

func (c *RkCol) ValueAt(wb *WorkBook, _ int) CellValue { return c.Xfrk.value(wb) }

func (c *LabelsstCol) ValueAt(wb *WorkBook, _ int) CellValue {
	if int(c.Sst) >= len(wb.sst) {
		return CellValue{}
	}
	return CellValue{Text: wb.sst[int(c.Sst)]}
}

func (c *labelCol) ValueAt(_ *WorkBook, _ int) CellValue { return CellValue{Text: c.Str} }

func (c *BlankCol) ValueAt(_ *WorkBook, _ int) CellValue { return CellValue{} }

// classifyCustomFormat is a verbatim copy of the code != "" branch of
// sheetsource.classifyNumFmt's token rule. biffxls cannot import sheetsource
// (import cycle), so these lines are duplicated on purpose — keep the two in
// sync when either changes.
func classifyCustomFormat(code string) (date, percent bool, unit string) {
	var rest, units strings.Builder
	for i := 0; i < len(code); i++ {
		switch c := code[i]; c {
		case '"':
			j := strings.IndexByte(code[i+1:], '"')
			if j < 0 {
				j = len(code) - i - 1
			}
			lit := strings.TrimSpace(code[i+1 : i+1+j])
			if lit != "" {
				units.WriteString(lit)
			}
			i += j + 1
		case '[':
			j := strings.IndexByte(code[i:], ']')
			if j < 0 {
				j = len(code) - i - 1
			}
			sec := code[i+1 : i+j]
			if strings.HasPrefix(sec, "$") { // [$€-407] currency section
				cur, _, _ := strings.Cut(sec[1:], "-")
				if cur = strings.TrimSpace(cur); cur != "" {
					units.WriteString(cur)
				}
			}
			i += j
		case '\\':
			if i+1 < len(code) {
				e := code[i+1]
				if e != ' ' && (e < '0' || e > '9') {
					units.WriteByte(e)
				}
				i++
			}
		default:
			rest.WriteByte(c)
		}
	}
	r := strings.ToLower(rest.String())
	if strings.TrimSpace(r) == "general" {
		return false, false, strings.TrimSpace(units.String())
	}
	percent = strings.Contains(r, "%")
	date = strings.ContainsAny(r, "ydhs") || (strings.Contains(r, "m") && !strings.ContainsAny(r, "0#?"))
	return date, percent, strings.TrimSpace(units.String())
}
