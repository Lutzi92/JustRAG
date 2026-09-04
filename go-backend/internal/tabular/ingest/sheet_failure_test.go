package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeXLSXWithOneBrokenSheet builds a minimal, hand-rolled .xlsx (round-1
// fix, ruling R13) with two sheets: "Good" (a real, readable worksheet
// part) and "Broken" (workbook.xml/rels list it, but its worksheet part is
// never written into the zip archive at all). internal/sheetsource opens
// worksheet parts lazily per ReadSheet call (see xlsx_workbook.go's
// openXLSX, which only eagerly reads workbook.xml/rels/sharedStrings/
// styles), so sheetsource.Open succeeds and Sheets() lists both sheets;
// only ReadSheet(1, ...) — and therefore CollectSample for that sheet —
// fails, with sheetsource's own errPartMissing ("sheetsource: part not in
// archive"). This is the same "part not in archive" failure mode
// internal/sheetsource/xlsx_workbook_test.go's TestOpenXLSXCorruptPart
// exercises, just triggered on one sheet's part instead of a workbook-level
// one, so the failure surfaces from ReadSheet/CollectSample rather than
// from sheetsource.Open itself.
func writeXLSXWithOneBrokenSheet(t *testing.T) string {
	t.Helper()

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	write := func(name, content string) {
		t.Helper()
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}

	write("xl/workbook.xml", `<?xml version="1.0"?><workbook><sheets>`+
		`<sheet name="Good" sheetId="1" r:id="rId1"/>`+
		`<sheet name="Broken" sheetId="2" r:id="rId2"/>`+
		`</sheets></workbook>`)
	write("xl/_rels/workbook.xml.rels", `<?xml version="1.0"?><Relationships>`+
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>`+
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>`+
		`</Relationships>`)
	// xl/worksheets/sheet2.xml (Broken's part) is deliberately never
	// written — that is the corruption.
	write("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet><sheetData>`+
		`<row r="1"><c r="A1" t="inlineStr"><is><t>Spalte</t></is></c></row>`+
		`<row r="2"><c r="A2" t="inlineStr"><is><t>ZielwertXYZ</t></is></c></row>`+
		`</sheetData></worksheet>`)

	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	path := filepath.Join(t.TempDir(), "one_broken_sheet.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write temp xlsx: %v", err)
	}
	return path
}

// TestIngestSheetFailureIsSoftAndContinues covers ruling R13: a per-sheet
// read/render failure must not abort the whole file's Ingest. Only sheet
// "Broken" (index 1) fails to read; "Good" (index 0) must still render
// with its real content, and Ingest must return no error.
func TestIngestSheetFailureIsSoftAndContinues(t *testing.T) {
	t.Parallel()
	path := writeXLSXWithOneBrokenSheet(t)

	g := New(nil, nil)
	res, err := g.Ingest(context.Background(), Input{
		FilePath: path, FileName: "one_broken_sheet.xlsx", FileID: "f", KBID: "kb",
		Options: Options{SampleRows: 200, ChunkSize: 512},
	})
	if err != nil {
		t.Fatalf("Ingest must be soft when only one sheet out of several fails: %v", err)
	}

	if len(res.Pages) != 2 {
		t.Fatalf("pages = %d, want 2: %+v", len(res.Pages), res.Pages)
	}
	if !strings.Contains(res.Pages[0].Text, "ZielwertXYZ") {
		t.Errorf("good sheet's text must be intact:\n%s", res.Pages[0].Text)
	}
	if !strings.Contains(res.Pages[1].Text, "Blatt konnte nicht gelesen werden") {
		t.Errorf("broken sheet must render a placeholder page:\n%s", res.Pages[1].Text)
	}

	if len(res.Report.Sheets) != 2 {
		t.Fatalf("report sheets = %d, want 2: %+v", len(res.Report.Sheets), res.Report.Sheets)
	}
	if res.Report.Sheets[0].Name != "Good" || len(res.Report.Sheets[0].Notes) != 0 {
		t.Errorf("good sheet's report must be clean: %+v", res.Report.Sheets[0])
	}
	broken := res.Report.Sheets[1]
	if broken.Name != "Broken" || len(broken.Notes) == 0 || !strings.Contains(broken.Notes[0], "Blatt konnte nicht gelesen werden") {
		t.Errorf("broken sheet's report must carry the note: %+v", broken)
	}
}

// writeXLSXWithAllSheetsBroken is writeXLSXWithOneBrokenSheet's counterpart:
// both sheets are listed in workbook.xml/rels, but NEITHER worksheet part is
// written into the archive, so every ReadSheet call fails.
func writeXLSXWithAllSheetsBroken(t *testing.T) string {
	t.Helper()

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	write := func(name, content string) {
		t.Helper()
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}

	write("xl/workbook.xml", `<?xml version="1.0"?><workbook><sheets>`+
		`<sheet name="Broken1" sheetId="1" r:id="rId1"/>`+
		`<sheet name="Broken2" sheetId="2" r:id="rId2"/>`+
		`</sheets></workbook>`)
	write("xl/_rels/workbook.xml.rels", `<?xml version="1.0"?><Relationships>`+
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>`+
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>`+
		`</Relationships>`)
	// Neither xl/worksheets/sheet1.xml nor sheet2.xml is written.

	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	path := filepath.Join(t.TempDir(), "all_broken_sheets.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write temp xlsx: %v", err)
	}
	return path
}

// TestIngestAllSheetsFailedIsAnError covers the other half of R13: Ingest
// still returns an error when EVERY sheet failed (nothing at all could be
// produced), as opposed to when only some did.
func TestIngestAllSheetsFailedIsAnError(t *testing.T) {
	t.Parallel()
	path := writeXLSXWithAllSheetsBroken(t)
	g := New(nil, nil)
	res, err := g.Ingest(context.Background(), Input{
		FilePath: path, FileName: "all_broken_sheets.xlsx", FileID: "f", KBID: "kb",
		Options: Options{SampleRows: 200, ChunkSize: 512},
	})
	if err == nil {
		t.Fatalf("Ingest must fail when every sheet failed, got res=%+v", res)
	}
}
