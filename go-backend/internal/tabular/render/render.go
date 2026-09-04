// Package render turns a sheet's profile (internal/tabular/profile) into the
// text page that reaches the standard ingest/chunk/embed pipeline: table
// regions become key:value records grouped into token-budgeted blocks, form
// regions become "Label: Value" lines, and prose regions become plain text
// lines. See the 2026-09-04 spreadsheet-ingest-phase2 spec §4.5.
package render

import (
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

// Page is one sheet's rendered page (ruling R5: render's own type, not
// parser.PageText — internal/parser now imports internal/tabular/ingest,
// which imports this package, so importing parser here would be a cycle).
// Task 6's ingester converts this to parser.PageText field-for-field.
type Page struct {
	PageNumber int // 1-based
	Text       string
}

// Options bound the text output; TableName is the materialised table for the
// marker line ("" when the region was not materialised).
type Options struct {
	ChunkSize    int // tokens; blocks are sized to 0.8 × ChunkSize; default 512
	EmbedMaxRows int // per table region; default 50 000

	// CardStats supplies the profile-card column stats for a table region,
	// keyed by [sheetIndex, regionIndex]. Filled by the ingester from the
	// materialiser; a missing entry falls back to stats derived from the
	// region's ColumnProfiles.
	CardStats map[[2]int][]ColumnCardStat
	// RowCounts supplies the row count for a table region's profile card,
	// keyed by [sheetIndex, regionIndex]. Used together with a CardStats
	// entry; when the entry is missing, the rendered row count
	// (RowsEmbedded + RowsPastCap) is used instead.
	RowCounts map[[2]int]int
}

// RegionRender carries the per-region render outcome.
type RegionRender struct {
	RegionIndex  int
	Kind         profile.SheetKind
	TableName    string // "" when not materialised
	RowsEmbedded int
	RowsPastCap  int
	Blocks       int
}

// SheetRender carries the per-sheet render outcome.
type SheetRender struct {
	Sheet   sheetsource.SheetInfo
	Page    Page // PageNumber = sheet index + 1
	Regions []RegionRender
}

// TableNames maps (sheetIdx, regionIdx) to the materialised table name; nil
// or missing entries render without a marker line.
type TableNames map[[2]int]string

const (
	defaultChunkSize    = 512
	defaultEmbedMaxRows = 50_000
)

// RenderSheet streams the sheet once more through src and produces its page.
// Table regions become key:value records; form regions Label: Value lines;
// prose regions text lines; hidden sheets carry "(hidden)" in the heading.
func RenderSheet(src sheetsource.Source, fileName string, sp profile.SheetProfile, names TableNames, opts Options) (SheetRender, error) {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = defaultChunkSize
	}
	if opts.EmbedMaxRows <= 0 {
		opts.EmbedMaxRows = defaultEmbedMaxRows
	}

	out := SheetRender{Sheet: sp.Sheet, Page: Page{PageNumber: sp.Sheet.Index + 1}}

	var b strings.Builder
	fmt.Fprintf(&b, "### %s › %s", fileName, sp.Sheet.Name)
	if sp.Sheet.Hidden {
		b.WriteString(" (hidden)")
	}
	b.WriteString("\n\n")

	if len(sp.Regions) == 0 {
		out.Page.Text = strings.TrimRight(b.String(), "\n")
		return out, nil
	}

	// One pass over the sheet: collect the rows every region needs, plus the
	// highest row index actually delivered (used as the upper bound for an
	// open-ended table region, whose Region.Bottom only reflects the sample
	// window CollectSample profiled, not necessarily the full sheet).
	needed := neededRows(sp, opts)
	maxRowSeen := -1
	rows := map[int][]sheetsource.Cell{}
	if _, err := src.ReadSheet(sp.Sheet.Index, func(r int, cells []sheetsource.Cell) error {
		if r > maxRowSeen {
			maxRowSeen = r
		}
		if needed(r) {
			rows[r] = cells
		}
		return nil
	}); err != nil {
		return out, err
	}

	for i, rp := range sp.Regions {
		rr := RegionRender{RegionIndex: i, Kind: rp.Kind}
		if names != nil {
			rr.TableName = names[[2]int{sp.Sheet.Index, i}]
		}
		switch rp.Kind {
		case profile.KindTable:
			renderTable(&b, fileName, sp, i, rp, rows, &rr, opts, maxRowSeen)
		case profile.KindForm:
			renderForm(&b, rp, rows)
		case profile.KindProse:
			renderProse(&b, rp, rows)
		case profile.KindEmpty:
			// nothing
		}
		out.Regions = append(out.Regions, rr)
	}

	out.Page.Text = strings.TrimRight(b.String(), "\n")
	return out, nil
}

// neededRows returns a predicate that is true for any row this sheet's
// render needs to buffer: every row of a form/prose region, and the first
// EmbedMaxRows data rows of each table region (rows beyond the cap are
// counted, not stored — see renderTable).
func neededRows(sp profile.SheetProfile, opts Options) func(r int) bool {
	type span struct{ lo, hi int }
	var spans []span
	for _, rp := range sp.Regions {
		switch rp.Kind {
		case profile.KindForm, profile.KindProse:
			spans = append(spans, span{rp.Region.Top, rp.Region.Bottom})
		case profile.KindTable:
			hi := rp.DataStart + opts.EmbedMaxRows - 1
			spans = append(spans, span{rp.DataStart, hi})
		}
	}
	return func(r int) bool {
		for _, s := range spans {
			if r >= s.lo && r <= s.hi {
				return true
			}
		}
		return false
	}
}
