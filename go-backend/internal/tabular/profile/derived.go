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

// FallbackRegionRows is the region size assumed for an OPEN-ENDED table
// region — one whose true bottom is not known until the sheet has been read
// to the end, AND whose sheet carries no usable row count either (an
// xlsx's <dimension> was absent/stale, or the file is .xls/.ods/.csv, none
// of which populate sheetsource.SheetInfo.RowCount). Ruling R22: the
// materialiser (streaming, one pass) and the renderer (buffered, but
// bounded by tabular_embed_max_rows) must feed IsDerivedRow the SAME
// regionRows for the same region, or an aggregate formula spanning half the
// region is a totals row on one side and a data row on the other — which
// desynchronises the renderer's block markers from the materialised
// _rowid values. When the sheet's row count IS known up front (an xlsx's
// <dimension ref> — see sheetsource.SheetInfo.RowCount), both sides derive
// the same regionRows from it instead of falling back to this constant;
// this fallback is what they use when it isn't. The value only scales rule
// (b)'s "spans at least half the region" threshold; it is deliberately
// large enough that a formula over a handful of rows in a long sheet is
// not mistaken for a grand total.
const FallbackRegionRows = 1000

// RegionRows is the row count both the materialiser and the renderer must
// pass to ClassifyRow/IsDerivedRow for a region: the region's exact height
// when profiling bounded it; when it is open-ended, the sheet's declared
// row count minus the rows before dataStart (when sheetRows is known AND
// at least dataStart), else the shared fallback. dataStart is the region's
// first data row (RegionProfile.DataStart); sheetRows is the sheet's
// sheetsource.SheetInfo.RowCount (0 = unknown).
//
// R58: a stale/placeholder xlsx dimension (see sheetRowCount's doc comment
// in sheetsource) can declare a row count BELOW the region's own data
// start -- that is not a genuinely tiny sheet, it is an unrecalculated ref
// (real-world example: a raw-Go-writer xlsx with every sheet stuck at
// "A1"). Trusting it verbatim would drive regionRows to ~1, which in turn
// drives IsDerivedRow's "formula spans at least half the region" threshold
// to ~0 -- misclassifying ordinary data rows as totals. So sheetRows is
// only treated as known when it is at least dataStart; anything smaller
// falls back to the shared constant exactly as if RowCount were 0.
func RegionRows(r Region, dataStart int, sheetRows int) int {
	if r.OpenEnded {
		if sheetRows > 0 && sheetRows >= dataStart {
			if n := sheetRows - dataStart + 1; n > 0 {
				return n
			}
			return 1
		}
		return FallbackRegionRows
	}
	if n := r.Bottom - dataStart + 1; n > 0 {
		return n
	}
	return 0
}

// ClassifyRow is the single row classifier shared by the materialiser
// (internal/tabular.MaterializeRegion) and the renderer
// (internal/tabular/render.renderTable), so a row is counted the same way on
// both sides and the rendered "[tabular.<table> rows a-b]" markers address
// the same rows as the table's _rowid column (ruling R22).
//
//   - empty: every kept cell is either an empty cell or a null token
//     ("-", "n/a", ...). These rows produce no table row (they would be
//     all-NULL) and no rendered record, so they must not consume an
//     ordinal on either side. The materialiser previously decided this
//     from ColumnAccumulator.Canonical (null tokens included) while the
//     renderer used a bare Cell.IsEmpty check, so a row of "-" was skipped
//     by one and numbered by the other.
//   - derived: IsDerivedRow -- a totals/subtotal row, rendered as prose and
//     never materialised.
//
// Emptiness is checked first on both sides: an all-null-token row carries no
// data to total up, so classifying it as derived would be meaningless.
func ClassifyRow(cells []sheetsource.Cell, kept []int, regionRows int) (empty, derived bool) {
	empty = true
	for _, i := range kept {
		if i < 0 || i >= len(cells) {
			continue
		}
		c := cells[i]
		if c.IsEmpty() {
			continue
		}
		// Mirrors ColumnAccumulator.Canonical: only a TEXT cell can carry a
		// null token; a numeric or date cell's raw value never matches one.
		if c.Kind == sheetsource.KindText && IsNullToken(strings.TrimSpace(c.Raw)) {
			continue
		}
		empty = false
		break
	}
	if empty {
		return true, false
	}
	return false, IsDerivedRow(cells, kept, regionRows)
}
