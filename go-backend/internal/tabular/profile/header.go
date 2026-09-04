package profile

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/sheetsource"
)

const (
	headerMinScore     = 0.45
	headerScanRows     = 20
	spacerFillRatio    = 0.05
	valueLikeMaxRunes  = 80
	titleRowMergeWidth = 3 // isTitleRow: a merged-anchor-only row is a title when the merge spans >= this many columns
	groupMergeWidth    = 2 // extension (R1): a merged-anchor row joins as a group-label row when the merge spans >= this many columns
)

// headerBlock is the detected header structure of a Region: the header row
// set, where data starts, an optional index row, prose found above the
// block, and the surviving (non-spacer) columns with their joined headers.
type headerBlock struct {
	Rows       []int // absolute row indices, ascending
	DataStart  int
	IndexRow   int // -1 when none
	ProseAbove []string
	Columns    []int    // kept absolute column indices (spacers removed)
	Headers    []string // joined header per kept column, same order as Columns
	Confidence float64  // best anchor score
}

func filledCount(s *sheetsource.Sample, r int, reg Region) int {
	n := 0
	for c := reg.Left; c <= reg.Right; c++ {
		if !s.Rows[r][c].IsEmpty() {
			n++
		}
	}
	return n
}

// modalWidth returns the most frequent count of filled cells per row in the
// region (rows with zero filled cells are excluded).
func modalWidth(s *sheetsource.Sample, reg Region) int {
	hist := map[int]int{}
	for r := reg.Top; r <= reg.Bottom; r++ {
		if n := filledCount(s, r, reg); n > 0 {
			hist[n]++
		}
	}
	best, bestN := 0, -1
	for w, n := range hist {
		if n > bestN || (n == bestN && w > best) {
			best, bestN = w, n
		}
	}
	return best
}

// isTitleRow reports whether row r reads as a title/metadata row rather than
// a header or data row: sparsely filled relative to the region's modal
// width, or a single cell that anchors a wide (>= 3 column) merge.
func isTitleRow(s *sheetsource.Sample, r int, reg Region, modal int) bool {
	n := filledCount(s, r, reg)
	if n == 0 {
		return false
	}
	if n <= 2 && modal >= 4 {
		return true
	}
	if n == 1 {
		for _, m := range s.Extras.Merged {
			if m.FromRow == r && m.ToRow == r && m.ToCol-m.FromCol+1 >= titleRowMergeWidth && !s.Rows[r][m.FromCol].IsEmpty() {
				return true
			}
		}
	}
	return false
}

// valueLike reports whether a cell reads as a value (number, date, bool, or
// very long text) rather than a header/category label.
func valueLike(c sheetsource.Cell) bool {
	if c.Kind == sheetsource.KindNumber || c.Kind == sheetsource.KindDate || c.Kind == sheetsource.KindBool {
		return true
	}
	if _, ok := ParseNumber(c.Raw, false); ok {
		return true
	}
	if _, ok := ParseNumber(c.Raw, true); ok {
		return true
	}
	if _, ok := ParseDateText(c.Raw); ok {
		return true
	}
	return utf8.RuneCountInString(c.Raw) > valueLikeMaxRunes
}

// firstFilledBelow returns the first non-empty cell in column c strictly
// below row r within the region, or nil when there is none.
func firstFilledBelow(s *sheetsource.Sample, r, c int, reg Region) *sheetsource.Cell {
	for rr := r + 1; rr <= reg.Bottom; rr++ {
		if !s.Rows[rr][c].IsEmpty() {
			return &s.Rows[rr][c]
		}
	}
	return nil
}

// headerScore scores how header-like row r is within reg (see the
// spreadsheet-ingest-phase1 spec for the weighting rationale). Returns 0 for
// rows with < 2 filled cells, unless the region is exactly one column wide
// (preflight ruling R2), in which case the single cell is scored.
func headerScore(s *sheetsource.Sample, r int, reg Region) float64 {
	filled, textOK, styled := 0, 0, 0
	seen := map[string]bool{}
	distinct := 0
	sig, sigDen := 0, 0
	for c := reg.Left; c <= reg.Right; c++ {
		cell := s.Rows[r][c]
		if cell.IsEmpty() {
			continue
		}
		filled++
		if cell.Kind == sheetsource.KindText && !valueLike(cell) {
			textOK++
		}
		if k := strings.ToLower(strings.TrimSpace(cell.Raw)); !seen[k] {
			seen[k] = true
			distinct++
		}
		if cell.Style.Bold || cell.Style.Filled || cell.Style.BottomBorder {
			styled++
		}
		sigDen++
		if below := firstFilledBelow(s, r, c, reg); below != nil && cell.Kind == sheetsource.KindText &&
			(below.Kind == sheetsource.KindNumber || below.Kind == sheetsource.KindDate) {
			sig++
		}
	}
	if filled == 0 {
		return 0
	}
	if filled < 2 && reg.Right > reg.Left { // multi-column regions need >= 2 filled cells; single-column regions score on their one cell (R2)
		return 0
	}

	covNum, covDen := 0, 0
	for c := reg.Left; c <= reg.Right; c++ {
		hasBelow := false
		for rr := r + 1; rr <= reg.Bottom && rr <= r+5; rr++ {
			if !s.Rows[rr][c].IsEmpty() {
				hasBelow = true
				break
			}
		}
		if hasBelow {
			covDen++
			if !s.Rows[r][c].IsEmpty() {
				covNum++
			}
		}
	}
	coverage := 0.0
	if covDen > 0 {
		coverage = float64(covNum) / float64(covDen)
	}

	f := float64(filled)
	return 0.35*float64(textOK)/f + 0.15*float64(distinct)/f + 0.25*coverage + 0.15*float64(sig)/float64(max(sigDen, 1)) + 0.10*float64(styled)/f
}

// isIndexRow reports whether row r's filled cells are the integers 1..n in
// increasing order (n >= 2).
func isIndexRow(s *sheetsource.Sample, r int, reg Region) bool {
	want := 1
	n := 0
	for c := reg.Left; c <= reg.Right; c++ {
		cell := s.Rows[r][c]
		if cell.IsEmpty() {
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

// hasWideMergeAnchor reports whether row r contains a filled cell that
// anchors a merged range spanning >= groupMergeWidth columns (a merged
// group-label cell, e.g. "Stammdaten" over several columns).
func hasWideMergeAnchor(s *sheetsource.Sample, r int, reg Region) bool {
	for _, m := range s.Extras.Merged {
		if m.FromRow == r && m.ToCol-m.FromCol+1 >= groupMergeWidth &&
			m.FromCol >= reg.Left && m.FromCol <= reg.Right && !s.Rows[r][m.FromCol].IsEmpty() {
			return true
		}
	}
	return false
}

// allNonValueText reports whether every filled cell in row r is text and not
// value-like (used by upward header-block extension, R1).
func allNonValueText(s *sheetsource.Sample, r int, reg Region) bool {
	n := 0
	for c := reg.Left; c <= reg.Right; c++ {
		cell := s.Rows[r][c]
		if cell.IsEmpty() {
			continue
		}
		if cell.Kind != sheetsource.KindText || valueLike(cell) {
			return false
		}
		n++
	}
	return n > 0
}

// joinHeader joins distinct, trimmed header parts (newlines collapsed to a
// single space) with " / ", preserving first-seen order.
func joinHeader(parts []string) string {
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.Join(strings.Fields(strings.ReplaceAll(p, "\n", " ")), " ")
		if p == "" || seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	return strings.Join(out, " / ")
}

// findAnchor scans the first min(headerScanRows, rows) non-title, non-empty
// rows of the region for the row with the highest headerScore, requiring at
// least headerMinScore.
func findAnchor(s *sheetsource.Sample, reg Region, modal int) (row int, score float64) {
	row, score = -1, 0.0
	scanned := 0
	for r := reg.Top; r <= reg.Bottom && scanned < headerScanRows; r++ {
		if filledCount(s, r, reg) == 0 || isTitleRow(s, r, reg, modal) {
			continue
		}
		scanned++
		if sc := headerScore(s, r, reg); sc > score {
			score, row = sc, r
		}
	}
	if row < 0 || score < headerMinScore {
		return -1, 0
	}
	return row, score
}

// extendUp extends the header block upward from the anchor row (R1): a row
// joins when all its filled cells are non-value-like text and either has
// more than 2 filled cells or carries a wide merged group label. An index
// row (1..n) is captured separately (not joined) only when it sits directly
// against the block being built so far (r == rows[0]-1 at the moment it is
// examined) — true by construction for the first index-shaped row met,
// since the loop walks upward one row at a time from the block's current
// top. After that one is recorded, extension continues past it (a
// group-header row may still join above the index row), but a second
// index-shaped row is never adjacent to the (unchanged) block top and stops
// the extension outright rather than being recorded or joined. Stops at the
// first blank row, the first row failing the join rule, or after
// maxHeaderRows-1 extra rows.
func extendUp(s *sheetsource.Sample, reg Region, anchor, maxHeaderRows int) (rows []int, indexRow int) {
	rows = []int{anchor}
	indexRow = -1
	for r := anchor - 1; r >= reg.Top && len(rows) < maxHeaderRows; r-- {
		if filledCount(s, r, reg) == 0 {
			break
		}
		if isIndexRow(s, r, reg) {
			if indexRow == -1 && r == rows[0]-1 {
				indexRow = r
				continue
			}
			break // a second (or non-adjacent) index-shaped row stops the extension
		}
		if !allNonValueText(s, r, reg) || (filledCount(s, r, reg) <= 2 && !hasWideMergeAnchor(s, r, reg)) {
			break
		}
		rows = append([]int{r}, rows...)
	}
	sort.Ints(rows)
	// Defensive guard: the recorded index row is only ever adjacent to the row
	// that was rows[0] at the time it was recorded, and that row is never
	// removed from rows afterward — so indexRow+1 always stays a member of
	// rows. This can't currently fail; kept in case a future rule changes that.
	if indexRow != -1 && !containsInt(rows, indexRow+1) {
		indexRow = -1
	}
	return rows, indexRow
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// prependProse collects every region row above blockTop (title rows,
// metadata) as ProseAbove, skipping the index row.
func prependProse(s *sheetsource.Sample, reg Region, blockTop, indexRow int) []string {
	var out []string
	for r := reg.Top; r < blockTop; r++ {
		if r == indexRow {
			continue
		}
		var parts []string
		for c := reg.Left; c <= reg.Right; c++ {
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

// blockTexts builds the per-cell raw text of the header block, plus a
// merge-filled copy in which each merged range intersecting the header rows
// has its anchor text copied into every covered cell (horizontal and
// vertical). The raw (unfilled) copy is what decides whether a column has
// genuine header text of its own; the merge-filled copy is what gets joined
// into the displayed header for a kept column, so a merged group label
// (e.g. "Stammdaten") shows up alongside a column's own label without, by
// itself, rescuing an otherwise-empty spacer column that the label merely
// happens to span.
func blockTexts(s *sheetsource.Sample, reg Region, rows []int) (raw, filled map[[2]int]string) {
	raw = map[[2]int]string{}
	filled = map[[2]int]string{}
	for _, r := range rows {
		for c := reg.Left; c <= reg.Right; c++ {
			v := s.Rows[r][c].Raw
			raw[[2]int{r, c}] = v
			filled[[2]int{r, c}] = v
		}
	}
	inBlock := map[int]bool{}
	for _, r := range rows {
		inBlock[r] = true
	}
	for _, m := range s.Extras.Merged {
		if !inBlock[m.FromRow] || m.FromCol < 0 || m.FromCol >= len(s.Rows[m.FromRow]) {
			continue
		}
		anchor := s.Rows[m.FromRow][m.FromCol].Raw
		for r := m.FromRow; r <= m.ToRow; r++ {
			if !inBlock[r] {
				continue
			}
			for c := m.FromCol; c <= m.ToCol; c++ {
				filled[[2]int{r, c}] = anchor
			}
		}
	}
	return raw, filled
}

// keepColumns decides which region columns survive as data columns
// (non-spacer): a column is kept when it carries genuine header text of its
// own (raw, pre-merge-fill) in any header row, or when its fill ratio over
// the sample's data rows is >= spacerFillRatio. A kept column's header is
// the joined (merge-filled) text top-down, defaulting to "Spalte <name>"
// when that is empty too (the fill-ratio-only case).
func keepColumns(s *sheetsource.Sample, reg Region, rows []int, dataStart int, raw, filled map[[2]int]string) (cols []int, headers []string) {
	dataRows := 0
	for r := dataStart; r <= reg.Bottom; r++ {
		dataRows++
	}
	for c := reg.Left; c <= reg.Right; c++ {
		ownText := false
		for _, r := range rows {
			if strings.TrimSpace(raw[[2]int{r, c}]) != "" {
				ownText = true
				break
			}
		}
		fillN := 0
		for r := dataStart; r <= reg.Bottom; r++ {
			if !s.Rows[r][c].IsEmpty() {
				fillN++
			}
		}
		if !ownText && (dataRows == 0 || float64(fillN)/float64(dataRows) < spacerFillRatio) {
			continue
		}
		var parts []string
		for _, r := range rows {
			parts = append(parts, filled[[2]int{r, c}])
		}
		h := joinHeader(parts)
		if h == "" {
			h = "Spalte " + sheetsource.ColumnName(c)
		}
		cols = append(cols, c)
		headers = append(headers, h)
	}
	return cols, headers
}

// detectHeaderBlock finds the header block of a Region: the anchor row
// (highest-scoring non-title row, argmax over the first min(headerScanRows,
// rows) candidates, requiring score >= headerMinScore), extended upward per
// R1, plus an optional index row, prose above, and the surviving columns
// with joined headers. ok is false when no row scores high enough.
func detectHeaderBlock(s *sheetsource.Sample, reg Region, maxHeaderRows int) (headerBlock, bool) {
	if maxHeaderRows <= 0 {
		maxHeaderRows = 3
	}
	modal := modalWidth(s, reg)
	hb := headerBlock{IndexRow: -1}

	anchor, score := findAnchor(s, reg, modal)
	if anchor < 0 {
		return hb, false
	}
	hb.Confidence = score

	rows, indexRow := extendUp(s, reg, anchor, maxHeaderRows)
	hb.Rows = rows
	hb.IndexRow = indexRow
	hb.DataStart = anchor + 1
	hb.ProseAbove = prependProse(s, reg, rows[0], indexRow)

	raw, filled := blockTexts(s, reg, rows)
	hb.Columns, hb.Headers = keepColumns(s, reg, rows, hb.DataStart, raw, filled)

	return hb, len(hb.Columns) > 0
}
