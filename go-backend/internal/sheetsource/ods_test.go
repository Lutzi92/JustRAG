package sheetsource

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
	if len(ex.Merged) == 0 || ex.Merged[0] != (Range{1, 0, 1, 1}) {
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

const odsThreeRowsDoc = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
  xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
  office:version="1.4">
  <office:body>
    <office:spreadsheet>
      <table:table table:name="Main">
        <table:table-row>
          <table:table-cell office:value-type="string"><text:p>R0</text:p></table:table-cell>
          <table:table-cell table:number-columns-repeated="16383"/>
        </table:table-row>
        <table:table-row>
          <table:table-cell office:value-type="string"><text:p>R1</text:p></table:table-cell>
          <table:table-cell table:number-columns-repeated="16383"/>
        </table:table-row>
        <table:table-row>
          <table:table-cell office:value-type="string"><text:p>R2</text:p></table:table-cell>
          <table:table-cell table:number-columns-repeated="16383"/>
        </table:table-row>
      </table:table>
    </office:spreadsheet>
  </office:body>
</office:document-content>`

// TestODSErrStopRowCount verifies that SheetExtras.RowCount counts the row
// on which the callback returned ErrStop: that row was delivered (fn ran on
// it) before it asked to stop, so it must be included, per the RowCount doc
// ("rows delivered, gaps included") and matching XLSXSource's behavior.
func TestODSErrStopRowCount(t *testing.T) {
	t.Parallel()
	path := buildODS(t, odsThreeRowsDoc)
	src, err := OpenODS(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	delivered := 0
	ex, err := src.ReadSheet(0, func(i int, cells []Cell) error {
		delivered++
		if i == 1 {
			return ErrStop
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadSheet returned err = %v, want nil (ErrStop is swallowed)", err)
	}
	if delivered != 2 {
		t.Fatalf("delivered = %d, want 2", delivered)
	}
	if ex.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2 (the stopping row, index 1, must be counted)", ex.RowCount)
	}
}

const odsCoveredRepeatDoc = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
  xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
  office:version="1.4">
  <office:body>
    <office:spreadsheet>
      <table:table table:name="Main">
        <table:table-row>
          <table:table-cell office:value-type="string"><text:p>A</text:p></table:table-cell>
          <table:covered-table-cell table:number-columns-repeated="3"/>
          <table:table-cell office:value-type="string"><text:p>E</text:p></table:table-cell>
          <table:table-cell table:number-columns-repeated="16380"/>
        </table:table-row>
      </table:table>
    </office:spreadsheet>
  </office:body>
</office:document-content>`

// TestODSCoveredCellRepeatAlignment covers a <table:covered-table-cell>
// that itself carries number-columns-repeated (distinct from the plain,
// single covered cell already exercised by the real-fixture test): it must
// expand to that many blank Cell{} entries without shifting the column
// index of what follows.
func TestODSCoveredCellRepeatAlignment(t *testing.T) {
	t.Parallel()
	path := buildODS(t, odsCoveredRepeatDoc)
	src, err := OpenODS(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	var got []Cell
	_, err = src.ReadSheet(0, func(i int, cells []Cell) error {
		if i == 0 {
			got = cells
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("row width = %d, want 5: %+v", len(got), got)
	}
	if got[0].Raw != "A" {
		t.Errorf("A = %+v", got[0])
	}
	for i := 1; i <= 3; i++ {
		if !got[i].IsEmpty() {
			t.Errorf("covered cell %d not empty: %+v", i, got[i])
		}
	}
	if got[4].Raw != "E" {
		t.Errorf("E must stay at column 4, got %+v", got[4])
	}
}

// odsGapDoc builds a one-table document with a content row, an empty row
// repeated `repeat` times, and a second content row — so the gap is interior
// and therefore actually flushed.
func odsGapDoc(repeat string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
  xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
  office:version="1.4">
  <office:body>
    <office:spreadsheet>
      <table:table table:name="Main">
        <table:table-row>
          <table:table-cell office:value-type="string"><text:p>A</text:p></table:table-cell>
        </table:table-row>
        <table:table-row table:number-rows-repeated="` + repeat + `">
          <table:table-cell/>
        </table:table-row>
        <table:table-row>
          <table:table-cell office:value-type="string"><text:p>B</text:p></table:table-cell>
        </table:table-row>
      </table:table>
    </office:spreadsheet>
  </office:body>
</office:document-content>`
}

// TestODSInteriorGapCap pins parity with the xlsx reader's maxGapRows: an
// interior run of empty rows is delivered in full up to 100k (it used to be
// truncated at 1000, which silently shifted every row below it), and a longer
// one is an error rather than a silent truncation.
func TestODSInteriorGapCap(t *testing.T) {
	t.Parallel()

	t.Run("gap past the old 1000 cap is delivered in full", func(t *testing.T) {
		t.Parallel()
		src, err := OpenODS(buildODS(t, odsGapDoc("5000")))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		var lastIdx int
		gaps := 0
		ex, err := src.ReadSheet(0, func(i int, cells []Cell) error {
			lastIdx = i
			if cells == nil {
				gaps++
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if gaps != 5000 {
			t.Errorf("gap rows delivered = %d, want 5000", gaps)
		}
		// A + 5000 gaps + B: the second content row must land at index 5001.
		if lastIdx != 5001 || ex.RowCount != 5002 {
			t.Errorf("last row index = %d, RowCount = %d, want 5001 / 5002", lastIdx, ex.RowCount)
		}
	})

	t.Run("gap past 100k is an error", func(t *testing.T) {
		t.Parallel()
		src, err := OpenODS(buildODS(t, odsGapDoc("200000")))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		if _, err := src.ReadSheet(0, func(int, []Cell) error { return nil }); err == nil {
			t.Fatal("ReadSheet accepted a 200000-row interior gap")
		} else if !strings.Contains(err.Error(), "gap of") {
			t.Errorf("error = %v, want a gap-too-large message", err)
		}
	})

	t.Run("trailing padding row is never counted", func(t *testing.T) {
		t.Parallel()
		// The final row every LibreOffice file writes: empty, repeated to the
		// bottom of the sheet. It is never flushed, so it must not trip the
		// interior-gap cap.
		doc := `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
  xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
  office:version="1.4">
  <office:body>
    <office:spreadsheet>
      <table:table table:name="Main">
        <table:table-row>
          <table:table-cell office:value-type="string"><text:p>A</text:p></table:table-cell>
        </table:table-row>
        <table:table-row table:number-rows-repeated="1048575">
          <table:table-cell table:number-columns-repeated="16384"/>
        </table:table-row>
      </table:table>
    </office:spreadsheet>
  </office:body>
</office:document-content>`
		src, err := OpenODS(buildODS(t, doc))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		ex, err := src.ReadSheet(0, func(int, []Cell) error { return nil })
		if err != nil {
			t.Fatalf("ReadSheet = %v, want nil (trailing padding is not an interior gap)", err)
		}
		if ex.RowCount != 1 {
			t.Errorf("RowCount = %d, want 1", ex.RowCount)
		}
	})
}

const odsPercentDoc = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
  xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
  office:version="1.4">
  <office:body>
    <office:spreadsheet>
      <table:table table:name="Main">
        <table:table-row>
          <table:table-cell office:value-type="percentage" office:value="0.07"/>
          <table:table-cell office:value-type="percentage" office:value="0.29"/>
          <table:table-cell office:value-type="percentage" office:value="0.365"/>
        </table:table-row>
      </table:table>
    </office:spreadsheet>
  </office:body>
</office:document-content>`

// TestODSPercentHasNoFloatNoise guards the shared percentRaw canonicalisation
// at the reader level: 0.07*100 is 7.000000000000001 and 0.29*100 is
// 28.999999999999996 in float64, and this reader used to write exactly those
// digits into Raw while the xlsx reader rounded.
func TestODSPercentHasNoFloatNoise(t *testing.T) {
	t.Parallel()
	path := buildODS(t, odsPercentDoc)
	src, err := OpenODS(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	var got []Cell
	if _, err := src.ReadSheet(0, func(i int, cells []Cell) error {
		if i == 0 {
			got = cells
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"7", "29", "36.5"}
	if len(got) < len(want) {
		t.Fatalf("row = %+v", got)
	}
	for i, w := range want {
		if got[i].Raw != w || got[i].Formatted != w+"%" || !got[i].Style.Percent || got[i].Kind != KindNumber {
			t.Errorf("cell %d = %+v, want Raw %q / Formatted %q", i, got[i], w, w+"%")
		}
	}
}
