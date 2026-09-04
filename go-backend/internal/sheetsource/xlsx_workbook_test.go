package sheetsource

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
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

// nopWriteCloser wraps an io.Writer to provide a Close() method.
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

func TestOpenXLSXCorruptPart(t *testing.T) {
	t.Parallel()
	// Create a zip with an entry using unknown compression method 99.
	// archive/zip reader has no decompressor for 99, so File.Open() fails with ErrAlgorithm.
	// This exercises the f.Open() error path, not just parsing errors.
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	defer zw.Close()

	// Register method 99 so the writer accepts it (but the reader cannot decode it)
	zw.RegisterCompressor(99, func(w io.Writer) (io.WriteCloser, error) {
		return nopWriteCloser{w}, nil
	})

	// Write minimal workbook.xml with standard Deflate
	workbookXML := `<?xml version="1.0"?><workbook><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`
	hdr := &zip.FileHeader{Name: "xl/workbook.xml", Method: zip.Deflate}
	w, _ := zw.CreateHeader(hdr)
	w.Write([]byte(workbookXML))

	// Write minimal workbook.xml.rels with standard Deflate
	relsXML := `<?xml version="1.0"?><Relationships><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`
	hdr = &zip.FileHeader{Name: "xl/_rels/workbook.xml.rels", Method: zip.Deflate}
	w, _ = zw.CreateHeader(hdr)
	w.Write([]byte(relsXML))

	// Create sharedStrings.xml with unknown method 99: File.Open() will fail
	hdr = &zip.FileHeader{Name: "xl/sharedStrings.xml", Method: 99}
	w, _ = zw.CreateHeader(hdr)
	w.Write([]byte(`<sst><si><t>x</t></si></sst>`))

	zw.Close()

	// Write to temp file
	tmpFile := filepath.Join(t.TempDir(), "corrupt.xlsx")
	if err := os.WriteFile(tmpFile, buf.Bytes(), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// Try to open - should get an error from f.Open() failing
	result, err := openXLSX(tmpFile)
	if err == nil {
		result.Close()
		t.Fatal("expected error opening zip with unknown compression method, got nil")
	}
	if result != nil {
		result.Close()
	}
	// Verify the error is from the f.Open() failure (ErrAlgorithm), not errPartMissing
	if errors.Is(err, errPartMissing) {
		t.Errorf("error should not be errPartMissing, got: %v", err)
	}
	if !errors.Is(err, zip.ErrAlgorithm) {
		t.Errorf("error should be or wrap zip.ErrAlgorithm, got: %v", err)
	}
}
