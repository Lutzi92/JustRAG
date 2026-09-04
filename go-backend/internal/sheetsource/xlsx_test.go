package sheetsource

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestXLSXSourceSheetsRowCount verifies RowCount is parsed from the sheet's
// <dimension ref="..."/> element (which precedes <sheetData> per the OOXML
// schema) rather than requiring a full read. multirow_header_formulas.xlsx
// went through a LibreOffice round-trip (see testdata/regen.sh), which
// recomputes a real dimension: sheet1 (Erhebung) is "A1:G15" -> 15 rows,
// sheet2 (Noten, hidden) is "A1:B5" -> 5 rows.
func TestXLSXSourceSheetsRowCount(t *testing.T) {
	t.Parallel()
	src, err := OpenXLSX("testdata/multirow_header_formulas.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	sheets := src.Sheets()
	if len(sheets) != 2 {
		t.Fatalf("Sheets() = %+v, want 2 sheets", sheets)
	}
	if sheets[0].Name != "Erhebung" || sheets[0].RowCount != 15 {
		t.Errorf("sheet 0 = %+v, want Name=Erhebung RowCount=15", sheets[0])
	}
	if sheets[1].Name != "Noten" || sheets[1].RowCount != 5 || !sheets[1].Hidden {
		t.Errorf("sheet 1 = %+v, want Name=Noten RowCount=5 Hidden=true", sheets[1])
	}
}

// TestXLSXSourceSheetsRowCountStubDimension covers a real fixture whose
// dimension was never recomputed by its writer (a raw Go xlsx writer, per
// testdata/regen.sh, unlike the LibreOffice-round-tripped fixture above):
// header_row14_metadata.xlsx's worksheet parts all carry a stale stub
// `<dimension ref="A1"/>` regardless of how much data the sheet actually
// holds. RowCount reports exactly what the ref declares (1) -- it is not a
// substitute for actually reading the sheet, and callers that need the true
// extent still fall back to FallbackRegionRows when this is unreliable.
func TestXLSXSourceSheetsRowCountStubDimension(t *testing.T) {
	t.Parallel()
	src, err := OpenXLSX("testdata/header_row14_metadata.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	sheets := src.Sheets()
	if len(sheets) == 0 {
		t.Fatal("Sheets() returned none")
	}
	if sheets[0].RowCount != 1 {
		t.Errorf("sheet 0 RowCount = %d, want 1 (stub <dimension ref=%q>)", sheets[0].RowCount, "A1")
	}
}

// TestXLSXSourceSheetsRowCountMissingDimension synthesizes a minimal xlsx
// whose worksheet part has no <dimension> element at all, so sheetRowCount
// must return 0 (not panic, not misread <sheetData> as a dimension).
func TestXLSXSourceSheetsRowCountMissingDimension(t *testing.T) {
	t.Parallel()
	path := writeMinimalXLSXWithoutDimension(t)
	src, err := OpenXLSX(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	sheets := src.Sheets()
	if len(sheets) != 1 {
		t.Fatalf("Sheets() = %+v, want 1 sheet", sheets)
	}
	if sheets[0].RowCount != 0 {
		t.Errorf("RowCount = %d, want 0 (no <dimension> element)", sheets[0].RowCount)
	}
}

// writeMinimalXLSXWithoutDimension builds the smallest zip archive that
// OpenXLSX will accept: workbook.xml + its rels + one worksheet part whose
// XML has no <dimension> element, only <sheetData>.
func writeMinimalXLSXWithoutDimension(t *testing.T) string {
	t.Helper()

	const workbookXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets>
</workbook>`

	const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>`

	const sheetXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<sheetData><row r="1"><c r="A1" t="str"><v>x</v></c></row></sheetData>
</worksheet>`

	dir := t.TempDir()
	path := filepath.Join(dir, "no_dimension.xlsx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"xl/workbook.xml":            workbookXML,
		"xl/_rels/workbook.xml.rels": relsXML,
		"xl/worksheets/sheet1.xml":   sheetXML,
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	return path
}
