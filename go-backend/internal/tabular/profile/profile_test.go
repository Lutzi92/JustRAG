package profile

import (
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
)

func TestProfileSheetTableFormProse(t *testing.T) {
	t.Parallel()
	table := grid(
		"Gebäude|Baujahr|BGF",
		"A|#|#",
		"B|#|#",
		"C|#|#",
		"Summe|.|#",
	)
	boldRow(table, 0)
	table.Rows[4][2].IsFormula, table.Rows[4][2].Formula = true, "SUM(C2:C4)"
	p := ProfileSheet(table, Options{})
	if p.Kind != KindTable || len(p.Regions) != 1 || p.Regions[0].Kind != KindTable {
		t.Fatalf("profile = %+v", p)
	}
	rp := p.Regions[0]
	if len(rp.Columns) != 3 || rp.Columns[2].Role != RoleMeasure || rp.Columns[0].Header != "Gebäude" {
		t.Errorf("columns = %+v", rp.Columns)
	}
	if len(rp.DerivedRows) != 1 || rp.DerivedRows[0] != 4 {
		t.Errorf("derived = %v", rp.DerivedRows)
	}

	form := grid(
		".|Steckbrief|.|.",
		".|.|.|.",
		".|Gebäude|Physiologie|.",
		".|Adresse|Aulweg 129|.",
		".|Baujahr|#|.",
		".|.|.|.",
		".|Fläche|#|.",
		".|Bewertung|mittel|.",
	)
	form.Extras.Merged = []sheetsource.Range{
		{FromRow: 0, FromCol: 1, ToRow: 0, ToCol: 3},
		{FromRow: 6, FromCol: 2, ToRow: 6, ToCol: 3},
	}
	fp := ProfileSheet(form, Options{})
	if fp.Kind != KindForm {
		t.Errorf("form kind = %s (%+v)", fp.Kind, fp.Regions)
	}

	prose := grid(".|Ausfüllhinweise", ".|Zu erfassen sind alle Gebäude.", ".|Bitte je Zeile ein Gebäude.")
	if pp := ProfileSheet(prose, Options{}); pp.Kind != KindProse {
		t.Errorf("prose kind = %s", pp.Kind)
	}
	if ep := ProfileSheet(grid(".|.", ".|."), Options{}); ep.Kind != KindEmpty {
		t.Errorf("empty kind = %s", ep.Kind)
	}
}
