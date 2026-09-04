package sheetsource

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

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

func TestOpenXLSXCorruptPart(t *testing.T) {
	t.Parallel()
	// Create a minimal valid XLSX with corrupt sharedStrings data
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	// Write minimal workbook.xml
	workbookXML := `<?xml version="1.0"?><workbook><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`
	w, _ := zw.Create("xl/workbook.xml")
	w.Write([]byte(workbookXML))

	// Write minimal workbook.xml.rels
	relsXML := `<?xml version="1.0"?><Relationships><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`
	w, _ = zw.Create("xl/_rels/workbook.xml.rels")
	w.Write([]byte(relsXML))

	// Create sharedStrings.xml entry, but truncate it mid-stream to corrupt it
	w, _ = zw.Create("xl/sharedStrings.xml")
	w.Write([]byte(`<?xml version="1.0"?><sst><si><t>test</t></si><si><t>inco`))
	// Entry is incomplete/invalid XML

	zw.Close()

	// Write to temp file
	tmpFile := filepath.Join(t.TempDir(), "corrupt.xlsx")
	if err := os.WriteFile(tmpFile, buf.Bytes(), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// Try to open - should get an error from parsing corrupt sharedStrings
	w_result, err := openXLSX(tmpFile)
	if err == nil {
		w_result.Close()
		t.Fatal("expected error opening corrupt sharedStrings.xml, got nil")
	}
	if w_result != nil {
		w_result.Close()
	}
}
