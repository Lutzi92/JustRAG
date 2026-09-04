package biffxls

import (
	"encoding/binary"
	"math"
	"testing"
)

// errResult builds a FORMULA cached-result blob for error code c.
func errResult(c byte) [8]byte { return [8]byte{2, 0, c, 0, 0, 0, 0xFF, 0xFF} }

func TestFormulaColValue(t *testing.T) {
	t.Parallel()
	wb := &WorkBook{Formats: map[uint16]*Format{}}
	wb.addXf(&Xf8{Format: 0}) // "General": neither date nor percent

	str := "Ja"
	numeric := [8]byte{}
	binary.LittleEndian.PutUint64(numeric[:], math.Float64bits(2.5))

	cases := []struct {
		name   string
		result [8]byte
		str    *string
		want   CellValue
	}{
		{"string result", [8]byte{0, 0, 0, 0, 0, 0, 0xFF, 0xFF}, &str,
			CellValue{IsFormula: true, Text: "Ja"}},
		{"string result without STRING record", [8]byte{0, 0, 0, 0, 0, 0, 0xFF, 0xFF}, nil,
			CellValue{IsFormula: true, Text: ""}},
		{"bool true", [8]byte{1, 0, 1, 0, 0, 0, 0xFF, 0xFF}, nil,
			CellValue{IsFormula: true, IsBool: true, Text: "true"}},
		{"bool false", [8]byte{1, 0, 0, 0, 0, 0, 0xFF, 0xFF}, nil,
			CellValue{IsFormula: true, IsBool: true, Text: "false"}},
		{"err NULL", errResult(0x00), nil, CellValue{IsFormula: true, IsError: true, Text: "#NULL!"}},
		{"err DIV0", errResult(0x07), nil, CellValue{IsFormula: true, IsError: true, Text: "#DIV/0!"}},
		{"err VALUE", errResult(0x0F), nil, CellValue{IsFormula: true, IsError: true, Text: "#VALUE!"}},
		{"err REF", errResult(0x17), nil, CellValue{IsFormula: true, IsError: true, Text: "#REF!"}},
		{"err NAME", errResult(0x1D), nil, CellValue{IsFormula: true, IsError: true, Text: "#NAME?"}},
		{"err NUM", errResult(0x24), nil, CellValue{IsFormula: true, IsError: true, Text: "#NUM!"}},
		{"err NA", errResult(0x2A), nil, CellValue{IsFormula: true, IsError: true, Text: "#N/A"}},
		{"err unknown", errResult(0x99), nil, CellValue{IsFormula: true, IsError: true, Text: "#ERR"}},
		{"empty string", [8]byte{3, 0, 0, 0, 0, 0, 0xFF, 0xFF}, nil,
			CellValue{IsFormula: true, Text: ""}},
		{"numeric", numeric, nil,
			CellValue{IsFormula: true, IsNumber: true, Number: 2.5, Text: "2.5"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var fc FormulaCol
			fc.Header.Result = c.result
			fc.Str = c.str
			if got := fc.Value(wb); got != c.want {
				t.Errorf("Value = %+v, want %+v", got, c.want)
			}
			if got := fc.String(wb); len(got) != 1 || got[0] != c.want.Text {
				t.Errorf("String = %q, want [%q]", got, c.want.Text)
			}
		})
	}
}

// The 0xFFFF marker only applies to bytes 6-7; a double whose top two bytes
// happen to be 0xFFFF is a NaN, which is exactly what that encoding reserves.
func TestFormulaColValueNumericIsNotMisreadAsMarker(t *testing.T) {
	t.Parallel()
	wb := &WorkBook{Formats: map[uint16]*Format{}}
	var fc FormulaCol
	binary.LittleEndian.PutUint64(fc.Header.Result[:], math.Float64bits(-1234.5))
	got := fc.Value(wb)
	if !got.IsNumber || got.Number != -1234.5 || got.Text != "-1234.5" {
		t.Errorf("Value = %+v, want the double -1234.5", got)
	}
}
