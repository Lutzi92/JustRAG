package numfmt

import "testing"

// classifyCases is shared by the table test and the fuzz seed corpus.
var classifyCases = []struct {
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
	// Truncated / malformed codes: an unterminated '[' section used to slice
	// backwards and panic (review finding A).
	{174, `0.00[`, false, false, ""},
	{175, `[`, false, false, ""},
	{176, `0.00[$`, false, false, ""},
	{177, `[$€-407`, false, false, "€"},
	// Relocated from biffxls.TestClassifyCustomFormat (the fork's copy of this
	// tokenizer is gone; these codes exercise the backslash-escape branch).
	{164, `0.00`, false, false, ""},
	{164, `0.0%`, false, true, ""},
	{164, `DD.MM.YYYY`, true, false, ""},
	{164, `hh:mm:ss`, true, false, ""},
	{164, `#,##0.00\ "€"`, false, false, "€"}, // the XfRk.String bug: not a date
	{164, `#,##0.00\ [$€-407]`, false, false, "€"},
	{164, `0 "m"`, false, false, "m"}, // literal m is a unit, not a month
}

func TestClassify(t *testing.T) {
	t.Parallel()
	for _, c := range classifyCases {
		d, p, u := Classify(c.id, c.code)
		if d != c.date || p != c.percent || u != c.unit {
			t.Errorf("Classify(%d,%q) = %v,%v,%q want %v,%v,%q", c.id, c.code, d, p, u, c.date, c.percent, c.unit)
		}
	}
}

func TestIsBuiltin(t *testing.T) {
	t.Parallel()
	for _, id := range []int{9, 10, 14, 22, 27, 36, 45, 47, 50, 58} {
		if !IsBuiltin(id) {
			t.Errorf("IsBuiltin(%d) = false, want true", id)
		}
	}
	for _, id := range []int{0, 4, 8, 11, 13, 23, 26, 44, 48, 49, 59, 164} {
		if IsBuiltin(id) {
			t.Errorf("IsBuiltin(%d) = true, want false", id)
		}
	}
}

// FuzzClassify asserts Classify never panics on an arbitrary format code —
// codes reach it straight out of an untrusted spreadsheet's styles part.
func FuzzClassify(f *testing.F) {
	for _, c := range classifyCases {
		f.Add(c.id, c.code)
	}
	f.Add(164, `\`)
	f.Add(164, `"`)
	f.Add(164, `[]`)
	f.Fuzz(func(t *testing.T, id int, code string) {
		Classify(id, code)
	})
}
