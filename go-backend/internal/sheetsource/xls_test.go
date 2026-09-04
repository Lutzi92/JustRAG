package sheetsource

import (
	"reflect"
	"testing"
)

func TestXLSSourceReadsFormulaResults(t *testing.T) {
	t.Parallel()
	src, err := OpenXLS("testdata/formulas.xls")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if len(src.Sheets()) < 1 || src.Sheets()[0].Name != "Erhebung" {
		t.Fatalf("sheets = %+v", src.Sheets())
	}
	var rows [][]Cell
	_, err = src.ReadSheet(0, func(i int, cells []Cell) error {
		for len(rows) < i {
			rows = append(rows, nil)
		}
		rows = append(rows, cells)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f := rows[2][5]
	if !f.IsFormula || f.Raw == "FormulaCol" || (f.Kind != KindNumber && f.Kind != KindEmpty && f.Kind != KindText) {
		t.Errorf("F3 = %+v", f)
	}
	if rows[0][0].Raw != "Nr." {
		t.Errorf("A1 = %+v", rows[0][0])
	}
}

func TestXLSSourceSheetsAndExtras(t *testing.T) {
	t.Parallel()
	src, err := OpenXLS("testdata/formulas.xls")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	want := []SheetInfo{{Index: 0, Name: "Erhebung"}, {Index: 1, Name: "Noten", Hidden: true}}
	if got := src.Sheets(); !reflect.DeepEqual(got, want) {
		t.Errorf("Sheets() = %+v, want %+v", got, want)
	}

	var lastRow int
	ex, err := src.ReadSheet(0, func(i int, _ []Cell) error { lastRow = i; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if ex.RowCount != 15 || lastRow != 14 {
		t.Errorf("RowCount = %d, last row index = %d, want 15/14", ex.RowCount, lastRow)
	}
	// colMac would make this 8; the widest row really is A..G.
	if ex.MaxCol != 7 {
		t.Errorf("MaxCol = %d, want 7", ex.MaxCol)
	}
	if len(ex.Merged) != 1 || ex.Merged[0] != (Range{FromRow: 0, FromCol: 2, ToRow: 0, ToCol: 5}) {
		t.Errorf("Merged = %+v, want C1:F1", ex.Merged)
	}
	if len(ex.Validations) != 0 {
		t.Errorf("Validations = %+v, want none (BIFF DV records are not read)", ex.Validations)
	}
}

func TestXLSSourceCellKindsAndErrStop(t *testing.T) {
	t.Parallel()
	src, err := OpenXLS("testdata/formulas.xls")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var rows [][]Cell
	ex, err := src.ReadSheet(0, func(i int, cells []Cell) error {
		rows = append(rows, cells)
		if i == 3 {
			return ErrStop
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || ex.RowCount != 4 {
		t.Fatalf("ErrStop: delivered %d rows, RowCount = %d, want 4/4", len(rows), ex.RowCount)
	}
	if c := rows[2][3]; c.Kind != KindNumber || c.Raw != "2" || c.IsFormula {
		t.Errorf("D3 = %+v, want plain number 2", c)
	}
	if c := rows[2][6]; c.Kind != KindNumber || c.Raw != "2" || !c.IsFormula {
		t.Errorf("G3 = %+v, want formula number 2", c)
	}
	// F3 is =IF(D3>=5,D3,"") with D3 = 2, so the cached result is "".
	if c := rows[2][5]; c.Kind != KindEmpty || c.Raw != "" || !c.IsFormula {
		t.Errorf("F3 = %+v, want empty formula result", c)
	}
	if c := rows[0][0]; c.Kind != KindText || c.Raw != "Nr." {
		t.Errorf("A1 = %+v, want text", c)
	}
}

func TestXLSSourceSumFormula(t *testing.T) {
	t.Parallel()
	src, err := OpenXLS("testdata/formulas.xls")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var d15 Cell
	if _, err := src.ReadSheet(0, func(i int, cells []Cell) error {
		if i == 14 && len(cells) > 3 {
			d15 = cells[3]
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !d15.IsFormula || d15.Kind != KindNumber || d15.Raw != "46" || d15.Formatted != "46" {
		t.Errorf("D15 = %+v, want the cached SUM result 46", d15)
	}
}
