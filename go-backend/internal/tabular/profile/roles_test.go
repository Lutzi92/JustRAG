package profile

import (
	"fmt"
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
)

func col(vals ...string) []sheetsource.Cell {
	out := make([]sheetsource.Cell, len(vals))
	for i, v := range vals {
		out[i] = sheetsource.Cell{Kind: sheetsource.KindText, Raw: v, Formatted: v}
		if v == "" {
			out[i].Kind = sheetsource.KindEmpty
		}
	}
	return out
}

func sampleFromColumns(headers []string, cols ...[]sheetsource.Cell) (*sheetsource.Sample, Region, headerBlock) {
	n := len(cols[0])
	rows := make([][]sheetsource.Cell, n+1)
	rows[0] = make([]sheetsource.Cell, len(cols))
	for c, h := range headers {
		rows[0][c] = sheetsource.Cell{Kind: sheetsource.KindText, Raw: h, Formatted: h, Style: sheetsource.CellStyle{Bold: true}}
	}
	for r := 0; r < n; r++ {
		rows[r+1] = make([]sheetsource.Cell, len(cols))
		for c := range cols {
			rows[r+1][c] = cols[c][r]
		}
	}
	s := &sheetsource.Sample{Rows: rows, Width: len(cols), TotalRows: n + 1}
	reg := Region{0, 0, n, len(cols) - 1, true}
	hb := headerBlock{Rows: []int{0}, DataStart: 1, IndexRow: -1, Headers: headers}
	for c := range cols {
		hb.Columns = append(hb.Columns, c)
	}
	return s, reg, hb
}

func TestAssignRoles(t *testing.T) {
	t.Parallel()
	numCells := func(vals ...string) []sheetsource.Cell {
		out := col(vals...)
		for i := range out {
			if out[i].Kind != sheetsource.KindEmpty {
				out[i].Kind = sheetsource.KindNumber
			}
		}
		return out
	}
	s, reg, hb := sampleFromColumns(
		[]string{"Lieferantennummer", "Material", "GIS-Code", "Beschreibung", "BGF", "Denkmalschutz", "Aktiv", "Stand", "Fläche", "Bemerkung"},
		col("0002001919", "0002001920", "0002001921", "0002001922", "0002001923", "0002001924"),
		numCells("931404826", "931404827", "931404828", "931404829", "931404830", "931404831"),
		col("01.1440.055_.10", "01.1675.003A.10", "01.2590.021A.12", "01.3610.001_.10", "01.3610.003_.10", "02.0001.006_.10"),
		col("Institutsgebäude", "Nebengebäude", "Trafostation", "Neues Schloss", "Zeughaus", "Institutsgebäude"),
		numCells("2143.28", "822.18", "832.7", "1265.87", "4407.6", "700"),
		col("Nein", "Einzelkulturdenkmal", "Nein", "Ensembleschutz", "Nein", "Nein"),
		col("Ja", "Nein", "Ja", "Ja", "Nein", "Ja"),
		col("14.03.2025", "15.03.2025", "16.03.2025", "17.03.2025", "18.03.2025", ""),
		col("12,5", "1.234,75", "3,0", "7", "8,25", "n/a"),
		col("Sanierung erforderlich, ELT / DV", "2014 komplett saniert", "Abgabe erfolgt", "keine", "siehe Anlage 3", "Rückbau geplant 2027"),
	)
	cols := AssignRoles(s, reg, hb)
	want := []Role{RoleID, RoleID, RoleID, RoleText, RoleMeasure, RoleCategory, RoleBool, RoleDate, RoleMeasure, RoleText}
	for i, w := range want {
		if cols[i].Role != w {
			t.Errorf("%s: role = %s want %s (stats %+v)", cols[i].Header, cols[i].Role, w, cols[i].Stats)
		}
	}
	if !cols[8].DecimalComma {
		t.Error("Fläche must be flagged decimal-comma")
	}
	if cols[3].Role == RoleCategory {
		t.Error("6 distinct of 6 must not be a category")
	}
}

// TestAssignRolesCategoryThreshold covers controller ruling #1 (fix round
// 1): Category fires at n >= 5 && distinct <= 50 && distinct <= max(3,
// 0.2*n), not a flat 50% ratio. 100 distinct of 200 stays Text; 12 distinct
// of 200 is Category (max(3, 0.2*200=40) admits 12 but not 100).
func TestAssignRolesCategoryThreshold(t *testing.T) {
	t.Parallel()
	repeating := func(distinctCount int) []sheetsource.Cell {
		vals := make([]string, 200)
		for i := range vals {
			vals[i] = fmt.Sprintf("Name %d", i%distinctCount)
		}
		return col(vals...)
	}

	s, reg, hb := sampleFromColumns([]string{"Bezeichnung"}, repeating(100))
	cols := AssignRoles(s, reg, hb)
	if cols[0].Role != RoleText {
		t.Errorf("100 distinct of 200: role = %s want %s (stats %+v)", cols[0].Role, RoleText, cols[0].Stats)
	}

	s2, reg2, hb2 := sampleFromColumns([]string{"Bezeichnung"}, repeating(12))
	cols2 := AssignRoles(s2, reg2, hb2)
	if cols2[0].Role != RoleCategory {
		t.Errorf("12 distinct of 200: role = %s want %s (stats %+v)", cols2[0].Role, RoleCategory, cols2[0].Stats)
	}
}

// TestAssignRolesIDPatternTextOnly covers controller ruling #2 (fix round
// 1): an idValueRe hit only counts as ID evidence for untyped KindText
// cells that don't independently parse as a number (either decimal style)
// or a date. Every CSV cell is KindText (internal/sheetsource/csv.go), so a
// CSV column of plain decimals like "2143.28" must not be misread as an ID
// column via the digit.digit shape idValueRe also matches.
func TestAssignRolesIDPatternTextOnly(t *testing.T) {
	t.Parallel()
	s, reg, hb := sampleFromColumns([]string{"BGF"},
		col("2143.28", "822.18", "832.7", "1265.87", "4407.6", "700"))
	cols := AssignRoles(s, reg, hb)
	if cols[0].Role != RoleMeasure {
		t.Errorf("role = %s want %s (stats %+v)", cols[0].Role, RoleMeasure, cols[0].Stats)
	}
	if cols[0].Stats.IDPattern != 0 {
		t.Errorf("Stats.IDPattern = %d want 0", cols[0].Stats.IDPattern)
	}
}

// TestAssignRolesFixedWidthNeedsThreeValues covers controller ruling #3
// (fix round 1): fixedWidthCodes must restore the brief's n >= 3 guard —
// two same-width all-digit values must not be mistaken for a fixed-width ID
// code arm; both are plain numbers, so the column should read as Measure.
func TestAssignRolesFixedWidthNeedsThreeValues(t *testing.T) {
	t.Parallel()
	s, reg, hb := sampleFromColumns([]string{"Wert"}, col("123456", "654321"))
	cols := AssignRoles(s, reg, hb)
	if cols[0].Role != RoleMeasure {
		t.Errorf("role = %s want %s (stats %+v)", cols[0].Role, RoleMeasure, cols[0].Stats)
	}
}
