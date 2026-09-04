package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/splitter"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

func splitterCount(s string) int {
	return splitter.CountTokens(s)
}

func openFixture(t *testing.T, name string) sheetsource.Source {
	t.Helper()
	src, err := sheetsource.Open("../../sheetsource/testdata/"+name, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	return src
}

func profileOf(t *testing.T, src sheetsource.Source, sheet int) profile.SheetProfile {
	t.Helper()
	s, err := sheetsource.CollectSample(src, sheet, 200)
	if err != nil {
		t.Fatal(err)
	}
	return profile.ProfileSheet(s, profile.Options{})
}

func TestRenderTableAsKeyValueRecords(t *testing.T) {
	t.Parallel()
	src := openFixture(t, "ids_leading_zero.xlsx")
	sp := profileOf(t, src, 0)
	out, err := RenderSheet(src, "ids_leading_zero.xlsx", sp, TableNames{{0, 0}: "sheet_abc_0_0"}, Options{ChunkSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	txt := out.Page.Text
	if out.Page.PageNumber != 1 || !strings.HasPrefix(txt, "### ids_leading_zero.xlsx › Sheet1") {
		t.Fatalf("page: %d %q", out.Page.PageNumber, txt[:60])
	}
	if !strings.Contains(txt, "[tabular.sheet_abc_0_0 rows 1–8]") {
		t.Errorf("marker missing:\n%s", txt)
	}
	if !strings.Contains(txt, "Lieferantennummer: 0002001919 | Material: 931404826 | GIS-Code: 01.1440.055_.10") {
		t.Errorf("record shape wrong:\n%s", txt)
	}
	if strings.Contains(txt, "| --- |") || strings.Contains(txt, "Lieferantennummer | Material") {
		t.Error("markdown table rendering must be gone")
	}
	if out.Regions[0].RowsEmbedded != 8 || out.Regions[0].Blocks < 1 {
		t.Errorf("region stats: %+v", out.Regions[0])
	}
	// All 8 records land in one block at ChunkSize 512 (Blocks == 1); that
	// block holds far more than one record, so R3's single-oversized-record
	// exemption cannot excuse an overshoot — it must respect the budget.
	found := false
	for _, block := range strings.Split(txt, "\n\n") {
		// I7: heading line first, marker line second.
		head, rest, ok := strings.Cut(block, "\n")
		if !ok || head != "### ids_leading_zero.xlsx › Sheet1" || !strings.HasPrefix(rest, "[tabular.sheet_abc_0_0 rows") {
			continue
		}
		found = true
		chunkSize := 512
		if budget := int(0.8 * float64(chunkSize)); splitterCount(block) > budget {
			t.Errorf("block over budget (%d tokens > %d):\n%s", splitterCount(block), budget, block)
		}
	}
	if !found {
		t.Fatal("marker block not found in page text")
	}
}

// TestBlockHeadingRepeatsPerBlock pins I7: the spec's "### <file> › <sheet>"
// heading opens EVERY row block, not just the page. internal/processor only
// records the enclosing heading in chunk metadata (SectionsForChunk →
// meta["sections"]) and never prepends it to the embedded chunk text, so a
// block that becomes its own chunk would otherwise be embedded with no file
// or sheet name in it at all.
func TestBlockHeadingRepeatsPerBlock(t *testing.T) {
	t.Parallel()
	const dataRows = 40
	grid := narrowGrid(dataRows)
	src := &fakeSource{sheet: sheetsource.SheetInfo{Index: 0, Name: "Sheet1"}, rows: grid}
	sample := &sheetsource.Sample{
		Info: src.sheet, Rows: grid, Width: 3, TotalRows: len(grid),
		Extras: sheetsource.SheetExtras{MaxCol: 3, RowCount: len(grid)},
	}
	sp := profile.ProfileSheet(sample, profile.Options{})
	out, err := RenderSheet(src, "synthetic.xlsx", sp, TableNames{{0, 0}: "sheet_x_0_0"}, Options{ChunkSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	rr := out.Regions[0]
	if rr.Blocks < 3 {
		t.Fatalf("need several blocks to make the point, got %d", rr.Blocks)
	}
	markers := 0
	for _, block := range strings.Split(out.Page.Text, "\n\n") {
		head, rest, ok := strings.Cut(block, "\n")
		if !ok || !strings.HasPrefix(rest, "[tabular.sheet_x_0_0 rows") {
			continue
		}
		markers++
		if head != "### synthetic.xlsx › Sheet1" {
			t.Errorf("block %d does not open with its own heading: %q", markers, head)
		}
	}
	if markers != rr.Blocks {
		t.Errorf("found %d marker blocks, region reported %d", markers, rr.Blocks)
	}
}

func TestRenderPercentHeaderAndDerivedRows(t *testing.T) {
	t.Parallel()
	src := openFixture(t, "totals_rows.xlsx")
	sp := profileOf(t, src, 0)
	out, err := RenderSheet(src, "totals_rows.xlsx", sp, nil, Options{ChunkSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	txt := out.Page.Text
	if !strings.Contains(txt, "Gesamt") || !strings.Contains(txt, "Stand: 2026-08") {
		t.Errorf("prose above missing:\n%s", txt)
	}
	if strings.Contains(txt, "Gebäude: Summe") || !strings.Contains(txt, "Summe:") {
		t.Errorf("derived row must be a prose line, not a record:\n%s", txt)
	}
	if out.Regions[0].RowsEmbedded != 10 {
		t.Errorf("RowsEmbedded = %d", out.Regions[0].RowsEmbedded)
	}
	src2 := openFixture(t, "numbers_formats.xlsx")
	sp2 := profileOf(t, src2, 0)
	out2, _ := RenderSheet(src2, "numbers_formats.xlsx", sp2, nil, Options{ChunkSize: 512})
	if !strings.Contains(out2.Page.Text, "Anteil (%): 36.5") || !strings.Contains(out2.Page.Text, "Betrag (€):") {
		t.Errorf("unit headers:\n%s", out2.Page.Text)
	}
}

func TestRenderFormProseAndCap(t *testing.T) {
	t.Parallel()
	src := openFixture(t, "steckbrief_form.xlsx")
	sp := profileOf(t, src, 0)
	out, _ := RenderSheet(src, "steckbrief_form.xlsx", sp, nil, Options{ChunkSize: 512})
	if !strings.Contains(out.Page.Text, "Gebäude: Physiologie") || !strings.Contains(out.Page.Text, "Adresse: Aulweg 129") {
		t.Errorf("form lines:\n%s", out.Page.Text)
	}
	src2 := openFixture(t, "header_row14_metadata.xlsx")
	sp2 := profileOf(t, src2, 2)
	out2, _ := RenderSheet(src2, "header_row14_metadata.xlsx", sp2, nil, Options{ChunkSize: 512})
	if out2.Page.PageNumber != 3 || !strings.Contains(out2.Page.Text, "Zu erfassen sind alle Gebäude.") {
		t.Errorf("prose sheet:\n%s", out2.Page.Text)
	}
	sp3 := profileOf(t, src2, 0)
	// M1: no table name here (nil TableNames), so the rows past the cap are
	// reachable by nothing at all — the card must NOT point at table_query.
	out3, _ := RenderSheet(src2, "header_row14_metadata.xlsx", sp3, nil, Options{ChunkSize: 512, EmbedMaxRows: 5})
	if out3.Regions[0].RowsEmbedded != 5 || out3.Regions[0].RowsPastCap != 15 ||
		!strings.Contains(out3.Page.Text, "Zeilen 6–20 sind nicht eingebettet und hier nicht abrufbar") ||
		strings.Contains(out3.Page.Text, "table_query") {
		t.Errorf("cap without a table: %+v\n%s", out3.Regions[0], out3.Page.Text)
	}
	// Same region WITH a materialised table: now table_query really can
	// reach the capped rows, so the card says so.
	out3t, _ := RenderSheet(src2, "header_row14_metadata.xlsx", sp3, TableNames{{0, 0}: "sheet_cap_0_0"}, Options{ChunkSize: 512, EmbedMaxRows: 5})
	if !strings.Contains(out3t.Page.Text, "Zeilen 6–20 sind nur über table_query erreichbar") ||
		strings.Contains(out3t.Page.Text, "nicht abrufbar") {
		t.Errorf("cap with a table:\n%s", out3t.Page.Text)
	}
	if !strings.Contains(out3.Page.Text, "Stammdaten / Ressort: HMWK") {
		t.Errorf("joined header must be the record key:\n%s", out3.Page.Text)
	}
	hidden := profileOf(t, src2, 1)
	outH, _ := RenderSheet(src2, "header_row14_metadata.xlsx", hidden, nil, Options{ChunkSize: 512})
	if !strings.Contains(outH.Page.Text, "(hidden)") {
		t.Error("hidden heading")
	}
}

func TestBlocksRespectTokenBudget(t *testing.T) {
	t.Parallel()
	src := openFixture(t, "header_row14_metadata.xlsx")
	sp := profileOf(t, src, 0)
	out, _ := RenderSheet(src, "header_row14_metadata.xlsx", sp, nil, Options{ChunkSize: 128})
	if out.Regions[0].Blocks < 3 {
		t.Fatalf("expected several blocks at 128 tokens, got %d", out.Regions[0].Blocks)
	}
	for _, block := range strings.Split(out.Page.Text, "\n\n") {
		if strings.HasPrefix(block, "[rows") && splitterCount(block) > 128 {
			// The budget is allowed to be exceeded only when the block holds a
			// single record (a record larger than the whole budget forms its
			// own block by design — R3).
			lines := strings.Split(strings.TrimPrefix(block, "["), "\n")
			recordLines := 0
			for _, l := range lines[1:] {
				if strings.TrimSpace(l) != "" {
					recordLines++
				}
			}
			if recordLines >= 2 {
				t.Errorf("block over budget (%d tokens):\n%s", splitterCount(block), block)
			}
		}
	}
}

// fakeSource is a minimal in-memory sheetsource.Source over a single sheet,
// used to exercise the multi-record batching path with a table narrow
// enough that several records fit in one 0.8×ChunkSize block — every
// fixture table in the other tests here is wide enough that each record
// alone exceeds the budget, so the batching branch of the block-flush logic
// (splitter.CountTokens(block.String()+rec) > budget) never actually runs
// against them.
type fakeSource struct {
	sheet sheetsource.SheetInfo
	rows  [][]sheetsource.Cell
}

func (f *fakeSource) Sheets() []sheetsource.SheetInfo { return []sheetsource.SheetInfo{f.sheet} }

func (f *fakeSource) ReadSheet(index int, fn sheetsource.RowFunc) (sheetsource.SheetExtras, error) {
	width := 0
	for _, r := range f.rows {
		if len(r) > width {
			width = len(r)
		}
	}
	for i, r := range f.rows {
		if err := fn(i, r); err != nil {
			if err == sheetsource.ErrStop {
				break
			}
			return sheetsource.SheetExtras{}, err
		}
	}
	return sheetsource.SheetExtras{MaxCol: width, RowCount: len(f.rows)}, nil
}

func (f *fakeSource) Close() error { return nil }

func textCell(s string) sheetsource.Cell {
	return sheetsource.Cell{Kind: sheetsource.KindText, Raw: s, Formatted: s}
}

func numCell(s string) sheetsource.Cell {
	return sheetsource.Cell{Kind: sheetsource.KindNumber, Raw: s, Formatted: s}
}

// narrowGrid builds a 3-column header + n data-row grid: "A|B|C" header,
// data rows "x<i>|<i>|y<i>" — short enough that several records fit inside
// a small token budget, unlike every real fixture used above.
func narrowGrid(n int) [][]sheetsource.Cell {
	rows := [][]sheetsource.Cell{{textCell("A"), textCell("B"), textCell("C")}}
	for i := 1; i <= n; i++ {
		rows = append(rows, []sheetsource.Cell{
			textCell(fmt.Sprintf("x%d", i)),
			numCell(fmt.Sprintf("%d", i)),
			textCell(fmt.Sprintf("y%d", i)),
		})
	}
	return rows
}

func TestBlocksBatchRecordsWithinBudget(t *testing.T) {
	t.Parallel()
	const dataRows = 40
	grid := narrowGrid(dataRows)
	src := &fakeSource{sheet: sheetsource.SheetInfo{Index: 0, Name: "Sheet1"}, rows: grid}

	sample := &sheetsource.Sample{
		Info:      src.sheet,
		Rows:      grid,
		Width:     3,
		TotalRows: len(grid),
		Extras:    sheetsource.SheetExtras{MaxCol: 3, RowCount: len(grid)},
	}
	sp := profile.ProfileSheet(sample, profile.Options{})
	if len(sp.Regions) != 1 || sp.Regions[0].Kind != profile.KindTable {
		t.Fatalf("profile did not detect a single table region: %+v", sp)
	}
	rp := sp.Regions[0]
	if rp.DataStart != 1 || len(rp.Columns) != 3 {
		t.Fatalf("unexpected region shape: DataStart=%d columns=%d (%+v)", rp.DataStart, len(rp.Columns), rp)
	}

	out, err := RenderSheet(src, "synthetic.xlsx", sp, nil, Options{ChunkSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	rr := out.Regions[0]
	if rr.Blocks < 3 {
		t.Fatalf("expected several blocks at ChunkSize 64, got %d: %+v", rr.Blocks, rr)
	}
	if rr.RowsEmbedded != dataRows {
		t.Fatalf("RowsEmbedded = %d, want %d", rr.RowsEmbedded, dataRows)
	}

	chunkSize := 64
	// I7: a block is heading + marker + records, and the WHOLE block has to
	// fit the chunk budget — the renderer subtracts both fixed lines from
	// the record budget, so measuring the assembled block against the plain
	// chunk budget is the real guard.
	budget := int(0.8 * float64(chunkSize))
	sawMultiRecordBlock := false
	nextWant := 1
	for _, block := range strings.Split(out.Page.Text, "\n\n") {
		head, rest, ok := strings.Cut(block, "\n")
		if !ok || !strings.HasPrefix(head, "### ") || !strings.HasPrefix(rest, "[rows") {
			continue
		}
		parts := strings.SplitN(rest, "\n", 2)
		var a, b int
		if _, err := fmt.Sscanf(parts[0], "[rows %d–%d]", &a, &b); err != nil {
			t.Fatalf("marker parse %q: %v", parts[0], err)
		}
		if a != nextWant {
			t.Errorf("marker range not contiguous: got start %d, want %d (%q)", a, nextWant, parts[0])
		}
		nextWant = b + 1

		recordLines := 0
		if len(parts) > 1 {
			for _, l := range strings.Split(parts[1], "\n") {
				if strings.TrimSpace(l) != "" {
					recordLines++
				}
			}
		}
		if recordLines >= 2 {
			sawMultiRecordBlock = true
			if tok := splitterCount(block); tok > budget {
				t.Errorf("multi-record block over budget (%d tokens > %d):\n%s", tok, budget, block)
			}
		}
	}
	if !sawMultiRecordBlock {
		t.Fatal("no multi-record block observed — the batching path was not exercised")
	}
	if nextWant-1 != dataRows {
		t.Errorf("marker ranges don't sum to %d rows: last end %d", dataRows, nextWant-1)
	}
}
