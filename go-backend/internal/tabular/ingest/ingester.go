// Package ingest is the one-file spreadsheet ingestion orchestrator (Phase 2
// of the 2026-09-04 spreadsheet-ingest rework, Task 5): open the sheet
// source, profile every sheet, optionally LLM-assist each table region's
// profile, optionally materialise table regions into native Postgres
// tables, render the hybrid text page for the standard chunk/embed
// pipeline, and assemble a per-file ParseReport.
//
// Ruling R5 (see the task-5 brief): this package deliberately does NOT
// import internal/parser — Task 6 wires ingest into the parser factory,
// which would create a parser<->ingest import cycle if this package also
// depended on parser.ParseResult. Result/Page below are ingest's own types;
// Task 6 converts them to parser.ParseResult when wiring the factory.
package ingest

import (
	"context"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/profile"
	"github.com/justrag/go-backend/internal/tabular/render"
)

// RegionMaterializer is the slice of *tabular.Materializer the ingester
// needs (test seam — internal/tabular/ingest_test.go stubs it with a fake).
type RegionMaterializer interface {
	MaterializeRegion(ctx context.Context, in tabular.RegionInput) (*tabular.RegionResult, error)
	DropTablesForFile(ctx context.Context, fileID string) error
}

// Options configures one file's ingest pass. Field comments name the
// site_config key each one is resolved from by the caller (Task 6/7).
type Options struct {
	Materialize  bool               // chat_tabular_query_enabled
	SampleRows   int                // tabular_profile_sample_rows (200)
	LLM          profile.LLMOptions // Enabled from tabular_profile_llm_enabled, Threshold
	MaxRows      int                // tabular_max_rows
	EmbedMaxRows int                // tabular_embed_max_rows
	MaxDistinct  int                // tabular_column_values_max_distinct
	ChunkSize    int                // for block sizing
	// Lang and Model are NOT here: the LLM prompt language and model
	// override are carried on the AIProfiler passed to WithLLM (see llm.go)
	// instead — Ingest itself never reads either, so duplicating them here
	// was dead configuration (fix round 1, item 3).
}

// Input is one file's ingest request.
type Input struct {
	FilePath, FileName, FileID, KBID string
	Options                          Options
	// Progress receives human-readable stage detail ("Blatt 2/3 · 120000
	// Zeilen"); may be nil.
	Progress func(detail string)
}

// Page is one sheet's rendered text (ruling R5: ingest's own type, not
// parser.PageText). Number is 1-based, matching the sheet's position.
type Page struct {
	Number int
	Text   string
}

// Result is one file's ingest outcome (ruling R5: ingest's own type, not
// parser.ParseResult). Text is every page's Text joined by "\n\n", for
// callers that don't care about per-page splitting.
type Result struct {
	Pages  []Page
	Text   string
	Report tabular.ParseReport
	Tables []string // materialised table names
}

// Ingester runs one file's ingest pass. A nil mat means render-only (no
// materialisation, even when Options.Materialize is set); a nil llm means
// heuristics-only (no LLM assist, even when Options.LLM.Enabled is set).
type Ingester struct {
	mat RegionMaterializer
	llm profile.LLMProfiler
}

// New builds an Ingester. mat may be nil (render-only deployments); llm may
// be nil (LLM assist disabled or unavailable).
func New(mat RegionMaterializer, llm profile.LLMProfiler) *Ingester {
	return &Ingester{mat: mat, llm: llm}
}

// WithLLM returns a shallow copy of g with the LLM profiler swapped. The
// shared Ingester (built once, holding the materialiser) has no per-file
// state; the LLM adapter does (KB id, language, model override), so Task
// 6/7 builds one *AIProfiler per file and calls WithLLM rather than
// reconstructing the whole Ingester (ruling R6).
func (g *Ingester) WithLLM(llm profile.LLMProfiler) *Ingester {
	cp := *g
	cp.llm = llm
	return &cp
}

// Ingest runs the full sequence documented in the task-5 brief: open,
// profile every sheet, LLM-assist table regions, materialise table
// regions (fail-soft per region), render, and assemble the report.
func (g *Ingester) Ingest(ctx context.Context, in Input) (*Result, error) {
	opts := in.Options
	if opts.SampleRows <= 0 {
		opts.SampleRows = 200
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 512
	}
	progress := func(d string) {
		if in.Progress != nil {
			in.Progress(d)
		}
	}

	src, err := sheetsource.Open(in.FilePath, in.FileName)
	if err != nil {
		return nil, fmt.Errorf("ingest: open %q: %w", in.FileName, err)
	}
	defer src.Close()

	// R17: drop this file's old tables/catalog rows whenever a materialiser
	// is wired, regardless of whether THIS run is materialising — not only
	// when materialise is true. Otherwise a re-ingest with
	// chat_tabular_query_enabled toggled off (materialise=false) leaves the
	// previous run's tables and tabular_catalog rows behind: stale rows
	// that no longer correspond to the current file content, discoverable
	// by table_query and never cleaned up short of a manual DROP.
	if g.mat != nil {
		if err := g.mat.DropTablesForFile(ctx, in.FileID); err != nil {
			return nil, fmt.Errorf("ingest: drop old tables: %w", err)
		}
	}
	materialise := opts.Materialize && g.mat != nil

	sheets := src.Sheets()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("ingest: %q has no sheets", in.FileName)
	}
	res := &Result{Report: tabular.ParseReport{Version: 1, Materialised: materialise}}
	var pages []Page
	var anySheetOK bool

	// Ruling R13: a per-sheet failure (a corrupt sheet, a read error partway
	// through) is fail-soft — it must not abort the rest of the file. This
	// matters most on a re-ingest: DropTablesForFile has already run above,
	// so aborting Ingest entirely on sheet k's error would leave every
	// OTHER sheet's tables dropped too, with nothing materialised to
	// replace them. Ingest only returns an error when the source can't be
	// opened at all (above), it has no sheets (above), or every sheet
	// failed (below).
	for _, info := range sheets {
		progress(fmt.Sprintf("Blatt %d/%d · Profil", info.Index+1, len(sheets)))

		page, rep, err := g.ingestSheet(ctx, src, info, len(sheets), in, opts, materialise, res, progress)
		if err != nil {
			logctx.From(ctx).Warn("tabular: sheet failed; skipping", "sheet", info.Name, "error", err.Error())
			note := fmt.Sprintf("Blatt konnte nicht gelesen werden: %s", SanitizeNote(err.Error()))
			rep = tabular.SheetReport{Name: info.Name, Hidden: info.Hidden, HeaderRow: -1, Notes: []string{note}}
			page = Page{Number: info.Index + 1, Text: fmt.Sprintf("# %s\n\nBlatt konnte nicht gelesen werden.", info.Name)}
		} else {
			anySheetOK = true
		}
		pages = append(pages, page)
		res.Report.Sheets = append(res.Report.Sheets, rep)
	}

	if !anySheetOK {
		return nil, fmt.Errorf("ingest: every sheet in %q failed", in.FileName)
	}

	texts := make([]string, len(pages))
	for i, p := range pages {
		texts[i] = p.Text
	}
	res.Pages = pages
	res.Text = strings.Join(texts, "\n\n")
	return res, nil
}

// ingestSheet runs one sheet's profile → LLM-assist → materialise → render
// sequence. A materialise failure on one table REGION is handled inside
// this method and never returned as an error (existing fail-soft
// behaviour, unchanged); an error returned by this method means the whole
// SHEET could not be profiled or rendered (CollectSample/RenderSheet
// failed), which Ingest's caller turns into the placeholder page/report
// per ruling R13. res.Tables is mutated directly for any region that did
// materialise before such a failure, since that table genuinely exists in
// Postgres regardless of whether this sheet's own report/page could be
// built.
func (g *Ingester) ingestSheet(ctx context.Context, src sheetsource.Source, info sheetsource.SheetInfo, sheetCount int, in Input, opts Options, materialise bool, res *Result, progress func(string)) (Page, tabular.SheetReport, error) {
	sample, err := sheetsource.CollectSample(src, info.Index, opts.SampleRows)
	if err != nil {
		return Page{}, tabular.SheetReport{}, fmt.Errorf("collect sample: %w", err)
	}
	sp := profile.ProfileSheet(sample, profile.Options{})
	rep := tabular.SheetReport{Name: info.Name, Kind: string(sp.Kind), Hidden: info.Hidden, HeaderRow: -1}
	names := render.TableNames{}
	cardStats := map[[2]int][]render.ColumnCardStat{}
	rowCounts := map[[2]int]int{}

	for ri := range sp.Regions {
		rp := &sp.Regions[ri]
		if rp.Kind != profile.KindTable {
			continue
		}

		if g.llm != nil && opts.LLM.Enabled {
			req := profile.BuildLLMRequest(sample, *rp, in.FileName, opts.LLM.MaxRows)
			if prop, err := g.llm.ProfileTableRegion(ctx, req); err != nil {
				logctx.From(ctx).Warn("tabular: profiler llm failed; using heuristics", "sheet", info.Name, "region", ri, "error", err.Error())
			} else {
				profile.ApplyLLM(rp, prop, rp.Confidence, opts.LLM)
			}
		}

		if rep.HeaderRow < 0 && len(rp.HeaderRows) > 0 {
			rep.HeaderRow = rp.HeaderRows[len(rp.HeaderRows)-1]
			rep.Columns = len(rp.Columns)
			rep.DroppedColumns = len(rp.Dropped)
		}
		rep.UsedLLM = rep.UsedLLM || rp.UsedLLM
		if fe, ok := rp.Diagnostics["formula_cells_empty"].(int); ok {
			rep.FormulaCellsEmpty = fe
		}

		key := [2]int{info.Index, ri}
		if materialise {
			rr, err := g.mat.MaterializeRegion(ctx, tabular.RegionInput{
				Source: src, SheetIndex: info.Index, Sheet: info, Profile: *rp, RegionIndex: ri,
				FileID: in.FileID, KBID: in.KBID, FileName: in.FileName, MaxRows: opts.MaxRows, MaxDistinct: opts.MaxDistinct,
				Progress: func(rows int) {
					progress(fmt.Sprintf("Blatt %d/%d · %d Zeilen", info.Index+1, sheetCount, rows))
				},
			})
			if err != nil {
				logctx.From(ctx).Warn("tabular: materialise failed; text path only", "sheet", info.Name, "region", ri, "error", err.Error())
				rep.Notes = append(rep.Notes, fmt.Sprintf("region %d: materialisation failed: %s", ri, SanitizeNote(err.Error())))
				res.Report.Materialised = false
			} else {
				names[key] = rr.TableName
				res.Tables = append(res.Tables, rr.TableName)
				rep.Tables = append(rep.Tables, rr.TableName)
				rep.RowsRead += int(rr.RowsRead)
				rep.RowsMaterialised += int(rr.RowsMaterialised)
				rep.CoercionFailures += int(rr.CoercionFailures)
				if rr.RowsDropped > 0 {
					rep.Notes = append(rep.Notes, fmt.Sprintf("region %d: %d rows past tabular_max_rows dropped", ri, rr.RowsDropped))
				}
				cardStats[key] = cardStatsFromResult(rr, *rp)
				rowCounts[key] = int(rr.RowsMaterialised)
			}
		}
		// R13/round-1 fix: no ingest-side fallback here. render/kv.go's
		// renderTable already falls back to columnCardStatsFromProfile
		// + rowCount = RowsEmbedded+RowsPastCap when a region's key is
		// ABSENT from CardStats — pre-filling cardStats[key] for every
		// unmaterialised region (render-only mode, or a per-region
		// materialise failure) defeated that fallback's row-count arm
		// and always printed "0 Zeilen" on the card (RenderSheet hasn't
		// run yet at this point in the loop, so RowsEmbedded/RowsPastCap
		// aren't known here either way).
	}

	sr, err := render.RenderSheet(src, in.FileName, sp, names, render.Options{
		ChunkSize: opts.ChunkSize, EmbedMaxRows: opts.EmbedMaxRows, CardStats: cardStats, RowCounts: rowCounts,
	})
	if err != nil {
		return Page{}, tabular.SheetReport{}, fmt.Errorf("render: %w", err)
	}
	for _, r := range sr.Regions {
		rep.RowsEmbedded += r.RowsEmbedded
		rep.RowsPastCap += r.RowsPastCap
	}
	if !materialise {
		rep.RowsRead = rep.RowsEmbedded + rep.RowsPastCap
	}
	return Page{Number: sr.Page.PageNumber, Text: sr.Page.Text}, rep, nil
}
