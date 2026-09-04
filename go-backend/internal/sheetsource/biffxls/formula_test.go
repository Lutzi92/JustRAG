package biffxls

import (
	"os"
	"testing"
)

func TestFormulaCellsReturnCachedResults(t *testing.T) {
	t.Parallel()
	f, err := os.Open("../testdata/formulas.xls")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	wb, err := OpenReader(f, "utf-8")
	if err != nil {
		t.Fatal(err)
	}
	sh := wb.GetSheet(0)
	if sh == nil {
		t.Fatal("no sheet")
	}
	num, empty := 0, 0
	for r := 2; r < 14; r++ {
		cv, ok := sh.CellAt(r, 5) // column F: =IF(D>=5,D,"")
		if !ok || !cv.IsFormula {
			t.Fatalf("row %d col F: %+v ok=%v", r, cv, ok)
		}
		if cv.Text == "FormulaCol" {
			t.Fatalf("row %d still returns the FormulaCol stub", r)
		}
		if cv.IsNumber {
			num++
		} else if cv.Text == "" {
			empty++
		}
	}
	if num == 0 || empty == 0 {
		t.Errorf("want numeric and empty-string results, got num=%d empty=%d", num, empty)
	}
	sum, ok := sh.CellAt(14, 3) // D15 =SUM(D3:D14)
	if !ok || !sum.IsFormula || !sum.IsNumber || sum.Number <= 0 {
		t.Errorf("SUM = %+v", sum)
	}
}

func TestSheetVisibilityAndMerges(t *testing.T) {
	t.Parallel()
	f, err := os.Open("../testdata/formulas.xls")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	wb, err := OpenReader(f, "utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if n := wb.NumSheets(); n != 2 {
		t.Fatalf("NumSheets = %d, want 2", n)
	}
	// The BOUNDSHEET grbit bytes are easy to read the wrong way round; the
	// fixture's second sheet ("Noten") is hidden and the first is not.
	if sh := wb.GetSheet(0); sh.Name != "Erhebung" || sh.Hidden() {
		t.Errorf("sheet 0 = %q hidden=%v, want Erhebung visible", sh.Name, sh.Hidden())
	}
	if sh := wb.GetSheet(1); sh.Name != "Noten" || !sh.Hidden() {
		t.Errorf("sheet 1 = %q hidden=%v, want Noten hidden", sh.Name, sh.Hidden())
	}
	// MERGEDCELLS (0x0E5): the first header row merges C1:F1.
	got := wb.GetSheet(0).Merged
	want := [][4]int{{0, 2, 0, 5}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Merged = %v, want %v", got, want)
	}
}

func TestLastDefinedColIgnoresColMac(t *testing.T) {
	t.Parallel()
	f, err := os.Open("../testdata/formulas.xls")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	wb, err := OpenReader(f, "utf-8")
	if err != nil {
		t.Fatal(err)
	}
	// Row 3 spans A..G; the ROW record's colMac is 7 (one past the last
	// defined column), which would size the row one cell too wide.
	row := wb.GetSheet(0).Row(2)
	if row == nil {
		t.Fatal("row 3 missing")
	}
	if got := row.LastDefinedCol(); got != 6 {
		t.Errorf("LastDefinedCol = %d, want 6", got)
	}
	if got := row.LastCol(); got != 7 {
		t.Errorf("LastCol (colMac) = %d, want 7 — the off-by-one this guards", got)
	}
	// Rows past the end must not panic (upstream dereferenced a nil map hit).
	if r := wb.GetSheet(0).Row(9999); r != nil {
		t.Errorf("Row(9999) = %v, want nil", r)
	}
}

func TestClassifyCustomFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code          string
		date, percent bool
		unit          string
	}{
		{`General`, false, false, ""},
		{`0.00`, false, false, ""},
		{`0.0%`, false, true, ""},
		{`DD.MM.YYYY`, true, false, ""},
		{`hh:mm:ss`, true, false, ""},
		{`#,##0.00\ "€"`, false, false, "€"}, // the XfRk.String bug: not a date
		{`#,##0.00\ [$€-407]`, false, false, "€"},
		{`0 "m"`, false, false, "m"}, // literal m is a unit, not a month
	}
	for _, c := range cases {
		d, p, u := classifyCustomFormat(c.code)
		if d != c.date || p != c.percent || u != c.unit {
			t.Errorf("classifyCustomFormat(%q) = date:%v percent:%v unit:%q, want date:%v percent:%v unit:%q",
				c.code, d, p, u, c.date, c.percent, c.unit)
		}
	}
}

// FormatInfo is what carries the unit out to the profiler; the fixture has no
// unit-formatted cell (its only custom format is "General"), so the wiring is
// pinned on a synthetic workbook instead.
func TestFormatInfoReportsUnit(t *testing.T) {
	t.Parallel()
	wb := &WorkBook{Formats: map[uint16]*Format{}}
	wb.addXf(&Xf8{Format: 164}) // xf 0 -> custom currency
	wb.addXf(&Xf8{Format: 9})   // xf 1 -> builtin percent
	wb.addXf(&Xf8{Format: 14})  // xf 2 -> builtin date
	cur := &Format{str: `#,##0.00\ "€"`}
	cur.Head.Index = 164
	wb.addFormat(cur)

	for _, c := range []struct {
		xf            uint16
		date, percent bool
		unit          string
	}{
		{0, false, false, "€"},
		{1, false, true, ""},
		{2, true, false, ""},
		{99, false, false, ""}, // out of range
	} {
		d, p, u := wb.FormatInfo(c.xf)
		if d != c.date || p != c.percent || u != c.unit {
			t.Errorf("FormatInfo(%d) = date:%v percent:%v unit:%q, want date:%v percent:%v unit:%q",
				c.xf, d, p, u, c.date, c.percent, c.unit)
		}
		if dd, pp := wb.FormatIsDate(c.xf); dd != c.date || pp != c.percent {
			t.Errorf("FormatIsDate(%d) = %v/%v, want %v/%v", c.xf, dd, pp, c.date, c.percent)
		}
	}

	// And the unit must survive into the typed cell value.
	n := &NumberCol{Index: 0, Float: 12.5}
	if got := n.ValueAt(wb, 0); got.Unit != "€" || got.Number != 12.5 {
		t.Errorf("NumberCol.ValueAt = %+v, want Number 12.5 Unit €", got)
	}
}
