package profile

import (
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
)

func boldRow(s *sheetsource.Sample, r int) {
	for c := range s.Rows[r] {
		if !s.Rows[r][c].IsEmpty() {
			s.Rows[r][c].Style.Bold = true
		}
	}
}

func TestHeaderBlockRow14Shape(t *testing.T) {
	t.Parallel()
	s := grid(
		".|Gesamtliste landeseigene Gebäude|.|.|.|.",
		".|.|.|.|.|.",
		".|Stand:|03-14-25|.|.|.",
		".|.|.|.|.|.",
		".|Stammdaten|.|.|.|.",
		".|1|.|2|3|4",
		".|Ressort|.|Bezeichnung|Straße|BGF",
		"#|HMWK|.|Gebäude 1|Musterstraße|#",
		"#|HMWK|.|Gebäude 2|Musterstraße|#",
		"#|HMWK|.|Gebäude 3|Musterstraße|#",
	)
	s.Extras.Merged = []sheetsource.Range{
		{FromRow: 0, FromCol: 1, ToRow: 0, ToCol: 4},
		{FromRow: 4, FromCol: 1, ToRow: 4, ToCol: 5},
	}
	boldRow(s, 6)
	// index row cells must be numbers, not text
	for c := 1; c < 6; c++ {
		if !s.Rows[5][c].IsEmpty() {
			s.Rows[5][c].Kind = sheetsource.KindNumber
		}
	}
	reg := Region{Top: 0, Left: 0, Bottom: 9, Right: 5, OpenEnded: true}
	hb, ok := detectHeaderBlock(s, reg, 3)
	if !ok {
		t.Fatal("no header found")
	}
	if len(hb.Rows) != 2 || hb.Rows[0] != 4 || hb.Rows[1] != 6 || hb.IndexRow != 5 || hb.DataStart != 7 {
		t.Errorf("block = %+v", hb)
	}
	wantCols := []int{0, 1, 3, 4, 5} // column 2 is a spacer
	if len(hb.Columns) != len(wantCols) {
		t.Fatalf("columns = %v", hb.Columns)
	}
	for i, c := range wantCols {
		if hb.Columns[i] != c {
			t.Errorf("columns = %v", hb.Columns)
		}
	}
	if hb.Headers[1] != "Stammdaten / Ressort" || hb.Headers[2] != "Stammdaten / Bezeichnung" || hb.Headers[0] != "Spalte A" {
		t.Errorf("headers = %v", hb.Headers)
	}
	if len(hb.ProseAbove) != 2 || hb.ProseAbove[0] != "Gesamtliste landeseigene Gebäude" {
		t.Errorf("prose = %v", hb.ProseAbove)
	}
	if hb.Confidence < 0.45 {
		t.Errorf("confidence = %f", hb.Confidence)
	}
}

func TestHeaderBlockNoHeaderInNumericBlock(t *testing.T) {
	t.Parallel()
	s := grid("#|#|#", "#|#|#", "#|#|#")
	if _, ok := detectHeaderBlock(s, Region{0, 0, 2, 2, true}, 3); ok {
		t.Error("numeric block must not yield a header")
	}
}

func TestJoinHeader(t *testing.T) {
	t.Parallel()
	if got := joinHeader([]string{"Kriterien\nBaurecht", "Kriterien\nBaurecht", "Note"}); got != "Kriterien Baurecht / Note" {
		t.Errorf("got %q", got)
	}
}

// TestHeaderBlockTwoIndexRows: two consecutive index-shaped rows directly
// above the anchor must not both be treated as the index row. Only the one
// adjacent to the header block (row 1) is the index row; row 0 (also
// index-shaped, but not adjacent once row 1 is claimed) stops the upward
// extension and is reported as prose, not swallowed as (or leaking past) the
// index row.
func TestHeaderBlockTwoIndexRows(t *testing.T) {
	t.Parallel()
	s := grid(
		"1|2|3",
		"1|2|3",
		"Name|Wert|Note",
		"a|#|#",
		"b|#|#",
		"c|#|#",
	)
	for r := 0; r < 2; r++ {
		for c := 0; c < 3; c++ {
			s.Rows[r][c].Kind = sheetsource.KindNumber
		}
	}
	boldRow(s, 2)
	reg := Region{Top: 0, Left: 0, Bottom: 5, Right: 2, OpenEnded: true}
	hb, ok := detectHeaderBlock(s, reg, 3)
	if !ok {
		t.Fatal("no header found")
	}
	if hb.IndexRow != 1 {
		t.Errorf("IndexRow = %d, want 1", hb.IndexRow)
	}
	if len(hb.Rows) != 1 || hb.Rows[0] != 2 {
		t.Errorf("Rows = %v, want [2]", hb.Rows)
	}
	if len(hb.ProseAbove) != 1 || hb.ProseAbove[0] != "1 2 3" {
		t.Errorf("ProseAbove = %v, want [\"1 2 3\"]", hb.ProseAbove)
	}
	if hb.DataStart != 3 {
		t.Errorf("DataStart = %d, want 3", hb.DataStart)
	}
}
