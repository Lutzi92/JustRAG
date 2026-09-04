package profile

import "github.com/justrag/go-backend/internal/sheetsource"

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
	return sp
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
