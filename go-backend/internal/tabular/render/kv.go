package render

import (
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/splitter"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

// headerKey is the record key for a column: its joined header, plus a unit
// suffix when the column carries one the header doesn't already spell out.
func headerKey(c profile.ColumnProfile) string {
	h := strings.Join(strings.Fields(c.Header), " ")
	switch {
	case c.Unit == "%" && !strings.Contains(h, "%"):
		return h + " (%)"
	case c.Unit != "" && c.Unit != "%" && !strings.Contains(h, c.Unit):
		return h + " (" + c.Unit + ")"
	}
	return h
}

// cellValue normalises a raw cell value for record/prose rendering: no
// embedded newlines (they would break the record's own line), and no "|"
// (it is the record's field separator).
func cellValue(raw string) string {
	v := strings.ReplaceAll(raw, "\n", " ")
	v = strings.ReplaceAll(v, "|", "/")
	return strings.TrimSpace(v)
}

// record renders one data row as a "Header: value | Header: value" line over
// the region's kept columns, from Raw values, empty cells omitted.
func record(rp profile.RegionProfile, cells []sheetsource.Cell) string {
	parts := make([]string, 0, len(rp.Columns))
	for _, c := range rp.Columns {
		if c.Index >= len(cells) || cells[c.Index].IsEmpty() {
			continue
		}
		parts = append(parts, headerKey(c)+": "+cellValue(cells[c.Index].Raw))
	}
	return strings.Join(parts, " | ")
}

// proseLine renders a derived (totals) row as a prose line: the row's
// non-empty Formatted cells over the kept columns, first cell as the label
// followed by ": ", the rest joined by " | ".
func proseLine(cells []sheetsource.Cell, cols []int) string {
	var vals []string
	for _, c := range cols {
		if c < 0 || c >= len(cells) || cells[c].IsEmpty() {
			continue
		}
		vals = append(vals, cellValue(cells[c].Formatted))
	}
	switch len(vals) {
	case 0:
		return ""
	case 1:
		return vals[0]
	default:
		return vals[0] + ": " + strings.Join(vals[1:], " | ")
	}
}

// MarkerPrefix is the opening of a row block's marker line. A full block
// marker reads "[tabular.<table> rows <a>–<b>]" (en dash between the
// bounds), where a and b are _rowid values in the materialised table; when
// the region was not materialised the table part is omitted. Exported
// because the table_query tool description in internal/mcp/builtin teaches
// a model to parse this exact shape, and its test pins the two together.
func MarkerPrefix(table string) string {
	if table == "" {
		return "[rows"
	}
	return "[tabular." + table + " rows"
}

// blockHeading is the markdown heading repeated at the start of every row
// block (spec §4.5). internal/processor records a chunk's enclosing heading
// in chunk METADATA only (SectionsForChunk fills meta sections); it never
// prepends it to the embedded chunk text, so a block that lands in its own
// chunk would otherwise carry no file/sheet provenance in what is actually
// embedded and quoted back to the answer model.
func blockHeading(fileName string, sp profile.SheetProfile, regionIdx int) string {
	h := "### " + fileName + " › " + sp.Sheet.Name
	if sp.Sheet.Hidden {
		h += " (hidden)"
	}
	if len(sp.Regions) > 1 {
		h += fmt.Sprintf(" › Bereich %d", regionIdx+1)
	}
	return h
}

// lastRow is the highest absolute row index this table region's data can
// reach: the region's own bottom when it is not open-ended (an exact bound
// from profiling), or the highest row this render pass actually saw in the
// sheet when it is (the sample that produced the profile may have been
// truncated before the sheet's real end).
func lastRow(rp profile.RegionProfile, maxRowSeen int) int {
	if !rp.Region.OpenEnded {
		return rp.Region.Bottom
	}
	if maxRowSeen > rp.Region.Bottom {
		return maxRowSeen
	}
	return rp.Region.Bottom
}

// renderTable writes one table region: its folded-in prose lines, the
// always-present profile card, then the row blocks. The card is written
// after the rows are rendered into a temporary builder so RowsPastCap (needed
// for the cap sentence) is known first.
func renderTable(b *strings.Builder, fileName string, sp profile.SheetProfile, regionIdx int, rp profile.RegionProfile, rows map[int][]sheetsource.Cell, rr *RegionRender, opts Options, maxRowSeen int) {
	cols := make([]int, len(rp.Columns))
	for i, c := range rp.Columns {
		cols[i] = c.Index
	}
	derived := map[int]bool{}
	for _, r := range rp.DerivedRows {
		derived[r] = true
	}

	last := lastRow(rp, maxRowSeen)
	// R22: the SAME regionRows the materialiser passes to ClassifyRow for
	// this region. It is deliberately NOT derived from `last` (which folds
	// in maxRowSeen, a quantity the materialiser's single streaming pass
	// cannot know), because a different regionRows makes IsDerivedRow's
	// "formula spans half the region" rule fire on one side only — and the
	// block markers below would then address different rows than _rowid.
	regionRows := profile.RegionRows(rp.Region, rp.DataStart, sp.Sheet.RowCount)

	// The budget bounds the RECORDS in a block, but the block that reaches
	// the chunker is heading + marker + records, so both fixed lines come
	// off the chunk budget first. (The marker line was already unaccounted
	// for before I7 added the heading; a block could therefore overshoot
	// the chunk by its own marker.) The marker's own width is bounded with
	// a six-digit stand-in for each ordinal rather than the real numbers,
	// which are not known until the block is flushed.
	heading := blockHeading(fileName, sp, regionIdx)
	overhead := splitter.CountTokens(heading) + splitter.CountTokens(MarkerPrefix(rr.TableName)+" 000000–000000]")
	budget := int(0.8*float64(opts.ChunkSize)) - overhead
	if budget < 1 {
		budget = 1
	}

	var rowsBuf strings.Builder
	var block strings.Builder
	blockStart, ordinal := 0, 0
	flush := func(lastOrdinal int) {
		if block.Len() == 0 {
			return
		}
		fmt.Fprintf(&rowsBuf, "%s\n%s %d–%d]\n%s\n", heading, MarkerPrefix(rr.TableName), blockStart, lastOrdinal, block.String())
		block.Reset()
		rr.Blocks++
	}

	for r := rp.DataStart; r <= last; r++ {
		cells, ok := rows[r]
		if !ok {
			// Beyond the per-region embed cap: counted, never rendered.
			if r >= rp.DataStart+opts.EmbedMaxRows {
				rr.RowsPastCap++
			}
			continue
		}
		rowEmpty, rowDerived := profile.ClassifyRow(cells, cols, regionRows)
		if rowEmpty {
			continue
		}
		if derived[r] || rowDerived {
			flush(ordinal)
			if line := proseLine(cells, cols); line != "" {
				rowsBuf.WriteString(line)
				rowsBuf.WriteString("\n\n")
			}
			continue
		}
		rec := record(rp, cells)
		if block.Len() > 0 && splitter.CountTokens(block.String()+rec) > budget {
			flush(ordinal)
		}
		if block.Len() == 0 {
			blockStart = ordinal + 1
		}
		ordinal++
		block.WriteString(rec + "\n")
		rr.RowsEmbedded++
	}
	flush(ordinal)

	for _, p := range rp.ProseAbove {
		b.WriteString(p)
		b.WriteString("\n")
	}
	if len(rp.ProseAbove) > 0 {
		b.WriteString("\n")
	}

	key := [2]int{sp.Sheet.Index, regionIdx}
	stats, ok := opts.CardStats[key]
	rowCount := opts.RowCounts[key]
	if !ok {
		stats = columnCardStatsFromProfile(rp.Columns)
		rowCount = rr.RowsEmbedded + rr.RowsPastCap
	}
	card := ProfileCard(fileName, sp, rp, rr.TableName, rowCount, stats)
	if rr.RowsPastCap > 0 {
		// M1: "reachable via table_query" is only true when the region
		// actually has a table. Without one (render-only ingest, or a
		// per-region materialisation failure) the rows past the cap are
		// simply not in the index at all, and pointing the model at a tool
		// that cannot see them invites a fabricated answer.
		if rr.TableName != "" {
			card += fmt.Sprintf("Zeilen %d–%d sind nur über table_query erreichbar.\n", opts.EmbedMaxRows+1, rowCount)
		} else {
			card += fmt.Sprintf("Zeilen %d–%d sind nicht eingebettet und hier nicht abrufbar.\n", opts.EmbedMaxRows+1, rowCount)
		}
	}
	b.WriteString(card)
	b.WriteString("\n")

	b.WriteString(rowsBuf.String())
}
