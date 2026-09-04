package sheetsource

import "testing"

// TestPercentRaw pins the one canonicalisation every reader must share.
// Before this helper the xlsx reader rounded to 10 decimals while the ods and
// xls readers formatted f*100 directly, so the same 7 % cell read back as "7"
// from one file format and "7.000000000000001" from another.
func TestPercentRaw(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   float64
		want string
	}{
		{0.07, "7"},  // 0.07*100 == 7.000000000000001
		{0.29, "29"}, // 0.29*100 == 28.999999999999996
		{0.365, "36.5"},
		{0.42, "42"},
		{0, "0"},
		{1, "100"},
		{0.12345, "12.345"},
		{-0.07, "-7"},
	}
	for _, c := range cases {
		if got := percentRaw(c.in); got != c.want {
			t.Errorf("percentRaw(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
