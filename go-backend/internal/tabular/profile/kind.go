package profile

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/sheetsource"
)

const (
	tableMinDataRows = 3
	// tableMinFill is the mean fill ratio a region's *participating* kept
	// columns must reach over the data rows. It is measured only over columns
	// with at least spacerFillRatio data fill: wide real-world exports carry
	// blocks of optional columns that are empty throughout (the JLU CAFM room
	// register fills 17 of 31), and letting those into the denominator vetoed
	// tables that are otherwise completely dense.
	tableMinFill  = 0.5
	formPairRatio = 0.4
	formMinMerges = 5

	formHeaderContrast  = 0.15
	singleColMaxRunes   = 40
	singleColShortRatio = 0.8
)

// formPairMaxRowFill is the most filled cells a row may have and still be
// read as a label/value pair. A true pair is two cells; allowing a third made
// every three-column table's rows count as pairs, which pushed narrow tables
// (and every all-text CSV) over formPairRatio.
const formPairMaxRowFill = 2

// labelValuePairRatio is, over filled cells in the region, the fraction that
// are text cells whose right neighbour (skipping covered merged cells) is
// filled and that sit in a row of at most formPairMaxRowFill filled cells.
func labelValuePairRatio(s *sheetsource.Sample, reg Region) float64 {
	filled, pairs := 0, 0
	for r := reg.Top; r <= reg.Bottom; r++ {
		rowFilled := filledCount(s, r, reg)
		for c := reg.Left; c <= reg.Right; c++ {
			cell := s.Rows[r][c]
			if cell.IsEmpty() {
				continue
			}
			filled++
			if cell.Kind != sheetsource.KindText || rowFilled > formPairMaxRowFill {
				continue
			}
			for cc := c + 1; cc <= reg.Right; cc++ {
				if !s.Rows[r][cc].IsEmpty() {
					pairs++
					break
				}
				if !coveredByMerge(s, r, cc) {
					break
				}
			}
		}
	}
	if filled == 0 {
		return 0
	}
	return float64(pairs) / float64(filled)
}

// coveredByMerge reports whether (r, c) is covered by a merged range without
// being that range's anchor (top-left) cell.
func coveredByMerge(s *sheetsource.Sample, r, c int) bool {
	for _, m := range s.Extras.Merged {
		if r >= m.FromRow && r <= m.ToRow && c >= m.FromCol && c <= m.ToCol && !(r == m.FromRow && c == m.FromCol) {
			return true
		}
	}
	return false
}

// mergesIn counts merged ranges intersecting reg.
func mergesIn(s *sheetsource.Sample, reg Region) int {
	n := 0
	for _, m := range s.Extras.Merged {
		if m.FromRow <= reg.Bottom && m.ToRow >= reg.Top && m.FromCol <= reg.Right && m.ToCol >= reg.Left {
			n++
		}
	}
	return n
}

// headerContrast is the anchor's score minus the median headerScore of the
// first five data rows: low contrast means the "header" row looks like a
// data row (R4), e.g. a Steckbrief's "Gebäude | Physiologie" label row.
func headerContrast(s *sheetsource.Sample, reg Region, hb *headerBlock) float64 {
	var scores []float64
	for r := hb.DataStart; r <= reg.Bottom && len(scores) < 5; r++ {
		scores = append(scores, headerScore(s, r, reg))
	}
	if len(scores) == 0 {
		return 1
	}
	sort.Float64s(scores)
	return hb.Confidence - scores[len(scores)/2]
}

// singleColumnIsTabular rejects a one-column block of sentences (R3): a
// region with exactly one kept column is a table only if >= 80% of its data
// cells are <= 40 runes and none ends with '.'.
func singleColumnIsTabular(s *sheetsource.Sample, reg Region, hb *headerBlock) bool {
	short, n := 0, 0
	for r := hb.DataStart; r <= reg.Bottom; r++ {
		cell := s.Rows[r][hb.Columns[0]]
		if cell.IsEmpty() {
			continue
		}
		n++
		txt := strings.TrimSpace(cell.Raw)
		if strings.HasSuffix(txt, ".") {
			return false
		}
		if utf8.RuneCountInString(txt) <= singleColMaxRunes {
			short++
		}
	}
	return n > 0 && float64(short) >= singleColShortRatio*float64(n)
}

// participatingFill is the mean fill ratio of the region's kept columns over
// its data rows, counting only columns whose own fill reaches
// spacerFillRatio. Columns that are empty throughout the sample are optional
// fields of the export, not evidence against the block being a table, so they
// leave the denominator entirely. Returns 0 when no column participates.
func participatingFill(s *sheetsource.Sample, reg Region, hb *headerBlock) float64 {
	dataRows := reg.Bottom - hb.DataStart + 1
	if dataRows <= 0 {
		return 0
	}
	fill, cells := 0, 0
	for _, c := range hb.Columns {
		n := 0
		for r := hb.DataStart; r <= reg.Bottom; r++ {
			if !s.Rows[r][c].IsEmpty() {
				n++
			}
		}
		if float64(n)/float64(dataRows) < spacerFillRatio {
			continue
		}
		fill += n
		cells += dataRows
	}
	if cells == 0 {
		return 0
	}
	return float64(fill) / float64(cells)
}

// classifyKind classifies a region as empty, form, table, or prose.
// Preflight ruling R4: the form check runs before the table check when
// labelValuePairRatio >= 0.4 and (merges >= 5 or the header contrast is
// low). The contrast arm additionally requires at least one merged range in
// the region: contrast is near zero for any all-text block (a header row of
// labels looks exactly like a row of text data), so on its own it made every
// narrow text table — and every merge-less delimited file — a form.
// Preflight ruling R3 (single-kept-column table gate) is enforced via
// singleColumnIsTabular.
func classifyKind(s *sheetsource.Sample, reg Region, hb *headerBlock, ok bool) SheetKind {
	if filledInRegion(s, reg) == 0 {
		return KindEmpty
	}
	pairs := labelValuePairRatio(s, reg)
	merges := mergesIn(s, reg)
	if pairs >= formPairRatio &&
		(merges >= formMinMerges || (ok && merges >= 1 && headerContrast(s, reg, hb) < formHeaderContrast)) {
		return KindForm
	}
	if ok {
		dataRows := reg.Bottom - hb.DataStart + 1
		if dataRows >= tableMinDataRows || (reg.OpenEnded && dataRows >= 1) {
			if participatingFill(s, reg, hb) >= tableMinFill &&
				(len(hb.Columns) > 1 || singleColumnIsTabular(s, reg, hb)) {
				return KindTable
			}
		}
	}
	if pairs >= formPairRatio || merges >= formMinMerges {
		return KindForm
	}
	return KindProse
}

func filledInRegion(s *sheetsource.Sample, reg Region) int {
	n := 0
	for r := reg.Top; r <= reg.Bottom; r++ {
		n += filledCount(s, r, reg)
	}
	return n
}
