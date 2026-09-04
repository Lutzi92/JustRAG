package parser

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/justrag/go-backend/internal/tabular/ingest"
)

// spreadsheetMIMEs holds the MIME types the SpreadsheetParser recognizes in
// addition to file extension. Deliberately excludes .xlsm (macro-enabled
// workbooks) — out of scope.
var spreadsheetMIMEs = map[string]bool{
	"text/csv":                  true,
	"text/tab-separated-values": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
	"application/vnd.ms-excel":                       true,
	"application/vnd.oasis.opendocument.spreadsheet": true,
}

// SpreadsheetParser replaces the legacy CSVParser/XLSXParser/XLSParser/
// OdsParser quartet: one streaming reader (internal/sheetsource) feeds the
// profiler + hybrid renderer (internal/tabular/ingest) in render-only mode
// (Materialize: false — no Postgres tables), producing the same
// markdown-page ParseResult shape the processor already expects.
type SpreadsheetParser struct{}

// Name implements Parser.
func (p *SpreadsheetParser) Name() string { return "spreadsheet" }

// CanParse implements Parser.
func (p *SpreadsheetParser) CanParse(mimeType, fileName string) bool {
	if spreadsheetMIMEs[mimeType] {
		return true
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".csv", ".tsv", ".xlsx", ".xls", ".ods":
		return true
	}
	return false
}

// Parse implements Parser. Runs the ingester render-only (no
// materialisation, no LLM profiling assist) — just the sheet-source read,
// heuristic profile, and hybrid markdown render.
func (p *SpreadsheetParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	res, err := ingest.New(nil, nil).Ingest(ctx, ingest.Input{
		FilePath: pctx.FilePath,
		FileName: pctx.FileName,
		KBID:     pctx.KbID,
		Options: ingest.Options{
			Materialize: false,
			SampleRows:  200,
			ChunkSize:   pctx.ChunkSize,
		},
	})
	if err != nil {
		return nil, err
	}
	out := &ParseResult{Text: res.Text, IsMarkdown: true}
	for _, pg := range res.Pages {
		out.Pages = append(out.Pages, PageText{PageNumber: pg.Number, Text: pg.Text})
	}
	return out, nil
}
