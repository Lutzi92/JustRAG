package parser

import (
	"context"
	"strings"
	"testing"
)

func TestSpreadsheetParserRendersKeyValuePages(t *testing.T) {
	t.Parallel()
	p := &SpreadsheetParser{}
	for _, c := range []struct {
		mime, name string
		want       bool
	}{
		{"text/csv", "a.csv", true}, {"", "a.xlsx", true}, {"application/vnd.ms-excel", "x", true}, {"", "a.ods", true}, {"", "a.xls", true}, {"application/pdf", "a.pdf", false}, {"", "a.xlsm", false},
	} {
		if got := p.CanParse(c.mime, c.name); got != c.want {
			t.Errorf("CanParse(%q,%q)=%v", c.mime, c.name, got)
		}
	}
	res, err := p.Parse(context.Background(), ParseContext{FilePath: "../sheetsource/testdata/header_row14_metadata.xlsx", FileName: "header_row14_metadata.xlsx", ChunkSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsMarkdown || len(res.Pages) != 3 || !strings.Contains(res.Pages[0].Text, "Stammdaten / Ressort: HMWK") || strings.Contains(res.Text, "| --- |") {
		t.Errorf("result: pages=%d md=%v\n%s", len(res.Pages), res.IsMarkdown, res.Pages[0].Text[:300])
	}
	if strings.Count(res.Text, "\n|\n") > 0 {
		t.Error("the bare-pipe data loss of the old parser must be gone")
	}
	csvRes, err := p.Parse(context.Background(), ParseContext{FilePath: "../sheetsource/testdata/bom_semicolon_cp1252.csv", FileName: "bom_semicolon_cp1252.csv", ChunkSize: 512})
	if err != nil || len(csvRes.Pages) != 1 || !strings.Contains(csvRes.Pages[0].Text, "Gebäude:") {
		t.Errorf("csv: %v %+v", err, csvRes)
	}
}
