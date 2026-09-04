package sheetsource

import (
	"os"
	"testing"
)

func TestCSVSniffAndDecode(t *testing.T) {
	t.Parallel()
	src, err := OpenCSV("testdata/bom_semicolon_cp1252.csv")
	if err != nil {
		t.Fatal(err)
	}
	if src.delim != ';' || src.enc != "windows-1252" {
		t.Errorf("delim=%q enc=%q", src.delim, src.enc)
	}
	var rows [][]Cell
	_, err = src.ReadSheet(0, func(i int, cells []Cell) error { rows = append(rows, cells); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0].Raw != "Gebäude" || rows[0][2].Raw != "Straße" {
		t.Errorf("header = %+v", rows[0])
	}
	if len(rows) != 6 {
		t.Errorf("rows = %d", len(rows))
	}
}

func TestCSVDecimalCommaQuoted(t *testing.T) {
	t.Parallel()
	src, err := OpenCSV("testdata/decimal_comma.csv")
	if err != nil {
		t.Fatal(err)
	}
	if src.delim != ',' {
		t.Errorf("delim=%q", src.delim)
	}
	var rows [][]Cell
	_, _ = src.ReadSheet(0, func(i int, cells []Cell) error { rows = append(rows, cells); return nil })
	if rows[1][1].Raw != "12,5" || rows[2][1].Raw != "1.234,75" {
		t.Errorf("values = %+v / %+v", rows[1], rows[2])
	}
}

func TestSniffDelimiter(t *testing.T) {
	t.Parallel()
	cases := map[string]rune{
		"a;b;c\n1;2;3\n":         ';',
		"a,b,c\n1,2,3\n":         ',',
		"a\tb\tc\n1\t2\t3\n":     '\t',
		"a|b|c\n1|2|3\n":         '|',
		"\"x,y\";b\n1;2\n":       ';',
		"single column\nvalue\n": ',',
	}
	for in, want := range cases {
		if got := sniffDelimiter([]byte(in)); got != want {
			t.Errorf("sniffDelimiter(%q) = %q want %q", in, got, want)
		}
	}
}

func TestCSVTabDelimitedKeepsEmptyFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/test.tsv"
	content := "a\tb\tc\n1\t\t3\n4\t   \t6\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	src, err := Open(path, "test.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	csv := src.(*CSVSource)
	if csv.delim != '\t' {
		t.Errorf("delim=%q, want %q", csv.delim, '\t')
	}
	var rows [][]Cell
	_, err = src.ReadSheet(0, func(i int, cells []Cell) error {
		rows = append(rows, cells)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 3 {
		t.Fatalf("got %d rows, want >= 3", len(rows))
	}
	if len(rows[0]) != 3 {
		t.Errorf("row 0: got %d cells, want 3", len(rows[0]))
	}
	if len(rows[1]) != 3 {
		t.Errorf("row 1: got %d cells, want 3", len(rows[1]))
	}
	if rows[1][1].Kind != KindEmpty {
		t.Errorf("row 1, cell 1: kind=%v, want KindEmpty", rows[1][1].Kind)
	}
	if rows[2][1].Raw != "   " {
		t.Errorf("row 2, cell 1: raw=%q, want \"   \"", rows[2][1].Raw)
	}
	if rows[2][2].Raw != "6" {
		t.Errorf("row 2, cell 2: raw=%q, want \"6\"", rows[2][2].Raw)
	}
}

func TestCSVBlankLinesAreGapRows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/test.csv"
	content := "a,b\n\nc,d\n\n\ne,f\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	src, err := Open(path, "test.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var rows [][]Cell
	extras, err := src.ReadSheet(0, func(i int, cells []Cell) error {
		rows = append(rows, cells)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantIndices := []int{0, 1, 2, 3, 4, 5}
	if len(rows) != len(wantIndices) {
		t.Errorf("got %d rows, want %d (indices %v)", len(rows), len(wantIndices), wantIndices)
	}
	if rows[0] != nil && (len(rows[0]) != 2 || rows[0][0].Raw != "a") {
		t.Errorf("row 0: got %+v, want [a, b]", rows[0])
	}
	if rows[1] != nil {
		t.Errorf("row 1 (gap): got %+v, want nil", rows[1])
	}
	if rows[2] != nil && (len(rows[2]) != 2 || rows[2][0].Raw != "c") {
		t.Errorf("row 2: got %+v, want [c, d]", rows[2])
	}
	if rows[3] != nil {
		t.Errorf("row 3 (gap): got %+v, want nil", rows[3])
	}
	if rows[4] != nil {
		t.Errorf("row 4 (gap): got %+v, want nil", rows[4])
	}
	if rows[5] != nil && (len(rows[5]) != 2 || rows[5][0].Raw != "e") {
		t.Errorf("row 5: got %+v, want [e, f]", rows[5])
	}
	if extras.RowCount != 6 {
		t.Errorf("RowCount=%d, want 6", extras.RowCount)
	}
}
