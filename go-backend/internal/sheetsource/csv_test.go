package sheetsource

import "testing"

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
