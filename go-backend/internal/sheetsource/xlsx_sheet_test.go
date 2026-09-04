package sheetsource

import (
	"strings"
	"testing"
)

func readAll(t *testing.T, path string, sheet int) ([][]Cell, SheetExtras) {
	t.Helper()
	src, err := OpenXLSX(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var rows [][]Cell
	ex, err := src.ReadSheet(sheet, func(i int, cells []Cell) error {
		for len(rows) < i {
			rows = append(rows, nil)
		}
		rows = append(rows, cells)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows, ex
}

func TestXLSXHeaderRow14Layout(t *testing.T) {
	t.Parallel()
	src, err := OpenXLSX("testdata/header_row14_metadata.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	infos := src.Sheets()
	if len(infos) != 3 || infos[1].Name != "Dropdown" || !infos[1].Hidden {
		t.Fatalf("sheets: %+v", infos)
	}
	rows, ex := readAll(t, "testdata/header_row14_metadata.xlsx", 0)
	if len(rows) < 34 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0] != nil || rows[2] != nil {
		t.Error("blank rows 1 and 3 must be gaps")
	}
	if got := rows[1][1].Raw; got != "Gesamtliste landeseigene Gebäude" {
		t.Errorf("B2 = %q", got)
	}
	if got := rows[13][1].Raw; got != "Ressort" || !rows[13][1].Style.Bold || !rows[13][1].Style.Filled {
		t.Errorf("B14 = %+v", rows[13][1])
	}
	if rows[14][0].Kind != KindNumber || rows[14][0].Raw != "1" {
		t.Errorf("A15 = %+v", rows[14][0])
	}
	if rows[14][15].Raw != "Nein" || rows[14][15].Kind != KindText {
		t.Errorf("P15 = %+v", rows[14][15])
	}
	if len(ex.Merged) != 3 {
		t.Errorf("merged = %+v", ex.Merged)
	}
	if len(ex.Validations) != 2 {
		t.Fatalf("validations = %+v", ex.Validations)
	}
	refs := ex.Validations[0].Ref + " " + ex.Validations[1].Ref
	if !strings.Contains(refs, "Dropdown!$C$6:$C$8") || !strings.Contains(refs, `"vor 1977,von 1977 bis 2010,ab 2010"`) {
		t.Errorf("validation refs = %q", refs)
	}
	if ex.MaxCol < 20 || ex.RowCount != 34 {
		t.Errorf("maxcol=%d rowcount=%d", ex.MaxCol, ex.RowCount)
	}
}

func TestXLSXNumbersFormats(t *testing.T) {
	t.Parallel()
	rows, _ := readAll(t, "testdata/numbers_formats.xlsx", 0)
	r := rows[1]
	if r[1].Kind != KindNumber || r[1].Raw != "2144.28" {
		t.Errorf("BGF = %+v", r[1])
	}
	if r[2].Kind != KindNumber || r[2].Raw != "36.5" || !r[2].Style.Percent || r[2].Formatted != "36.5%" {
		t.Errorf("Anteil = %+v", r[2])
	}
	if r[3].Kind != KindDate || r[3].Raw != "2025-03-15" {
		t.Errorf("Stand = %+v", r[3])
	}
	if r[4].Kind != KindDate || r[4].Raw != "2025-03-15" {
		t.Errorf("Datum2 = %+v", r[4])
	}
	if rows[2][5].Kind != KindText || rows[2][5].Raw != "2007; Anbau 2018" {
		t.Errorf("Baujahr mixed = %+v", rows[2][5])
	}
	if r[6].Style.Unit != "€" {
		t.Errorf("Betrag unit = %+v", r[6].Style)
	}
	if !rows[0][0].Style.Bold || !rows[0][0].Style.BottomBorder {
		t.Errorf("header style = %+v", rows[0][0].Style)
	}
}

func TestXLSXFormulas(t *testing.T) {
	t.Parallel()
	rows, _ := readAll(t, "testdata/multirow_header_formulas.xlsx", 0)
	c := rows[2][5] // F3
	if !c.IsFormula || !strings.HasPrefix(strings.ToUpper(c.Formula), "IF(") {
		t.Errorf("F3 = %+v", c)
	}
	emptyResult, numResult := false, false
	for i := 2; i < 14; i++ {
		f := rows[i][5]
		if !f.IsFormula {
			t.Fatalf("row %d F not a formula: %+v", i+1, f)
		}
		if f.Kind == KindEmpty || (f.Kind == KindText && f.Raw == "") {
			emptyResult = true
		}
		if f.Kind == KindNumber {
			numResult = true
		}
	}
	if !emptyResult || !numResult {
		t.Errorf("expected both empty-string and numeric cached results (empty=%v num=%v)", emptyResult, numResult)
	}
	sum := rows[14][3] // D15 =SUM(D3:D14)
	if !sum.IsFormula || sum.Kind != KindNumber {
		t.Errorf("SUM = %+v", sum)
	}
	uncached, _ := readAll(t, "testdata/uncached_formulas.xlsx", 0)
	u := uncached[1][2]
	if !u.IsFormula || u.Kind != KindEmpty {
		t.Errorf("uncached C2 = %+v (want IsFormula && KindEmpty)", u)
	}
}

func TestXLSXLeadingZeroIDs(t *testing.T) {
	t.Parallel()
	rows, _ := readAll(t, "testdata/ids_leading_zero.xlsx", 0)
	if rows[1][0].Kind != KindText || rows[1][0].Raw != "0002001919" {
		t.Errorf("Lieferantennummer = %+v", rows[1][0])
	}
	if rows[1][1].Kind != KindNumber || rows[1][1].Raw != "931404826" {
		t.Errorf("Material = %+v", rows[1][1])
	}
	if rows[1][2].Raw != "01.1440.055_.10" {
		t.Errorf("GIS = %+v", rows[1][2])
	}
}
