//go:build integration

// Fix-wave integration coverage (R21 / R22). Requires a live main Postgres,
// same gating as materializer_integration_test.go (openMainPool/seedFile are
// reused from there).

package tabular

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
	"github.com/justrag/go-backend/internal/tabular/render"
)

// ---------------------------------------------------------------------------
// synthetic source
// ---------------------------------------------------------------------------

type memSource struct {
	info sheetsource.SheetInfo
	rows [][]sheetsource.Cell
}

func (m *memSource) Sheets() []sheetsource.SheetInfo { return []sheetsource.SheetInfo{m.info} }
func (m *memSource) Close() error                    { return nil }
func (m *memSource) ReadSheet(_ int, fn sheetsource.RowFunc) (sheetsource.SheetExtras, error) {
	width := 0
	for _, r := range m.rows {
		if len(r) > width {
			width = len(r)
		}
	}
	for i, r := range m.rows {
		if err := fn(i, r); err != nil {
			if err == sheetsource.ErrStop {
				break
			}
			return sheetsource.SheetExtras{}, err
		}
	}
	return sheetsource.SheetExtras{MaxCol: width, RowCount: len(m.rows)}, nil
}

func tCell(s string) sheetsource.Cell {
	return sheetsource.Cell{Kind: sheetsource.KindText, Raw: s, Formatted: s}
}
func nCell(s string) sheetsource.Cell {
	return sheetsource.Cell{Kind: sheetsource.KindNumber, Raw: s, Formatted: s}
}
func fCell(formula, value string) sheetsource.Cell {
	return sheetsource.Cell{Kind: sheetsource.KindNumber, Raw: value, Formatted: value, IsFormula: true, Formula: formula}
}

// profileOne profiles the source's only sheet and returns the sheet profile
// plus its single table region's index.
func profileOne(t *testing.T, src sheetsource.Source) (profile.SheetProfile, int) {
	t.Helper()
	sample, err := sheetsource.CollectSample(src, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	sp := profile.ProfileSheet(sample, profile.Options{})
	for i, r := range sp.Regions {
		if r.Kind == profile.KindTable {
			return sp, i
		}
	}
	t.Fatalf("no table region in %+v", sp)
	return sp, 0
}

// ---------------------------------------------------------------------------
// R22 — one row classifier on both sides
// ---------------------------------------------------------------------------

var markerRe = regexp.MustCompile(`^\[tabular\.[a-z0-9_]+ rows (\d+)–(\d+)\]$`)

// recordsByOrdinal walks the rendered page and returns ordinal -> the first
// field's value of the record numbered with that ordinal, derived purely
// from the block markers and the position of the record inside its block.
func recordsByOrdinal(t *testing.T, page string) map[int]string {
	t.Helper()
	out := map[int]string{}
	for _, block := range strings.Split(page, "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) < 2 {
			continue
		}
		mm := markerRe.FindStringSubmatch(lines[1])
		if mm == nil {
			continue
		}
		a, _ := strconv.Atoi(mm[1])
		b, _ := strconv.Atoi(mm[2])
		recs := lines[2:]
		if len(recs) != b-a+1 {
			t.Fatalf("marker %q covers %d ordinals but the block holds %d records:\n%s", lines[1], b-a+1, len(recs), block)
		}
		for i, rec := range recs {
			first, _, _ := strings.Cut(rec, " | ")
			_, val, _ := strings.Cut(first, ": ")
			if prev, dup := out[a+i]; dup {
				t.Fatalf("ordinal %d rendered twice: %q and %q", a+i, prev, val)
			}
			out[a+i] = val
		}
	}
	return out
}

// TestMarkerOrdinalsMatchRowIDs pins R22 end to end: the renderer's block
// markers and the materialised table's _rowid must be the same function of
// the same rows. The sheet deliberately contains BOTH ways the two sides
// used to disagree — an aggregate-formula subtotal row EARLY in the region
// (the materialiser used a running row count for IsDerivedRow's "spans half
// the region" rule, so early rows never tripped it while the renderer, which
// knew the whole region, did) and a row whose kept cells are all null tokens
// (the renderer's bare Cell.IsEmpty check numbered it; the materialiser's
// Canonical-based check skipped it). Either one shifts every later record's
// ordinal away from its _rowid, so "[tabular.T rows 12–18]" in a retrieved
// chunk would point table_query at the wrong rows.
func TestMarkerOrdinalsMatchRowIDs(t *testing.T) {
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool)

	const dataRows = 40
	rows := [][]sheetsource.Cell{{tCell("Gebäude"), tCell("BGF"), tCell("Ort")}}
	for i := 1; i <= dataRows; i++ {
		switch i {
		case 3:
			// (a) a subtotal row early in the region: an aggregate formula
			// spanning the whole data range.
			rows = append(rows, []sheetsource.Cell{tCell("Zwischenstand"), fCell("SUM(B2:B41)", "9999"), tCell("-")})
		case 5:
			// (b) a placeholder-only row: no cell is empty, every one is a
			// null token.
			rows = append(rows, []sheetsource.Cell{tCell("-"), tCell("n/a"), tCell("k.A.")})
		default:
			rows = append(rows, []sheetsource.Cell{
				tCell(fmt.Sprintf("Haus %d", i)),
				nCell(fmt.Sprintf("%d", 100+i)),
				tCell(fmt.Sprintf("Ort %d", i)),
			})
		}
	}
	src := &memSource{info: sheetsource.SheetInfo{Index: 0, Name: "Sheet1"}, rows: rows}
	sp, regionIdx := profileOne(t, src)

	m := NewMaterializer(pool)
	t.Cleanup(func() { _ = m.DropTablesForFile(context.Background(), fileID) })
	res, err := m.MaterializeRegion(context.Background(), RegionInput{
		Source: src, SheetIndex: 0, Sheet: src.info, Profile: sp.Regions[regionIdx], RegionIndex: regionIdx,
		FileID: fileID, KBID: kbID, FileName: "synthetic.xlsx", MaxDistinct: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.DerivedRowsSkipped == 0 {
		t.Fatal("fixture no longer produces a derived row — the test would not exercise the divergence")
	}
	if res.RowsSkippedEmpty == 0 {
		t.Fatal("fixture no longer produces an all-null-token row — the test would not exercise the divergence")
	}

	sr, err := render.RenderSheet(src, "synthetic.xlsx", sp,
		render.TableNames{{0, regionIdx}: res.TableName}, render.Options{ChunkSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	rendered := recordsByOrdinal(t, sr.Page.Text)
	if len(rendered) == 0 {
		t.Fatalf("no records parsed out of the page:\n%s", sr.Page.Text)
	}

	dbRows, err := pool.Query(context.Background(),
		fmt.Sprintf(`SELECT "_rowid", "gebaeude" FROM tabular.%q ORDER BY "_rowid"`, res.TableName))
	if err != nil {
		t.Fatal(err)
	}
	stored := map[int]string{}
	for dbRows.Next() {
		var id int
		var name string
		if err := dbRows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		stored[id] = name
	}
	dbRows.Close()

	if len(stored) != len(rendered) {
		t.Errorf("materialised %d rows but rendered %d records", len(stored), len(rendered))
	}
	for id, want := range stored {
		got, ok := rendered[id]
		if !ok {
			t.Errorf("_rowid %d (%q) has no rendered record with that ordinal", id, want)
			continue
		}
		if got != want {
			t.Errorf("ordinal %d: rendered %q but _rowid %d holds %q — markers and _rowid have drifted", id, got, id, want)
		}
	}
}

// ---------------------------------------------------------------------------
// R21 — over-long values must not reach the value index
// ---------------------------------------------------------------------------

// TestMaterializeRegionOverlongValue pins R21 against the real table:
// tabular_column_values' primary key spans (table, column, value), and
// Postgres' btree index-tuple limit makes a multi-kilobyte value fail the
// insert — which failed the WHOLE region, so one long free-text cell cost
// the file its SQL table entirely. The region must materialise, and the long
// value must simply be absent from the index.
func TestMaterializeRegionOverlongValue(t *testing.T) {
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool)

	long := strings.Repeat("ä", 3000) // 6000 bytes, well past any btree tuple limit
	rows := [][]sheetsource.Cell{{tCell("Gebäude"), tCell("Bemerkung")}}
	for i := 1; i <= 8; i++ {
		note := fmt.Sprintf("kurz %d", i)
		if i == 3 {
			note = long
		}
		rows = append(rows, []sheetsource.Cell{tCell(fmt.Sprintf("Haus %d", i)), tCell(note)})
	}
	src := &memSource{info: sheetsource.SheetInfo{Index: 0, Name: "Sheet1"}, rows: rows}
	sp, regionIdx := profileOne(t, src)

	m := NewMaterializer(pool)
	t.Cleanup(func() { _ = m.DropTablesForFile(context.Background(), fileID) })
	res, err := m.MaterializeRegion(context.Background(), RegionInput{
		Source: src, SheetIndex: 0, Sheet: src.info, Profile: sp.Regions[regionIdx], RegionIndex: regionIdx,
		FileID: fileID, KBID: kbID, FileName: "long.xlsx", MaxDistinct: 1000,
	})
	if err != nil {
		t.Fatalf("a 3000-character cell must not fail the region: %v", err)
	}
	if res.RowsMaterialised != 8 {
		t.Errorf("RowsMaterialised = %d, want 8", res.RowsMaterialised)
	}

	// The long value IS in the table (the typed column is unindexed text)...
	var stored string
	if err := pool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT "bemerkung" FROM tabular.%q WHERE "_rowid"=3`, res.TableName)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != long {
		t.Errorf("the long value must still be materialised verbatim (len %d, want %d)", len([]rune(stored)), len([]rune(long)))
	}
	// ...but not in the value index.
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tabular_column_values WHERE table_name=$1 AND column_name='bemerkung' AND value=$2`,
		res.TableName, long).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("over-long value reached tabular_column_values (%d rows)", n)
	}
	var short int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tabular_column_values WHERE table_name=$1 AND column_name='bemerkung'`,
		res.TableName).Scan(&short); err != nil {
		t.Fatal(err)
	}
	if short != 7 {
		t.Errorf("short values indexed = %d, want 7", short)
	}
	for _, st := range res.Stats {
		if st.Name == "bemerkung" && st.LongValuesSkipped != 1 {
			t.Errorf("bemerkung LongValuesSkipped = %d, want 1", st.LongValuesSkipped)
		}
	}
}
