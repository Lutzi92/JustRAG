package render

import (
	"strings"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

// renderForm writes one form region's rows as "Label: Value" lines. The
// label is the row's first non-empty text cell (using Formatted); the value
// is the next non-empty cell to its right. Rows without a pair (a lone
// label, or no cells at all) are skipped.
func renderForm(b *strings.Builder, rp profile.RegionProfile, rows map[int][]sheetsource.Cell) {
	for r := rp.Region.Top; r <= rp.Region.Bottom; r++ {
		cells, ok := rows[r]
		if !ok {
			continue
		}
		label, value := "", ""
		haveLabel := false
		for _, c := range cells {
			if c.IsEmpty() {
				continue
			}
			if !haveLabel {
				label = cellValue(c.Formatted)
				haveLabel = true
				continue
			}
			value = cellValue(c.Formatted)
			break
		}
		if !haveLabel || value == "" {
			continue
		}
		b.WriteString(label + ": " + value + "\n")
	}
	b.WriteString("\n")
}

// renderProse writes one prose region's rows as plain text lines: the row's
// non-empty Formatted cells joined by a space, one line per row.
func renderProse(b *strings.Builder, rp profile.RegionProfile, rows map[int][]sheetsource.Cell) {
	for r := rp.Region.Top; r <= rp.Region.Bottom; r++ {
		cells, ok := rows[r]
		if !ok {
			continue
		}
		var parts []string
		for _, c := range cells {
			if c.IsEmpty() {
				continue
			}
			parts = append(parts, cellValue(c.Formatted))
		}
		if len(parts) == 0 {
			continue
		}
		b.WriteString(strings.Join(parts, " "))
		b.WriteString("\n")
	}
	b.WriteString("\n")
}
