package sheetsource

import "testing"

type fakeSource struct {
	rows [][]Cell
	gaps map[int]bool
}

func (f *fakeSource) Sheets() []SheetInfo { return []SheetInfo{{Index: 0, Name: "S"}} }
func (f *fakeSource) Close() error        { return nil }
func (f *fakeSource) ReadSheet(_ int, fn RowFunc) (SheetExtras, error) {
	ex := SheetExtras{Merged: []Range{{0, 0, 0, 2}}}
	for i, r := range f.rows {
		if f.gaps[i] {
			if err := fn(i, nil); err != nil {
				return ex, err
			}
			continue
		}
		if len(r) > ex.MaxCol {
			ex.MaxCol = len(r)
		}
		if err := fn(i, r); err != nil {
			return ex, err
		}
	}
	ex.RowCount = len(f.rows)
	return ex, nil
}

func text(s string) Cell { return Cell{Raw: s, Formatted: s, Kind: KindText} }

func TestCollectSamplePadsAndKeepsExtras(t *testing.T) {
	t.Parallel()
	src := &fakeSource{
		rows: [][]Cell{{text("a")}, nil, {text("b"), text("c"), text("d")}, {text("e")}},
		gaps: map[int]bool{1: true},
	}
	s, err := CollectSample(src, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if s.Width != 3 || len(s.Rows) != 3 || s.TotalRows != 4 {
		t.Fatalf("width=%d rows=%d total=%d", s.Width, len(s.Rows), s.TotalRows)
	}
	for i, r := range s.Rows {
		if len(r) != 3 {
			t.Errorf("row %d not padded: %d", i, len(r))
		}
	}
	if s.Rows[1][0].Kind != KindEmpty || s.Rows[2][1].Raw != "c" {
		t.Error("content")
	}
	if len(s.Extras.Merged) != 1 {
		t.Error("extras lost")
	}
}
