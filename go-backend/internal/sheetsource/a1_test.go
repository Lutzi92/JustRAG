package sheetsource

import "testing"

func TestParseCellRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		row, col int
	}{
		{"A1", 0, 0}, {"B14", 13, 1}, {"$C$6", 5, 2}, {"AA10", 9, 26}, {"aw662", 661, 48},
		{"XFD1", 0, 16383}, // max valid column
	}
	for _, c := range cases {
		r, col, err := ParseCellRef(c.in)
		if err != nil || r != c.row || col != c.col {
			t.Errorf("ParseCellRef(%q) = %d,%d,%v want %d,%d", c.in, r, col, err, c.row, c.col)
		}
	}
	// Invalid cases
	for _, invalid := range []string{"1A", "Liste1", "ABCD1", "XFE1"} {
		if _, _, err := ParseCellRef(invalid); err == nil {
			t.Errorf("expected error for %q", invalid)
		}
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
