package sheetsource

import "testing"

func TestClassifyNumFmt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id            int
		code          string
		date, percent bool
		unit          string
	}{
		{14, "", true, false, ""},
		{22, "", true, false, ""},
		{10, "", false, true, ""},
		{4, "", false, false, ""},
		{164, "dd.mm.yyyy", true, false, ""},
		{165, "0.00%", false, true, ""},
		{166, `#,##0.00 "€"`, false, false, "€"},
		{167, `#,##0.00 "m²"`, false, false, "m²"},
		{168, `[$€-407] #,##0.00`, false, false, "€"},
		{169, `hh:mm`, true, false, ""},
		{170, `#,##0`, false, false, ""},
		{171, `General`, false, false, ""},
		{172, `d-mmm-yy`, true, false, ""},
		{173, `0.0 "Mio."`, false, false, "Mio."},
	}
	for _, c := range cases {
		d, p, u := classifyNumFmt(c.id, c.code)
		if d != c.date || p != c.percent || u != c.unit {
			t.Errorf("classifyNumFmt(%d,%q) = %v,%v,%q want %v,%v,%q", c.id, c.code, d, p, u, c.date, c.percent, c.unit)
		}
	}
}
