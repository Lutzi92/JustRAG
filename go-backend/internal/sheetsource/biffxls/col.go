package biffxls

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"time"
)

// content type
type contentHandler interface {
	String(*WorkBook) []string
	FirstCol() uint16
	LastCol() uint16
}

type Col struct {
	RowB      uint16
	FirstColB uint16
}

type Coler interface {
	Row() uint16
}

func (c *Col) Row() uint16 {
	return c.RowB
}

func (c *Col) FirstCol() uint16 {
	return c.FirstColB
}

func (c *Col) LastCol() uint16 {
	return c.FirstColB
}

func (c *Col) String(wb *WorkBook) []string {
	return []string{"default"}
}

type XfRk struct {
	Index uint16
	Rk    RK
}

func (xf *XfRk) String(wb *WorkBook) string {
	idx := int(xf.Index)
	if len(wb.Xfs) > idx {
		fNo := wb.Xfs[idx].formatNo()
		if fNo >= 164 { // user defined format
			// FORK FIX: upstream rendered *every* user-defined format as a
			// date, so a currency or thousands format came back as an RFC3339
			// timestamp. Only render a date when the format code says so.
			if cf := wb.Formats[fNo]; cf != nil {
				if isDate, _, _ := classifyCustomFormat(cf.str); isDate {
					i, f, isFloat := xf.Rk.number()
					if !isFloat {
						f = float64(i)
					}
					t := timeFromExcelTime(f, wb.dateMode == 1)

					return t.Format(time.RFC3339) //TODO it should be international and format as the describled style
				}
			}
			// see http://www.openoffice.org/sc/excelfileformat.pdf
		} else if 14 <= fNo && fNo <= 17 || fNo == 22 || 27 <= fNo && fNo <= 36 || 50 <= fNo && fNo <= 58 { // jp. date format
			i, f, isFloat := xf.Rk.number()
			if !isFloat {
				f = float64(i)
			}
			t := timeFromExcelTime(f, wb.dateMode == 1)
			return t.Format("2006.01") //TODO it should be international
		}
	}
	return xf.Rk.String()
}

type RK uint32

func (rk RK) number() (intNum int64, floatNum float64, isFloat bool) {
	multiplied := rk & 1
	isInt := rk & 2
	val := rk >> 2
	if isInt == 0 {
		isFloat = true
		floatNum = math.Float64frombits(uint64(val) << 34)
		if multiplied != 0 {
			floatNum = floatNum / 100
		}
		return
	}
	return int64(val), 0, false
}

func (rk RK) String() string {
	i, f, isFloat := rk.number()
	if isFloat {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatInt(i, 10)
}

var ErrIsInt = fmt.Errorf("is int")

func (rk RK) Float() (float64, error) {
	_, f, isFloat := rk.number()
	if !isFloat {
		return 0, ErrIsInt
	}
	return f, nil
}

type MulrkCol struct {
	Col
	Xfrks    []XfRk
	LastColB uint16
}

func (c *MulrkCol) LastCol() uint16 {
	return c.LastColB
}

func (c *MulrkCol) String(wb *WorkBook) []string {
	var res = make([]string, len(c.Xfrks))
	for i := 0; i < len(c.Xfrks); i++ {
		xfrk := c.Xfrks[i]
		res[i] = xfrk.String(wb)
	}
	return res
}

type MulBlankCol struct {
	Col
	Xfs      []uint16
	LastColB uint16
}

func (c *MulBlankCol) LastCol() uint16 {
	return c.LastColB
}

func (c *MulBlankCol) String(wb *WorkBook) []string {
	return make([]string, len(c.Xfs))
}

type NumberCol struct {
	Col
	Index uint16
	Float float64
}

func (c *NumberCol) String(wb *WorkBook) []string {
	return []string{strconv.FormatFloat(c.Float, 'f', -1, 64)}
}

type FormulaCol struct {
	Header struct {
		Col
		IndexXf uint16
		Result  [8]byte
		Flags   uint16
		_       uint32
	}
	Bts []byte
	// Str is filled from the STRING record (0x0207) that follows the FORMULA
	// record when the cached result is a string. FORK ADDITION.
	Str *string
}

// FORK FIX: Col is embedded inside the anonymous Header struct, so its methods
// promote to Header, not to *FormulaCol. Without these three delegations
// *FormulaCol satisfies neither contentHandler nor Coler, and WorkSheet.add
// silently dropped every formula cell.
func (c *FormulaCol) Row() uint16 { return c.Header.Col.Row() }

func (c *FormulaCol) FirstCol() uint16 { return c.Header.Col.FirstCol() }

func (c *FormulaCol) LastCol() uint16 { return c.Header.Col.LastCol() }

var biffErrors = map[byte]string{
	0x00: "#NULL!",
	0x07: "#DIV/0!",
	0x0F: "#VALUE!",
	0x17: "#REF!",
	0x1D: "#NAME?",
	0x24: "#NUM!",
	0x2A: "#N/A",
}

// Value decodes the cached result of the formula. FORK ADDITION.
func (c *FormulaCol) Value(wb *WorkBook) CellValue {
	r := c.Header.Result
	cv := CellValue{IsFormula: true}
	if r[6] == 0xFF && r[7] == 0xFF {
		switch r[0] {
		case 0: // string; the value rides the following STRING record
			if c.Str != nil {
				cv.Text = *c.Str
			}
		case 1:
			cv.IsBool = true
			cv.Text = "false"
			if r[2] != 0 {
				cv.Text = "true"
			}
		case 2:
			cv.IsError = true
			cv.Text = biffErrors[r[2]]
			if cv.Text == "" {
				cv.Text = "#ERR"
			}
		case 3: // empty string
			cv.Text = ""
		}
		return cv
	}
	cv.IsNumber = true
	cv.Number = math.Float64frombits(binary.LittleEndian.Uint64(r[:]))
	cv.IsDate, cv.IsPercent = wb.FormatIsDate(c.Header.IndexXf)
	cv.Text = strconv.FormatFloat(cv.Number, 'f', -1, 64)
	return cv
}

func (c *FormulaCol) ValueAt(wb *WorkBook, _ int) CellValue { return c.Value(wb) }

func (c *FormulaCol) String(wb *WorkBook) []string {
	return []string{c.Value(wb).Text}
}

type RkCol struct {
	Col
	Xfrk XfRk
}

func (c *RkCol) String(wb *WorkBook) []string {
	return []string{c.Xfrk.String(wb)}
}

type LabelsstCol struct {
	Col
	Xf  uint16
	Sst uint32
}

func (c *LabelsstCol) String(wb *WorkBook) []string {
	return []string{wb.sst[int(c.Sst)]}
}

type labelCol struct {
	BlankCol
	Str string
}

func (c *labelCol) String(wb *WorkBook) []string {
	return []string{c.Str}
}

type BlankCol struct {
	Col
	Xf uint16
}

func (c *BlankCol) String(wb *WorkBook) []string {
	return []string{""}
}
