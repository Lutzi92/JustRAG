package sheetsource

import "testing"

func TestParseCellRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		row, col int
	}{
		{"A1", 0, 0}, {"B14", 13, 1}, {"$C$6", 5, 2}, {"AA10", 9, 26}, {"aw662", 661, 48},
	}
	for _, c := range cases {
		r, col, err := ParseCellRef(c.in)
		if err != nil || r != c.row || col != c.col {
			t.Errorf("ParseCellRef(%q) = %d,%d,%v want %d,%d", c.in, r, col, err, c.row, c.col)
		}
	}
	if _, _, err := ParseCellRef("1A"); err == nil {
		t.Error("expected error for 1A")
	}
}

func TestParseSqref(t *testing.T) {
	t.Parallel()
	got, err := ParseSqref("AJ137:AJ148 AH15:AH120 D15")
	if err != nil {
		t.Fatal(err)
	}
	want := []Range{{136, 35, 147, 35}, {14, 33, 119, 33}, {14, 3, 14, 3}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if ColumnName(0) != "A" || ColumnName(26) != "AA" || ColumnName(48) != "AW" {
		t.Error("ColumnName")
	}
}
