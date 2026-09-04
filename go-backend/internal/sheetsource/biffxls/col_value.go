package biffxls

// FORK ADDITION (not upstream): typed access for the content handlers, plus
// the number-format classifier the typed accessors need. Each ValueAt mirrors
// the corresponding String method but keeps the number/date/bool distinction.

import "strconv"

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
