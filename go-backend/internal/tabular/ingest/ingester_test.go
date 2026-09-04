package ingest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

type fakeMat struct {
	calls   []tabular.RegionInput
	fail    bool
	dropped []string
}

func (f *fakeMat) MaterializeRegion(_ context.Context, in tabular.RegionInput) (*tabular.RegionResult, error) {
	f.calls = append(f.calls, in)
	if f.fail {
		return nil, errors.New("boom")
	}
	// drive the reader like the real thing so Progress fires and stats exist
	rows := int64(0)
	_, _ = in.Source.ReadSheet(in.SheetIndex, func(r int, cells []sheetsource.Cell) error {
		if r >= in.Profile.DataStart && cells != nil {
			rows++
		}
		return nil
	})
	cols := make([]tabular.ColumnSpec, 0, len(in.Profile.Columns))
	stats := make([]tabular.ColumnStat, 0, len(in.Profile.Columns))
	for _, c := range in.Profile.Columns {
		cols = append(cols, tabular.ColumnSpec{Original: c.Header, Name: tabular.SanitizeIdentifier(c.Header), Type: tabular.TypeText, Role: string(c.Role)})
		stats = append(stats, tabular.ColumnStat{Name: tabular.SanitizeIdentifier(c.Header), Original: c.Header, Type: "text", Role: string(c.Role)})
	}
	return &tabular.RegionResult{TableName: tabular.TableNameForRegion(in.FileID, in.SheetIndex, in.RegionIndex), Columns: cols, Stats: stats, RowsRead: rows, RowsMaterialised: rows}, nil
}
func (f *fakeMat) DropTablesForFile(_ context.Context, id string) error {
	f.dropped = append(f.dropped, id)
	return nil
}

type fakeLLM struct {
	calls int
	fail  bool
}

func (f *fakeLLM) ProfileTableRegion(_ context.Context, req ai.SheetProfileRequest) (ai.SheetProfileProposal, error) {
	f.calls++
	if f.fail {
		return ai.SheetProfileProposal{}, errors.New("llm down")
	}
	prop := ai.SheetProfileProposal{Kind: "table", Confidence: 0.9, HeaderRows: req.Heuristic.HeaderRows}
	for _, c := range req.Heuristic.Columns {
		prop.Columns = append(prop.Columns, ai.SheetProfileColumn{Index: c.Index, Name: c.Name, Role: c.Role, Description: "Beschreibung von " + c.Name})
	}
	return prop, nil
}

const fixtures = "../../sheetsource/testdata/"

func TestIngestMaterialisesAndRenders(t *testing.T) {
	t.Parallel()
	mat, llm := &fakeMat{}, &fakeLLM{}
	g := New(mat, llm)
	var details []string
	res, err := g.Ingest(context.Background(), Input{FilePath: fixtures + "header_row14_metadata.xlsx", FileName: "header_row14_metadata.xlsx", FileID: "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b", KBID: "kb",
		Options:  Options{Materialize: true, SampleRows: 200, LLM: profile.LLMOptions{Enabled: true, Threshold: 0.7}, ChunkSize: 512},
		Progress: func(d string) { details = append(details, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(mat.dropped) != 1 || len(mat.calls) < 2 { // Gebäudeliste table + the hidden Dropdown list (1-col table)
		t.Errorf("materialiser calls: dropped=%v calls=%d", mat.dropped, len(mat.calls))
	}
	if llm.calls != len(mat.calls) {
		t.Errorf("llm calls %d != table regions %d", llm.calls, len(mat.calls))
	}
	if len(res.Pages) != 3 || res.Pages[1].Number != 2 {
		t.Errorf("pages: %+v", res.Pages)
	}
	main := res.Pages[0].Text
	// Deviation from the brief's literal "rows 1–20]" (see task-5-report.md):
	// this fixture's 15 wide, long-headered columns put a single record at
	// ~200 tokens, so two records already exceed the ChunkSize:512 budget
	// (409 tokens) and render.RenderSheet (Task 1, unmodified here) flushes
	// after every row. All 20 rows still land in the table (RowsEmbedded
	// checked below); the marker for the first (single-row) block is
	// "rows 1–1]".
	if !strings.Contains(main, "[tabular.sheet_0f1e2d3c4b5a69788a9b0c1d2e3f4a5b_0_0 rows 1–1]") || !strings.Contains(main, "Beschreibung von") {
		t.Errorf("main page lacks marker or LLM description:\n%s", main[:min(len(main), 800)])
	}
	// CardStats must reach the card as the materialiser's SQL identifier +
	// type, not just the profile's role/description fallback.
	if !strings.Contains(main, "[stammdaten_ressort text]") {
		t.Error("main page card missing materialiser-derived SQLName/Type bracket")
	}
	if !strings.Contains(main, "Gesamtliste landeseigene Gebäude") {
		t.Error("prose above missing")
	}
	rep := res.Report
	if !rep.Materialised || len(rep.Sheets) != 3 || rep.Sheets[0].Kind != "table" || rep.Sheets[0].HeaderRow != 13 || rep.Sheets[0].RowsEmbedded != 20 || !rep.Sheets[0].UsedLLM || rep.Sheets[1].Hidden != true || rep.Sheets[2].Kind != "prose" {
		t.Errorf("report: %+v", rep)
	}
	if len(details) == 0 || !strings.Contains(details[0], "Blatt 1/3") {
		t.Errorf("progress details: %v", details)
	}
}

func TestIngestRenderOnlyAndLLMFailureAreSoft(t *testing.T) {
	t.Parallel()
	g := New(nil, &fakeLLM{fail: true})
	res, err := g.Ingest(context.Background(), Input{FilePath: fixtures + "ids_leading_zero.xlsx", FileName: "ids_leading_zero.xlsx", FileID: "f", KBID: "kb",
		Options: Options{Materialize: false, SampleRows: 200, LLM: profile.LLMOptions{Enabled: true}, ChunkSize: 512}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Materialised || len(res.Tables) != 0 || !strings.Contains(res.Pages[0].Text, "[rows 1–8]") || !strings.Contains(res.Pages[0].Text, "Lieferantennummer: 0002001919") {
		t.Errorf("render-only: %+v\n%s", res.Report, res.Pages[0].Text)
	}
	if res.Report.Sheets[0].UsedLLM {
		t.Error("failed LLM call must not be reported as used")
	}
}

func TestIngestMaterialiseFailureKeepsText(t *testing.T) {
	t.Parallel()
	g := New(&fakeMat{fail: true}, nil)
	res, err := g.Ingest(context.Background(), Input{FilePath: fixtures + "ids_leading_zero.xlsx", FileName: "ids_leading_zero.xlsx", FileID: "f", KBID: "kb",
		Options: Options{Materialize: true, SampleRows: 200, ChunkSize: 512}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Materialised || len(res.Report.Sheets[0].Notes) == 0 || !strings.Contains(res.Pages[0].Text, "Lieferantennummer: 0002001919") {
		t.Errorf("failure must be soft: %+v", res.Report)
	}
}
