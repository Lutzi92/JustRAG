package render

import (
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

// ColumnCardStat is one column's line in a table region's profile card (the
// TableRAG schema object). SQLName/Type/Min/Max are set only by the
// materialiser path; the parser-only fallback leaves them "".
type ColumnCardStat struct {
	Header, SQLName, Type, Role, Description string
	Min, Max                                 string   // "" when n/a
	Top                                      []string // ≤ 5 values, "" when n/a
	NullRate                                 float64
}

// columnCardStatsFromProfile builds the fallback card stats directly from a
// table region's ColumnProfiles (header, role, description; Top = the
// column's first 5 ListValues) when the caller supplied no materialiser
// stats for the region.
func columnCardStatsFromProfile(cols []profile.ColumnProfile) []ColumnCardStat {
	out := make([]ColumnCardStat, 0, len(cols))
	for _, c := range cols {
		top := c.ListValues
		if len(top) > 5 {
			top = top[:5]
		}
		out = append(out, ColumnCardStat{
			Header:      strings.Join(strings.Fields(c.Header), " "),
			Role:        string(c.Role),
			Description: c.Description,
			Top:         top,
		})
	}
	return out
}

// ProfileCard renders the column-profile card for one table region (the
// TableRAG schema object) — one block, always emitted before the rows.
func ProfileCard(fileName string, sp profile.SheetProfile, rp profile.RegionProfile, tableName string, rowCount int, stats []ColumnCardStat) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tabelle %q in Datei %q", sp.Sheet.Name, fileName)
	if rp.Region.Left > 0 || rp.Region.Top > 0 {
		fmt.Fprintf(&b, " (Bereich ab %s%d)", sheetsource.ColumnName(rp.Region.Left), rp.Region.Top+1)
	}
	fmt.Fprintf(&b, ", %d Zeilen", rowCount)
	if tableName != "" {
		fmt.Fprintf(&b, ", abfragbar über table_query als tabular.%s", tableName)
	}
	b.WriteString(". Spalten:\n")
	for _, s := range stats {
		fmt.Fprintf(&b, "- %s", s.Header)
		if s.SQLName != "" {
			fmt.Fprintf(&b, " [%s %s]", s.SQLName, s.Type)
		}
		fmt.Fprintf(&b, " (%s)", s.Role)
		if s.Description != "" {
			b.WriteString(": " + s.Description)
		}
		switch {
		case s.Min != "" || s.Max != "":
			fmt.Fprintf(&b, "; Bereich %s – %s", s.Min, s.Max)
		case len(s.Top) > 0:
			fmt.Fprintf(&b, "; Werte z. B. %s", strings.Join(s.Top, ", "))
		}
		if s.NullRate > 0 {
			fmt.Fprintf(&b, "; %.0f%% leer", s.NullRate*100)
		}
		b.WriteString("\n")
	}
	return b.String()
}
