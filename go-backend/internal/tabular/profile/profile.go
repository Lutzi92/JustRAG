package profile

import (
	"sort"
	"strings"

	"github.com/justrag/go-backend/internal/sheetsource"
)

// ProfileSheet runs the full detection pipeline over a sample: region
// detection, header-block detection, kind classification, and (for table
// regions) role assignment and derived-row detection.
func ProfileSheet(s *sheetsource.Sample, opts Options) SheetProfile {
	sp := SheetProfile{Sheet: s.Info, Kind: KindEmpty}
	if len(s.Rows) == 0 || s.Width == 0 {
		return sp
	}
	regions := DetectRegions(s, func(r int, reg Region) float64 { return headerScore(s, r, reg) })
	formulaEmpty := 0
	for _, row := range s.Rows {
		for _, c := range row {
			if c.IsFormula && c.Kind == sheetsource.KindEmpty {
				formulaEmpty++
			}
		}
	}
	for _, reg := range regions {
		hb, ok := detectHeaderBlock(s, reg, opts.MaxHeaderRows)
		rp := RegionProfile{Region: reg, IndexRow: -1, Diagnostics: map[string]any{
			"modal_width":         modalWidth(s, reg),
			"scanned_rows":        reg.Bottom - reg.Top + 1,
			"formula_cells_empty": formulaEmpty,
			"hidden_sheet":        s.Info.Hidden,
		}}
		rp.Kind = classifyKind(s, reg, &hb, ok)
		if rp.Kind == KindTable {
			rp.HeaderRows, rp.DataStart, rp.IndexRow = hb.Rows, hb.DataStart, hb.IndexRow
			rp.ProseAbove, rp.Confidence = hb.ProseAbove, hb.Confidence
			rp.Columns = AssignRoles(s, reg, hb)
			for c := reg.Left; c <= reg.Right; c++ {
				if !containsInt(hb.Columns, c) {
					rp.Dropped = append(rp.Dropped, c)
				}
			}
			for r := hb.DataStart; r <= reg.Bottom; r++ {
				if IsDerivedRow(s.Rows[r], hb.Columns, reg.Bottom-hb.DataStart+1) {
					rp.DerivedRows = append(rp.DerivedRows, r)
				}
			}
			rp.Diagnostics["header_score"] = hb.Confidence
			rp.Diagnostics["dropped_columns"] = len(rp.Dropped)
			rp.Diagnostics["derived_rows"] = len(rp.DerivedRows)
		}
		rp.Diagnostics["regions"] = len(regions)
		sp.Regions = append(sp.Regions, rp)
		sp.Kind = strongerKind(sp.Kind, rp.Kind)
	}
	foldAboveTableRegions(s, &sp)
	return sp
}

// foldAboveTableRegions attaches the title / metadata blocks that sit above a
// table to that table's ProseAbove and removes them from Regions.
//
// A blank row between a sheet's title (or its "Stand:" / contact block) and
// the table below makes region detection produce separate regions, so
// headerBlock.ProseAbove — which only ever sees rows *inside* one region —
// could never reach them. A region folds into table region T when it lies
// entirely above T, overlaps T's column span, has no other table region
// between it and T by rows, and is not itself a table of more than two
// columns (a two-column label/value block such as `Stand: | 03-14-25` passes
// the table gate on fill alone and must not be mistaken for the sheet's data).
//
// Folded lines are prepended to T.ProseAbove in row order, ahead of any prose
// the header block found inside T itself, and counted in
// Diagnostics["folded_regions"].
func foldAboveTableRegions(s *sheetsource.Sample, sp *SheetProfile) {
	folded := make([]bool, len(sp.Regions))
	// Bottom-up: a two-column metadata block is both a fold target (it passes
	// the table gate) and a fold candidate. Reaching the real table first
	// claims that block as a candidate instead of letting it swallow the title
	// above it.
	for ti := len(sp.Regions) - 1; ti >= 0; ti-- {
		t := &sp.Regions[ti]
		// Only a real multi-column table has a sheet header to fold into.
		// Stacked one-column lists (a dropdown sheet) are peers, not a table
		// with a title above it.
		if folded[ti] || t.Kind != KindTable || len(t.Columns) < 2 {
			continue
		}
		var cands []int
		for ri := range sp.Regions {
			if ri == ti || folded[ri] {
				continue
			}
			r := sp.Regions[ri]
			if r.Region.Bottom >= t.Region.Top {
				continue
			}
			if r.Region.Left > t.Region.Right || r.Region.Right < t.Region.Left {
				continue
			}
			if r.Kind == KindTable && len(r.Columns) > 2 {
				continue
			}
			cands = append(cands, ri)
		}
		// A table region between a candidate and T separates them — but only
		// one that is not itself being folded into T on this pass, or the
		// sheet's `Stand:` block would shield the title above it.
		cands = dropBlockedCandidates(sp.Regions, cands, ti)
		sort.Slice(cands, func(a, b int) bool {
			ra, rb := sp.Regions[cands[a]].Region, sp.Regions[cands[b]].Region
			if ra.Top != rb.Top {
				return ra.Top < rb.Top
			}
			return ra.Left < rb.Left
		})
		var lines []string
		for _, ri := range cands {
			folded[ri] = true
			// A folded region may carry prose of its own (a totals row above
			// its header, say); keep it ahead of the region's own rows.
			lines = append(lines, sp.Regions[ri].ProseAbove...)
			lines = append(lines, regionLines(s, sp.Regions[ri].Region)...)
		}
		t.ProseAbove = append(lines, t.ProseAbove...)
		if t.Diagnostics == nil {
			t.Diagnostics = map[string]any{}
		}
		t.Diagnostics["folded_regions"] = len(cands)
	}
	kept := sp.Regions[:0]
	for i, rp := range sp.Regions {
		if !folded[i] {
			kept = append(kept, rp)
		}
	}
	sp.Regions = kept
}

// dropBlockedCandidates removes from cands every region that has a table
// region strictly between it and regions[ti] by rows. Regions in cands do not
// block each other: they are all being folded into the same table, so the
// two-column `Stand:` block must not shield the title row above it.
func dropBlockedCandidates(regions []RegionProfile, cands []int, ti int) []int {
	inCands := make(map[int]bool, len(cands))
	for _, ri := range cands {
		inCands[ri] = true
	}
	out := cands[:0]
	for _, ri := range cands {
		above, below := regions[ri].Region, regions[ti].Region
		blocked := false
		for xi := range regions {
			if xi == ri || xi == ti || inCands[xi] || regions[xi].Kind != KindTable {
				continue
			}
			if x := regions[xi].Region; x.Top > above.Bottom && x.Bottom < below.Top {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, ri)
		}
	}
	return out
}

// regionLines renders a region as one string per non-empty row, each row's
// filled cells joined with a single space.
func regionLines(s *sheetsource.Sample, reg Region) []string {
	var out []string
	for r := reg.Top; r <= reg.Bottom && r < len(s.Rows); r++ {
		var parts []string
		for c := reg.Left; c <= reg.Right && c < len(s.Rows[r]); c++ {
			if cell := s.Rows[r][c]; !cell.IsEmpty() {
				parts = append(parts, strings.TrimSpace(cell.Formatted))
			}
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, " "))
		}
	}
	return out
}

// strongerKind ranks table > form > prose > empty and returns the stronger
// of a and b.
func strongerKind(a, b SheetKind) SheetKind {
	rank := map[SheetKind]int{KindEmpty: 0, KindProse: 1, KindForm: 2, KindTable: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
