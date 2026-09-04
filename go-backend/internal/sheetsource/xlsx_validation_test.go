package sheetsource

import (
	"reflect"
	"testing"
)

func TestXLSXValidationValuesResolved(t *testing.T) {
	t.Parallel()
	_, ex := readAll(t, "testdata/header_row14_metadata.xlsx", 0)
	got := map[string][]string{}
	for _, v := range ex.Validations {
		got[v.Ref] = v.Values
	}
	if want := []string{"Nein", "Einzelkulturdenkmal", "Ensembleschutz"}; !reflect.DeepEqual(got["Dropdown!$C$6:$C$8"], want) {
		t.Errorf("range list = %v want %v", got["Dropdown!$C$6:$C$8"], want)
	}
	if want := []string{"vor 1977", "von 1977 bis 2010", "ab 2010"}; !reflect.DeepEqual(got[`"vor 1977,von 1977 bis 2010,ab 2010"`], want) {
		t.Errorf("inline list = %v", got)
	}
}

func TestParseListRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in         string
		sheet, rng string
		inline     bool
	}{
		{`"a,b"`, "", "", true},
		{"Dropdown!$C$6:$C$8", "Dropdown", "$C$6:$C$8", false},
		{"'Meine Liste'!A1:A3", "Meine Liste", "A1:A3", false},
		{"$C$6:$C$8", "", "$C$6:$C$8", false},
	}
	for _, c := range cases {
		sheet, rng, inline := parseListRef(c.in)
		if sheet != c.sheet || rng != c.rng || inline != c.inline {
			t.Errorf("parseListRef(%q) = %q,%q,%v", c.in, sheet, rng, inline)
		}
	}
}
