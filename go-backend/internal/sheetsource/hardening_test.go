package sheetsource

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildXLSX writes a zip with the given part name -> content and returns its
// path. Callers supply whatever subset of the OOXML parts the case needs.
func buildXLSX(t *testing.T, parts map[string]string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "test.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimalWorkbookXML = `<?xml version="1.0"?><workbook><sheets><sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`
const minimalRelsXML = `<?xml version="1.0"?><Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`
const minimalSheetXML = `<?xml version="1.0"?><worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c></row></sheetData></worksheet>`

// sharedStrings of n entries; each <si> is well-formed so the only thing that
// can end the read is the size cap.
func sharedStringsOf(n int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><sst>`)
	for range n {
		b.WriteString(`<si><t>0123456789012345678901234567890123456789</t></si>`)
	}
	b.WriteString(`</sst>`)
	return b.String()
}

// TestPartSizeCap: a decompressed archive part larger than maxPartBytes must
// fail with a clear error rather than being read into memory. The declared
// uncompressed size in a zip header is attacker-controlled, so the bound has
// to be on the bytes actually read.
func TestPartSizeCap(t *testing.T) {
	// Not parallel: it overrides the package-level cap.
	orig := maxPartBytes
	t.Cleanup(func() { maxPartBytes = orig })

	big := sharedStringsOf(200)
	path := buildXLSX(t, map[string]string{
		"xl/workbook.xml":            minimalWorkbookXML,
		"xl/_rels/workbook.xml.rels": minimalRelsXML,
		"xl/worksheets/sheet1.xml":   minimalSheetXML,
		"xl/sharedStrings.xml":       big,
	})

	maxPartBytes = int64(len(big)) - 1
	src, err := OpenXLSX(path)
	if err == nil {
		src.Close()
		t.Fatal("OpenXLSX succeeded on a sharedStrings.xml past the cap")
	}
	if !strings.Contains(err.Error(), "exceeds the") || !strings.Contains(err.Error(), "sharedStrings") {
		t.Errorf("error = %v, want a clear over-the-limit message naming the part", err)
	}

	// Exactly at the cap is fine — the reader takes one byte past it to tell
	// "at the limit" from "over it".
	maxPartBytes = int64(len(big))
	src, err = OpenXLSX(path)
	if err != nil {
		t.Fatalf("OpenXLSX at exactly the cap = %v, want success", err)
	}
	src.Close()
}

// TestRecoverToErrBoundary exercises the panic boundary directly. A genuine
// parser panic is hard to provoke on demand (the surviving ones are found by
// FuzzOpenXLSX), so this pins the mechanism the entry points install: a panic
// below the boundary becomes an error, and the error names the operation.
func TestRecoverToErrBoundary(t *testing.T) {
	t.Parallel()
	boom := func() (err error) {
		defer recoverToErr(&err, "TestOp")
		cells := make([]Cell, 0)
		_ = cells[len(cells)+2] // index out of range, the shape a truncated record produces
		return nil
	}
	err := boom()
	if err == nil {
		t.Fatal("panic was not converted into an error")
	}
	if !strings.Contains(err.Error(), "panic in TestOp") {
		t.Errorf("error = %v, want it to name the operation", err)
	}
}

// TestReadSheetPanicBecomesError proves the boundary is installed on a real
// entry point: a RowFunc that panics comes back as an error rather than
// unwinding into the worker.
func TestReadSheetPanicBecomesError(t *testing.T) {
	t.Parallel()
	path := buildXLSX(t, map[string]string{
		"xl/workbook.xml":            minimalWorkbookXML,
		"xl/_rels/workbook.xml.rels": minimalRelsXML,
		"xl/worksheets/sheet1.xml":   minimalSheetXML,
	})
	src, err := OpenXLSX(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	_, err = src.ReadSheet(0, func(int, []Cell) error { panic("kaboom") })
	if err == nil {
		t.Fatal("ReadSheet returned nil after a panic")
	}
	if !strings.Contains(err.Error(), "kaboom") || !strings.Contains(err.Error(), "XLSXSource.ReadSheet") {
		t.Errorf("error = %v, want the panic value and the entry point", err)
	}
}

// FuzzOpenXLSX runs the whole xlsx path — archive, workbook, styles, shared
// strings, every sheet — over mutated real fixtures. Nothing may panic; any
// error is an acceptable outcome.
func FuzzOpenXLSX(f *testing.F) {
	seeds, err := filepath.Glob(filepath.Join("testdata", "*.xlsx"))
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range seeds {
		b, err := os.ReadFile(s)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "f.xlsx")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Skip()
		}
		src, err := OpenXLSX(path)
		if err != nil {
			return
		}
		defer src.Close()
		for _, sh := range src.Sheets() {
			n := 0
			_, err := src.ReadSheet(sh.Index, func(int, []Cell) error {
				n++
				if n > 5000 {
					return ErrStop
				}
				return nil
			})
			if err != nil && errors.Is(err, ErrStop) {
				t.Fatalf("ErrStop leaked out of ReadSheet: %v", err)
			}
		}
	})
}
