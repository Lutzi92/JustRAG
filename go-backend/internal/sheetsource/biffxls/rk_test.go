package biffxls

import (
	"math"
	"testing"
)

// rkFloat builds the RK encoding of a double whose low 34 mantissa bits are
// zero: the top 30 bits of the IEEE-754 pattern sit in bits 2..31.
func rkFloat(f float64, x100 bool) RK {
	rk := RK(uint32(math.Float64bits(f)>>34) << 2)
	if x100 {
		rk |= 1
	}
	return rk
}

// rkInt builds the RK encoding of a signed 30-bit integer payload.
func rkInt(v int32, x100 bool) RK {
	rk := RK(uint32(v<<2)) | 2
	if x100 {
		rk |= 1
	}
	return rk
}

func TestRKNumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rk      RK
		wantI   int64
		wantF   float64
		isFloat bool
		wantStr string
	}{
		{"positive int", rkInt(42, false), 42, 0, false, "42"},
		// 0xFFFFFFEE: fInt set, payload -18>>2 = -5. Without an arithmetic
		// shift this decoded as 1073741819.
		{"negative int", 0xFFFFFFEE, -5, 0, false, "-5"},
		// 0x000001F7: fInt+fX100, payload 125, so the value is 1.25.
		{"x100 int", 0x000001F7, 0, 1.25, true, "1.25"},
		{"negative x100 int", rkInt(-125, true), 0, -1.25, true, "-1.25"},
		{"float", rkFloat(2.5, false), 0, 2.5, true, "2.5"},
		{"x100 float", rkFloat(250, true), 0, 2.5, true, "2.5"},
		{"negative float", rkFloat(-2.5, false), 0, -2.5, true, "-2.5"},
		{"zero int", rkInt(0, false), 0, 0, false, "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i, f, isFloat := c.rk.number()
			if isFloat != c.isFloat {
				t.Fatalf("rk %#08x: isFloat = %v, want %v (i=%d f=%v)", uint32(c.rk), isFloat, c.isFloat, i, f)
			}
			if isFloat {
				if f != c.wantF {
					t.Errorf("rk %#08x: float = %v, want %v", uint32(c.rk), f, c.wantF)
				}
			} else if i != c.wantI {
				t.Errorf("rk %#08x: int = %d, want %d", uint32(c.rk), i, c.wantI)
			}
			if got := c.rk.String(); got != c.wantStr {
				t.Errorf("rk %#08x: String = %q, want %q", uint32(c.rk), got, c.wantStr)
			}
		})
	}
}

// TestXfRkValueUsesRKNumber pins the path Cell.Raw actually travels: an RK
// cell -> XfRk.value -> CellValue.Number.
func TestXfRkValueUsesRKNumber(t *testing.T) {
	t.Parallel()
	wb := &WorkBook{Formats: map[uint16]*Format{}}
	wb.addXf(&Xf8{Format: 0}) // "General"
	for _, c := range []struct {
		name string
		rk   RK
		want float64
	}{
		{"negative int", 0xFFFFFFEE, -5},
		{"x100 int", 0x000001F7, 1.25},
		{"float", rkFloat(2.5, false), 2.5},
	} {
		xf := XfRk{Index: 0, Rk: c.rk}
		if got := xf.value(wb); got.Number != c.want || !got.IsNumber {
			t.Errorf("%s: XfRk.value = %+v, want Number %v", c.name, got, c.want)
		}
	}
}
