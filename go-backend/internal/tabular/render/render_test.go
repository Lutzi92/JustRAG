package render

import (
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
	out3, _ := RenderSheet(src2, "header_row14_metadata.xlsx", sp3, nil, Options{ChunkSize: 512, EmbedMaxRows: 5})
	if out3.Regions[0].RowsEmbedded != 5 || out3.Regions[0].RowsPastCap != 15 || !strings.Contains(out3.Page.Text, "Zeilen 6–20 sind nur über table_query erreichbar") {
		t.Errorf("cap: %+v\n%s", out3.Regions[0], out3.Page.Text)
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
