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
	}{
		{`General`, false, false},
		{`0.00`, false, false},
		{`0.0%`, false, true},
		{`DD.MM.YYYY`, true, false},
		{`hh:mm:ss`, true, false},
		{`#,##0.00\ "€"`, false, false}, // the XfRk.String bug: not a date
		{`#,##0.00\ [$€-407]`, false, false},
		{`0 "m"`, false, false}, // literal m is a unit, not a month
	}
	for _, c := range cases {
		d, p, _ := classifyCustomFormat(c.code)
		if d != c.date || p != c.percent {
			t.Errorf("classifyCustomFormat(%q) = date:%v percent:%v, want date:%v percent:%v", c.code, d, p, c.date, c.percent)
		}
	}
}
