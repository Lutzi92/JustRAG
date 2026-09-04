package profile

import (
	"strconv"
	"strings"

	"github.com/justrag/go-backend/internal/sheetsource"
)

// boolTokens are the case-insensitive value tokens that make a column a
// Bool candidate (rule 3).
var boolTokens = map[string]bool{
	"ja": true, "nein": true, "yes": true, "no": true,
	"true": true, "false": true, "x": true, "✓": true,
	"wahr": true, "falsch": true,
}

// AssignRoles computes a ColumnProfile (role, stats, decimal style, unit,
// list values) for every kept column of hb, over the data rows
// hb.DataStart..reg.Bottom of the sample. Never panics: a column index
// outside the sample's row bounds is skipped defensively.
func AssignRoles(s *sheetsource.Sample, reg Region, hb headerBlock) []ColumnProfile {
	out := make([]ColumnProfile, 0, len(hb.Columns))
	for i, c := range hb.Columns {
		header := ""
		if i < len(hb.Headers) {
			header = hb.Headers[i]
		}
		cp := ColumnProfile{Index: c, Header: header}

		var texts []string
		distinct := map[string]int{}
		var totalLen int
		pctCount := 0
		unitSeen := ""

		for r := hb.DataStart; r <= reg.Bottom; r++ {
			if r < 0 || r >= len(s.Rows) || c < 0 || c >= len(s.Rows[r]) {
				continue
			}
			cell := s.Rows[r][c]
			if cell.IsEmpty() || IsNullToken(cell.Raw) {
				continue
			}
			cp.Stats.NonEmpty++
			key := strings.ToLower(strings.TrimSpace(cell.Raw))
			distinct[key]++
			totalLen += len(cell.Raw)
			if cell.Style.Unit != "" && unitSeen == "" {
				unitSeen = cell.Style.Unit
			}
			switch cell.Kind {
			case sheetsource.KindNumber:
				cp.Stats.Numeric++
				if cell.Style.Percent {
					pctCount++
				}
			case sheetsource.KindDate:
				cp.Stats.Dates++
			case sheetsource.KindBool:
				cp.Stats.Bools++
			default:
				cp.Stats.Texts++
				texts = append(texts, cell.Raw)
			}
			lz, ld, pat := LooksLikeIDValue(cell.Raw)
			if lz {
				cp.Stats.LeadingZero++
			}
			if ld {
				cp.Stats.LongDigits++
			}
			// idValueRe's digit-separator-digit shape also matches plain
			// decimal numbers ("2143.28") and dd.mm.yyyy dates
			// ("14.03.2025"); both are already unambiguously typed by the
			// sheet or ParseDateText, so a pattern hit there is not
			// evidence of an ID column.
			if pat && cell.Kind != sheetsource.KindNumber && cell.Kind != sheetsource.KindDate {
				if _, isDate := ParseDateText(cell.Raw); !isDate {
					cp.Stats.IDPattern++
				}
			}
		}

		cp.Stats.Distinct = len(distinct)
		if cp.Stats.NonEmpty > 0 {
			cp.Stats.AvgLen = float64(totalLen) / float64(cp.Stats.NonEmpty)
		}
		cp.DecimalComma = DetectDecimalComma(texts)
		cp.Unit = unitSeen
		if cp.Stats.Numeric > 0 && pctCount*2 >= cp.Stats.Numeric {
			cp.Unit = "%"
		}
		cp.ListValues = listValuesFor(s, c, hb.DataStart, reg.Bottom)
		cp.Role = decideRole(s, reg, hb, c, &cp, texts, distinct)
		out = append(out, cp)
	}
	return out
}

// decideRole applies the role rules (brief §"Role rules") in priority order:
// ID, Date, Bool, Measure, Category, else Text.
func decideRole(s *sheetsource.Sample, reg Region, hb headerBlock, c int, cp *ColumnProfile, texts []string, distinct map[string]int) Role {
	n := cp.Stats.NonEmpty
	if n == 0 {
		return RoleText
	}

	if idHeaderRe.MatchString(cp.Header) ||
		cp.Stats.LeadingZero > 0 ||
		cp.Stats.LongDigits > 0 ||
		float64(cp.Stats.IDPattern) >= 0.8*float64(n) ||
		fixedWidthCodes(s, reg, hb, c, distinct) ||
		isIndexColumn(s, reg, hb, c) {
		return RoleID
	}

	dates := cp.Stats.Dates
	for _, t := range texts {
		if _, ok := ParseDateText(t); ok {
			dates++
		}
	}
	if float64(dates) >= 0.9*float64(n) {
		return RoleDate
	}

	if n >= 2 && len(distinct) <= 2 {
		allBool := true
		for k := range distinct {
			if !boolTokens[k] {
				allBool = false
				break
			}
		}
		if allBool {
			return RoleBool
		}
	}

	nums := cp.Stats.Numeric
	for _, t := range texts {
		if _, ok := ParseNumber(t, cp.DecimalComma); ok {
			nums++
		}
	}
	if float64(nums) >= 0.9*float64(n) {
		return RoleMeasure
	}

	// Deviation from the brief's literal "distinct <= 20% of n": at small n
	// (test fixture n=6) 20% rounds down to a single distinct value, which
	// would reject the brief's own worked example (Denkmalschutz, 3 distinct
	// of 6) while still admitting nothing. 50% is the threshold that
	// classifies Denkmalschutz (3/6) as Category and Beschreibung (6/6) as
	// Text, matching the brief's stated expected roles; see task-11-report.md
	// for the stats and the DONE_WITH_CONCERNS note.
	if len(cp.ListValues) > 0 || (n >= 5 && len(distinct) <= 50 && float64(len(distinct)) <= 0.5*float64(n)) {
		return RoleCategory
	}

	return RoleText
}

// fixedWidthCodes reports whether every non-null data value in column c is
// an all-digit string of one identical length >= 4, with >= 90% of them
// distinct (rule 1, fixed-width codes).
func fixedWidthCodes(s *sheetsource.Sample, reg Region, hb headerBlock, c int, distinct map[string]int) bool {
	width := -1
	n := 0
	for r := hb.DataStart; r <= reg.Bottom; r++ {
		if r < 0 || r >= len(s.Rows) || c < 0 || c >= len(s.Rows[r]) {
			continue
		}
		cell := s.Rows[r][c]
		if cell.IsEmpty() || IsNullToken(cell.Raw) {
			continue
		}
		v := strings.TrimSpace(cell.Raw)
		if !isAllDigits(v) {
			return false
		}
		if width < 0 {
			width = len(v)
		} else if len(v) != width {
			return false
		}
		n++
	}
	if n == 0 || width < 4 {
		return false
	}
	return float64(len(distinct)) >= 0.9*float64(n)
}

// isIndexColumn reports whether column c's non-null data values are exactly
// the integers 1..n in row order (preflight ruling R6: an index column).
func isIndexColumn(s *sheetsource.Sample, reg Region, hb headerBlock, c int) bool {
	want := 1
	n := 0
	for r := hb.DataStart; r <= reg.Bottom; r++ {
		if r < 0 || r >= len(s.Rows) || c < 0 || c >= len(s.Rows[r]) {
			continue
		}
		cell := s.Rows[r][c]
		if cell.IsEmpty() || IsNullToken(cell.Raw) {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(cell.Raw))
		if err != nil || v != want {
			return false
		}
		want++
		n++
	}
	return n >= 2
}

// listValuesFor returns the resolved list-validation values covering column
// c over data rows [from, to], or nil when no validation's Sqref intersects
// it.
func listValuesFor(s *sheetsource.Sample, c, from, to int) []string {
	for _, v := range s.Extras.Validations {
		if len(v.Values) == 0 {
			continue
		}
		for _, r := range v.Sqref {
			if c >= r.FromCol && c <= r.ToCol && r.ToRow >= from && r.FromRow <= to {
				return v.Values
			}
		}
	}
	return nil
}
