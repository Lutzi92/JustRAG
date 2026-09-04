package profile

import (
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
