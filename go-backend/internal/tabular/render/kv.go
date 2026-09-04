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

func allEmpty(cells []sheetsource.Cell, cols []int) bool {
	for _, c := range cols {
		if c >= 0 && c < len(cells) && !cells[c].IsEmpty() {
			return false
		}
	}
	return true
}

func markerPrefix(table string) string {
	if table == "" {
		return "[rows"
	}
	return "[tabular." + table + " rows"
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
	regionRows := last - rp.DataStart + 1
	if regionRows < 0 {
		regionRows = 0
	}

	budget := int(0.8 * float64(opts.ChunkSize))

	var rowsBuf strings.Builder
	var block strings.Builder
	blockStart, ordinal := 0, 0
	flush := func(lastOrdinal int) {
		if block.Len() == 0 {
			return
		}
		fmt.Fprintf(&rowsBuf, "%s %d–%d]\n%s\n", markerPrefix(rr.TableName), blockStart, lastOrdinal, block.String())
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
		if allEmpty(cells, cols) {
			continue
		}
		if derived[r] || profile.IsDerivedRow(cells, cols, regionRows) {
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
		card += fmt.Sprintf("Zeilen %d–%d sind nur über table_query erreichbar.\n", opts.EmbedMaxRows+1, rowCount)
	}
	b.WriteString(card)
	b.WriteString("\n")

	b.WriteString(rowsBuf.String())
}
