package sheetsource

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestODSCoveredCellsAndTypes(t *testing.T) {
	t.Parallel()
	src, err := OpenODS("testdata/covered_cells.ods")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var rows [][]Cell
	ex, err := src.ReadSheet(0, func(i int, cells []Cell) error {
		for len(rows) < i {
			rows = append(rows, nil)
		}
		rows = append(rows, cells)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[0]) != 4 {
		t.Fatalf("header width = %d (trailing repeated empties must be trimmed): %+v", len(rows[0]), rows[0])
	}
	// A2:B2 merged ("Bereich"): B2 is a covered cell. C2/D2 are genuinely
	// blank in this section-header row (verified against the real fixture
	// bytes), but must still be present at their correct column indices —
	// not silently dropped by trailing-empty trimming, and not shifted left
	// by the covered cell. len(row) == 4 plus index-2/3 access succeeding
	// (rather than a panic from an over-eager trim) is the alignment proof.
	row2 := rows[1]
	if row2[0].Raw != "Bereich" || len(row2) != 4 || !row2[1].IsEmpty() || !row2[2].IsEmpty() || !row2[3].IsEmpty() {
		t.Errorf("row 2 alignment broken: %+v", row2)
	}
	// The next data row proves column continuation past the merge: Anteil
	// (percent) and Stand (date) land at the same columns C/D.
	row3 := rows[2]
	if row3[0].Raw != "Verwaltung" || row3[2].IsEmpty() || row3[3].IsEmpty() {
		t.Errorf("row 3 alignment broken: %+v", row3)
	}
	if ex.Merged == nil || len(ex.Merged) == 0 || ex.Merged[0] != (Range{1, 0, 1, 1}) {
		t.Errorf("merged = %+v", ex.Merged)
	}
	pctSeen, dateSeen := false, false
	for _, r := range rows[1:] {
		for _, c := range r {
			if c.Style.Percent && c.Kind == KindNumber {
				pctSeen = true
			}
			if c.Kind == KindDate && len(c.Raw) == 10 {
				dateSeen = true
			}
		}
	}
	if !pctSeen || !dateSeen {
		t.Errorf("percent=%v date=%v", pctSeen, dateSeen)
	}
	if ex.RowCount > 20 {
		t.Errorf("trailing repeated empty rows not trimmed: %d", ex.RowCount)
	}
}

// buildODS writes a minimal .ods zip containing the given content.xml body
// (already wrapped by the caller in the full office:document-content
// envelope) to a temp file and returns its path.
func buildODS(t *testing.T, contentXML string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	w, err := zw.Create("mimetype")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("application/vnd.oasis.opendocument.spreadsheet")); err != nil {
		t.Fatal(err)
	}
	w, err = zw.Create("content.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(contentXML)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "test.ods")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const odsTestDoc = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
  xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
  xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0"
  xmlns:of="urn:oasis:names:tc:opendocument:xmlns:of:1.2"
  office:version="1.4">
  <office:automatic-styles>
    <style:style style:name="taVisible" style:family="table">
      <style:table-properties table:display="true"/>
    </style:style>
    <style:style style:name="taHidden" style:family="table">
      <style:table-properties table:display="false"/>
    </style:style>
  </office:automatic-styles>
  <office:body>
    <office:spreadsheet>
      <table:content-validations>
        <table:content-validation table:name="val1" table:condition="of:cell-content-is-in-list(&quot;a&quot;;&quot;b&quot;)"/>
        <table:content-validation table:name="val2" table:condition="of:cell-content-is-in-list([Sheet.$C$6:.$C$8])"/>
      </table:content-validations>
      <table:table table:name="Main" table:style-name="taVisible">
        <table:table-row>
          <table:table-cell office:value-type="string" table:content-validation-name="val1"><text:p>Header</text:p></table:table-cell>
          <table:table-cell office:value-type="string" table:number-columns-repeated="3"><text:p>x</text:p></table:table-cell>
          <table:table-cell table:number-columns-repeated="16380"/>
        </table:table-row>
        <table:table-row>
          <table:table-cell office:value-type="string" table:content-validation-name="val1"><text:p>Second</text:p></table:table-cell>
          <table:table-cell table:formula="of:=SUM([.A1:.A3])" office:value-type="float" office:value="42"/>
          <table:table-cell table:number-columns-repeated="16380"/>
        </table:table-row>
        <table:table-row table:number-rows-repeated="2">
          <table:table-cell table:number-columns-repeated="4"/>
          <table:table-cell table:number-columns-repeated="16380"/>
        </table:table-row>
        <table:table-row>
          <table:table-cell office:value-type="string"><text:p>Tail</text:p></table:table-cell>
          <table:table-cell table:number-columns-repeated="16383"/>
        </table:table-row>
      </table:table>
      <table:table table:name="Hidden" table:style-name="taHidden">
        <table:table-row>
          <table:table-cell table:number-columns-repeated="16384"/>
        </table:table-row>
      </table:table>
    </office:spreadsheet>
  </office:body>
</office:document-content>`

func TestODSInMemoryDocument(t *testing.T) {
	t.Parallel()
	path := buildODS(t, odsTestDoc)
	src, err := OpenODS(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	infos := src.Sheets()
	if len(infos) != 2 {
		t.Fatalf("sheets = %+v", infos)
	}
	if infos[0].Name != "Main" || infos[0].Hidden {
		t.Errorf("Main sheet info = %+v", infos[0])
	}
	if infos[1].Name != "Hidden" || !infos[1].Hidden {
		t.Errorf("Hidden sheet info = %+v", infos[1])
	}

	var rows [][]Cell
	ex, err := src.ReadSheet(0, func(i int, cells []Cell) error {
		for len(rows) < i {
			rows = append(rows, nil)
		}
		rows = append(rows, cells)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// number-columns-repeated expansion + trailing trim: header row is
	// [Header, x, x, x] (width 4), the final repeated-16380 empty block
	// dropped.
	if len(rows[0]) != 4 {
		t.Fatalf("header width = %d: %+v", len(rows[0]), rows[0])
	}
	if rows[0][0].Raw != "Header" || rows[0][1].Raw != "x" || rows[0][2].Raw != "x" || rows[0][3].Raw != "x" {
		t.Errorf("header cells = %+v", rows[0])
	}

	// Formula cell, on the row right after the header.
	second := rows[1]
	if second[0].Raw != "Second" {
		t.Fatalf("row 1 = %+v", second)
	}
	f := second[1]
	if !f.IsFormula || f.Formula != "SUM([.A1:.A3])" {
		t.Errorf("formula cell = %+v", f)
	}
	if f.Kind != KindNumber || f.Raw != "42" {
		t.Errorf("formula cell value = %+v", f)
	}

	// Interior repeated empty row (number-rows-repeated=2) delivered as two
	// gap rows (cells == nil), not as rows of empty cells — "interior"
	// because a real row (Tail) follows.
	if rows[2] != nil || rows[3] != nil {
		t.Errorf("gap rows not nil: %+v / %+v", rows[2], rows[3])
	}
	if rows[4] == nil || rows[4][0].Raw != "Tail" {
		t.Errorf("row 4 = %+v", rows[4])
	}

	// RowCount reflects every delivered row (gaps included) through the
	// last real row; no trailing gap rows follow Tail, so nothing is
	// dropped here.
	if ex.RowCount != 5 {
		t.Errorf("RowCount = %d, want 5", ex.RowCount)
	}

	// Inline list validation attached to two cells (Header/A1 and Second/A2,
	// vertically adjacent, same column), merged into a single range by the
	// final pass. val2 (range-based, unused by any cell) is excluded.
	if len(ex.Validations) != 1 {
		t.Fatalf("validations = %+v", ex.Validations)
	}
	v := ex.Validations[0]
	if v.Ref != `of:cell-content-is-in-list("a";"b")` {
		t.Errorf("validation ref = %q", v.Ref)
	}
	if len(v.Values) != 2 || v.Values[0] != "a" || v.Values[1] != "b" {
		t.Errorf("validation values = %+v", v.Values)
	}
	if len(v.Sqref) != 1 || v.Sqref[0] != (Range{0, 0, 1, 0}) {
		t.Errorf("validation sqref = %+v", v.Sqref)
	}
}
