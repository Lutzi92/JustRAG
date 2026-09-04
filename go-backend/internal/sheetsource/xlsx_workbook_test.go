package sheetsource

import "testing"

func TestOpenXLSXReadsWorkbookParts(t *testing.T) {
	t.Parallel()
	w, err := openXLSX("testdata/header_row14_metadata.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if len(w.sheets) != 3 {
		t.Fatalf("sheets = %d", len(w.sheets))
	}
	byName := map[string]xlsxSheetEntry{}
	for _, s := range w.sheets {
		byName[s.Name] = s
	}
	if !byName["Dropdown"].Hidden || byName["Gebäudeliste"].Hidden {
		t.Errorf("hidden flags: %+v", byName)
	}
	if byName["Gebäudeliste"].Part == "" || w.parts[byName["Gebäudeliste"].Part] == nil {
		t.Errorf("sheet part not resolved: %+v", byName["Gebäudeliste"])
	}
	if len(w.sst) == 0 {
		t.Error("shared strings empty")
	}
	found := false
	for _, s := range w.sst {
		if s == "Gesamtliste landeseigene Gebäude" {
			found = true
		}
	}
	if !found {
		t.Error("title string not in sst")
	}
	if len(w.xfs) < 2 {
		t.Errorf("xfs = %d", len(w.xfs))
	}
	boldSeen := false
	for _, x := range w.xfs {
		if x.Bold && x.Filled {
			boldSeen = true
		}
	}
	if !boldSeen {
		t.Error("no bold+filled xf (header style) parsed")
	}
}

func TestOpenXLSXNumbersFormatsStyles(t *testing.T) {
	t.Parallel()
	w, err := openXLSX("testdata/numbers_formats.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var dates, pcts, euros int
	for _, x := range w.xfs {
		if x.Date {
			dates++
		}
		if x.Percent {
			pcts++
		}
		if x.Unit == "€" {
			euros++
		}
	}
	if dates < 2 || pcts < 1 || euros < 1 {
		t.Errorf("dates=%d pcts=%d euros=%d", dates, pcts, euros)
	}
	if w.date1904 {
		t.Error("date1904 should be false")
	}
}
