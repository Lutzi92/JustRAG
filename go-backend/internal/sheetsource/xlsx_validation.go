package sheetsource

import "strings"

const (
	maxListValues = 1000
	maxListCells  = 10_000
	// maxListValidations bounds how many range-ref list validations
	// resolveValidations resolves per sheet: each resolution re-streams the
	// referenced sheet, so the cap bounds O(N x sheet) re-reads. Validations
	// beyond the cap keep Values == nil.
	maxListValidations = 50
)

// parseListRef splits a formula1 into (sheet, range, inline). Inline lists are
// quoted comma-separated literals.
func parseListRef(ref string) (sheet, rng string, inline bool) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, `"`) {
		return "", "", true
	}
	if i := strings.LastIndex(ref, "!"); i >= 0 {
		sheet = strings.Trim(ref[:i], "'")
		rng = ref[i+1:]
		return sheet, rng, false
	}
	return "", ref, false
}

func splitInlineList(ref string) []string {
	ref = strings.Trim(strings.TrimSpace(ref), `"`)
	var out []string
	for _, p := range strings.Split(ref, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *XLSXSource) resolveValidations(ex *SheetExtras, sheetIdx int) {
	resolved := 0
	for i := range ex.Validations {
		// Cap resolutions per sheet: leave the rest Values == nil rather
		// than re-streaming without bound.
		if resolved >= maxListValidations {
			continue
		}
		v := &ex.Validations[i]
		sheet, rng, inline := parseListRef(v.Ref)
		if inline {
			v.Values = splitInlineList(v.Ref)
			resolved++
			continue
		}
		// Check defined names first when no sheet is specified
		if sheet == "" && rng != "" {
			if def, ok := s.wb.definedNames[rng]; ok {
				sheet, rng, _ = parseListRef(def)
			}
		}
		r, err := ParseRange(rng)
		if err != nil {
			continue
		}
		if (r.ToRow-r.FromRow+1)*(r.ToCol-r.FromCol+1) > maxListCells {
			continue
		}
		target := sheetIdx
		if sheet != "" {
			target = -1
			for j, sh := range s.wb.sheets {
				if sh.Name == sheet {
					target = j
					break // stop on first match
				}
			}
			if target < 0 {
				continue
			}
		}
		v.Values = s.readRangeValues(target, r)
		resolved++
	}
}

func (s *XLSXSource) readRangeValues(sheetIdx int, r Range) []string {
	var out []string
	_, _, _ = s.readSheetRaw(sheetIdx, func(rowIdx int, cells []Cell) error {
		if rowIdx > r.ToRow || len(out) >= maxListValues {
			return ErrStop
		}
		if rowIdx < r.FromRow {
			return nil
		}
		for c := r.FromCol; c <= r.ToCol && c < len(cells); c++ {
			if !cells[c].IsEmpty() {
				out = append(out, cells[c].Raw)
				if len(out) >= maxListValues {
					return ErrStop
				}
			}
		}
		return nil
	})
	return out
}
