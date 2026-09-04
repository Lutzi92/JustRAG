package sheetsource

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
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

func TestXLSXValidationDefinedNameResolved(t *testing.T) {
	t.Parallel()
	f := excelize.NewFile()
	defer f.Close()

	// Create Dropdown sheet with values
	if _, err := f.NewSheet("Dropdown"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Dropdown", "C6", "Nein"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Dropdown", "C7", "Einzelkulturdenkmal"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Dropdown", "C8", "Ensembleschutz"); err != nil {
		t.Fatal(err)
	}

	// Create defined name pointing to the range
	if err := f.SetDefinedName(&excelize.DefinedName{
		Name:     "Liste1",
		RefersTo: "Dropdown!$C$6:$C$8",
		Scope:    "Workbook",
	}); err != nil {
		t.Fatal(err)
	}

	// Add data validation on Sheet1 using the defined name
	dv := excelize.NewDataValidation(true)
	dv.Sqref = "A2:A10"
	dv.Type = "list"
	dv.Formula1 = "Liste1"
	if err := f.AddDataValidation("Sheet1", dv); err != nil {
		t.Fatal(err)
	}

	// Save to temp file
	path := filepath.Join(t.TempDir(), "defined_name.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}

	// Open and read
	_, ex := readAll(t, path, 0)
	got := map[string][]string{}
	for _, v := range ex.Validations {
		got[v.Ref] = v.Values
	}

	// Verify the defined name was resolved
	if len(ex.Validations) != 1 {
		t.Fatalf("expected 1 validation, got %d", len(ex.Validations))
	}
	if ex.Validations[0].Ref != "Liste1" {
		t.Errorf("validation ref = %q want Liste1", ex.Validations[0].Ref)
	}
	want := []string{"Nein", "Einzelkulturdenkmal", "Ensembleschutz"}
	if !reflect.DeepEqual(got["Liste1"], want) {
		t.Errorf("resolved values = %v want %v", got["Liste1"], want)
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

// TestResolveValidationsCapsReReads covers the Phase-1 final-review parked
// finding: each range validation re-streams the referenced sheet, so an
// unbounded number of them is O(N x sheet) re-reads. resolveValidations must
// resolve at most maxListValidations range-ref (re-streaming) validations
// per sheet, leaving the rest Values == nil rather than re-streaming without
// bound. Inline lists carry no I/O cost and must never be capped: a fix-round
// finding caught resolveValidations counting them against the cap too, which
// let cheap inline dropdowns starve the expensive range-ref resolutions the
// cap exists to bound.
func TestResolveValidationsCapsReReads(t *testing.T) {
	t.Parallel()
	src, err := OpenXLSX("testdata/header_row14_metadata.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	ex := SheetExtras{}
	const numInline = 60
	const numRange = maxListValidations + 5
	for i := 0; i < numInline; i++ {
		ex.Validations = append(ex.Validations, Validation{Ref: `"x"`})
	}
	for i := 0; i < numRange; i++ {
		ex.Validations = append(ex.Validations, Validation{Ref: "Dropdown!$C$6:$C$8"})
	}
	src.resolveValidations(&ex, 0)
	inlineResolved, rangeResolved, rangeUnresolved := 0, 0, 0
	for i, v := range ex.Validations {
		if i < numInline {
			if v.Values != nil {
				inlineResolved++
			}
			continue
		}
		if v.Values != nil {
			rangeResolved++
		} else {
			rangeUnresolved++
		}
	}
	if inlineResolved != numInline {
		t.Errorf("inline resolved = %d, want all %d (inline lists must never be capped)", inlineResolved, numInline)
	}
	if rangeResolved != maxListValidations {
		t.Errorf("range resolved = %d, want cap %d", rangeResolved, maxListValidations)
	}
	if want := numRange - maxListValidations; rangeUnresolved != want {
		t.Errorf("range unresolved = %d, want %d", rangeUnresolved, want)
	}
}
