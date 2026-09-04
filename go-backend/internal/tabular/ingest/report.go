package ingest

import (
	"strings"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/profile"
	"github.com/justrag/go-backend/internal/tabular/render"
)

// cardStatsFromResult maps a materialised region's ColumnStat entries to the
// renderer's profile-card shape. rp is the (possibly LLM-assisted) region
// profile that produced the materialised region: the real Materializer
// already copies a column's profiled Description into its ColumnStat (see
// ColumnAccumulator.Stat), so this is normally a no-op merge — but a test
// double for RegionMaterializer that builds ColumnStat by hand may leave
// Description empty, so it is filled in here by header when the stat
// itself doesn't carry one, keeping the LLM-authored description visible
// in the rendered card regardless of how faithfully a given materialiser
// stub reproduces the real one.
func cardStatsFromResult(rr *tabular.RegionResult, rp profile.RegionProfile) []render.ColumnCardStat {
	descByHeader := make(map[string]string, len(rp.Columns))
	for _, c := range rp.Columns {
		if c.Description != "" {
			descByHeader[strings.Join(strings.Fields(c.Header), " ")] = c.Description
		}
	}

	out := make([]render.ColumnCardStat, 0, len(rr.Stats))
	for _, s := range rr.Stats {
		top := s.ValueSet
		if len(top) > 5 {
			top = top[:5]
		}
		var nullRate float64
		if rr.RowsMaterialised > 0 {
			nullRate = float64(s.NullCount) / float64(rr.RowsMaterialised)
		}
		desc := s.Description
		if desc == "" {
			desc = descByHeader[strings.Join(strings.Fields(s.Original), " ")]
		}
		out = append(out, render.ColumnCardStat{
			Header:      s.Original,
			SQLName:     s.Name,
			Type:        s.Type,
			Role:        s.Role,
			Description: desc,
			Min:         s.Min,
			Max:         s.Max,
			Top:         top,
			NullRate:    nullRate,
		})
	}
	return out
}

// SanitizeNote truncates s to 200 runes and strips newlines, for report
// Notes entries built from arbitrary error text.
func SanitizeNote(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if utf8.RuneCountInString(s) <= 200 {
		return s
	}
	r := []rune(s)
	return string(r[:200])
}
