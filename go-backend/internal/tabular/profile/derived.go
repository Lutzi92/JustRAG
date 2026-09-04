package profile

import (
	"regexp"
	"strings"

	"github.com/justrag/go-backend/internal/sheetsource"
)

var (
	totalsLabelRe = regexp.MustCompile(`(?i)^(summe|gesamt|total|zwischensumme|insgesamt|mittelwert|durchschnitt|gesamtsumme)\b`)
	aggFormulaRe  = regexp.MustCompile(`(?i)\b(SUM|AVERAGE|COUNT|COUNTA|SUBTOTAL|MIN|MAX)\(([^)]*)\)`)
	rangeRe       = regexp.MustCompile(`\$?[A-Za-z]{1,3}\$?([0-9]+):\$?[A-Za-z]{1,3}\$?([0-9]+)`)
)

// formulaSpansRows returns the largest row span among A1 ranges found inside
// aggregate function calls (SUM/AVERAGE/COUNT/COUNTA/SUBTOTAL/MIN/MAX) in
// formula, or 0 when no such range is found.
func formulaSpansRows(formula string) int {
	best := 0
	for _, m := range aggFormulaRe.FindAllStringSubmatch(formula, -1) {
		for _, r := range rangeRe.FindAllStringSubmatch(m[2], -1) {
			a, b := atoi(r[1]), atoi(r[2])
			if span := abs(a-b) + 1; span > best {
				best = span
			}
		}
	}
	return best
}

// IsDerivedRow reports whether row cells reads as a derived (totals) row
// rather than a data row, considering only kept columns cols. regionRows is
// the number of data rows known so far (sample) or total (pass 1). Exported
// because Phase 2's streaming pass applies it per row.
//
// Two independent rules: (a) the first non-empty kept cell is a totals label
// ("Summe", "Gesamt", …), or (b) some kept cell holds an aggregate formula
// spanning at least half the region. Rule (a) used to `return` its match
// result, so a labelled row that failed the regex short-circuited the whole
// function and rule (b) only ever ran for rows starting with a number —
// "Jahressumme | =SUM(C4:C400)" was classified as data.
func IsDerivedRow(cells []sheetsource.Cell, cols []int, regionRows int) bool {
	for _, c := range cols {
		if c >= len(cells) || cells[c].IsEmpty() {
			continue
		}
		if cells[c].Kind == sheetsource.KindText && totalsLabelRe.MatchString(strings.TrimSpace(cells[c].Raw)) {
			return true
		}
		break
	}
	if regionRows < 4 {
		return false
	}
	for _, c := range cols {
		if c < len(cells) && cells[c].IsFormula && float64(formulaSpansRows(cells[c].Formula)) >= 0.5*float64(regionRows) {
			return true
		}
	}
	return false
}

// atoi parses the all-digit row numbers rangeRe captures. Anything longer than
// 9 digits is not a spreadsheet row number, and accumulating it would overflow
// into a meaningless (possibly negative) span, so it reads as 0.
func atoi(s string) int {
	if len(s) > 9 {
		return 0
	}
	n := 0
	for _, ch := range s {
		n = n*10 + int(ch-'0')
	}
	return n
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
