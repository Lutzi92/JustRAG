package profile

import "github.com/justrag/go-backend/internal/sheetsource"

// HeaderScoreFunc scores how header-like a row is inside a region (0..1).
type HeaderScoreFunc func(rowIdx int, reg Region) float64

const (
	// continuationHeaderScore is the absolute "not header-like" bar a
	// candidate block's first row must clear to be merged into the block
	// above it. It is the only test applied to one-column candidates.
	continuationHeaderScore = 0.35
	// continuationFlatDelta is the relative test applied to multi-column
	// candidates in addition to the absolute one: headerScore's `distinct`
	// and `coverage` terms are both 1.0 for *any* densely filled row, so a
	// full-width data row has a hard floor around 0.40 and can never clear
	// continuationHeaderScore. What actually separates a header from data is
	// that a header stands out from the row directly beneath it; a
	// continuation row does not.
	continuationFlatDelta = 0.15
	// gutterMinWidth is the minimum width, in columns, each of two adjacent
	// column blocks must have before a single blank column between them is
	// treated as a spacer inside one region rather than a region boundary.
	// One-column blocks are excluded so that side-by-side dropdown lists stay
	// separate regions.
	gutterMinWidth = 2
	// gutterRowAlign is the fraction of the narrower block's filled rows that
	// must also be filled in the wider block for the two to be the same table.
	gutterRowAlign = 0.9
)

func filledGrid(s *sheetsource.Sample) [][]bool {
	g := make([][]bool, len(s.Rows))
	for r := range s.Rows {
		g[r] = make([]bool, s.Width)
		for c := range s.Rows[r] {
			g[r][c] = !s.Rows[r][c].IsEmpty()
		}
	}
	for _, m := range s.Extras.Merged {
		if m.FromRow >= len(g) || m.FromCol >= s.Width || !g[m.FromRow][m.FromCol] {
			continue
		}
		for r := m.FromRow; r <= m.ToRow && r < len(g); r++ {
			for c := m.FromCol; c <= m.ToCol && c < s.Width; c++ {
				g[r][c] = true
			}
		}
	}
	return g
}

func DetectRegions(s *sheetsource.Sample, score HeaderScoreFunc) []Region {
	g := filledGrid(s)
	var out []Region
	r := 0
	prevBlockBottom := -2
	var prevBlockRegions []int

	for r < len(g) {
		if !rowHasAny(g[r]) {
			r++
			continue
		}
		blockTop := r
		for r < len(g) && rowHasAny(g[r]) {
			r++
		}
		blockBottom := r - 1

		var currentBlockRegions []int

		blocks := mergeGutterBlocks(g, columnBlocks(g, blockTop, blockBottom, s.Width), blockTop, blockBottom)
		for _, blk := range blocks {
			left, right := blk[0], blk[1]
			// Trim top: find first row in [blockTop, blockBottom] with a filled cell in [left, right]
			trimmedTop := blockTop
			for trimmedTop <= blockBottom && allColsEmpty(g, trimmedTop, left, right) {
				trimmedTop++
			}
			// Trim bottom: find last row in [blockTop, blockBottom] with a filled cell in [left, right]
			trimmedBottom := blockBottom
			for trimmedBottom >= trimmedTop && allColsEmpty(g, trimmedBottom, left, right) {
				trimmedBottom--
			}
			reg := Region{Top: trimmedTop, Left: left, Bottom: trimmedBottom, Right: right, OpenEnded: trimmedBottom == len(g)-1}

			// Look for a previous-block region to merge with
			merged := false
			if blockTop == prevBlockBottom+2 && len(prevBlockRegions) > 0 { // exactly one blank row between blocks
				// Find overlapping region from previous block with largest overlap
				bestIdx := -1
				bestOverlap := 0
				for _, idx := range prevBlockRegions {
					prevReg := out[idx]
					overlap := min(prevReg.Right, right) - max(prevReg.Left, left) + 1
					narrow := min(prevReg.Right-prevReg.Left, right-left) + 1
					if overlap > 0 && float64(overlap) >= 0.8*float64(narrow) {
						if overlap > bestOverlap {
							bestOverlap = overlap
							bestIdx = idx
						}
					}
				}
				if bestIdx >= 0 && isContinuation(out[bestIdx], reg, score) {
					// Merge into found region
					out[bestIdx].Bottom = trimmedBottom
					out[bestIdx].OpenEnded = reg.OpenEnded
					if reg.Left < out[bestIdx].Left {
						out[bestIdx].Left = reg.Left
					}
					if reg.Right > out[bestIdx].Right {
						out[bestIdx].Right = reg.Right
					}
					currentBlockRegions = append(currentBlockRegions, bestIdx)
					merged = true
				}
			}
			if !merged {
				currentBlockRegions = append(currentBlockRegions, len(out))
				out = append(out, reg)
			}
		}

		// Update tracking for next block
		prevBlockBottom = blockBottom
		prevBlockRegions = currentBlockRegions
	}

	return out
}

// columnBlocks splits [blockTop, blockBottom] into the maximal runs of
// columns that have at least one filled cell, as inclusive {left, right}
// pairs in ascending order.
func columnBlocks(g [][]bool, blockTop, blockBottom, width int) [][2]int {
	var out [][2]int
	c := 0
	for c < width {
		if !colHasAny(g, blockTop, blockBottom, c) {
			c++
			continue
		}
		left := c
		for c < width && colHasAny(g, blockTop, blockBottom, c) {
			c++
		}
		out = append(out, [2]int{left, c - 1})
	}
	return out
}

// mergeGutterBlocks joins adjacent column blocks separated by exactly one
// fully blank column when both are at least gutterMinWidth columns wide and
// their rows line up (see rowsAligned). Real forms put a narrow spacer column
// between two groups of columns of one and the same table — the
// `Stammdaten` / `Einschätzung aus Nutzersicht` split of the JLU
// Gebäudeliste is exactly that — so a lone blank column is not a region
// boundary. The gutter column stays inside the merged block and is dropped
// later by keepColumns as a spacer. Merging is left-to-right and the
// accumulated span is what the next block is compared against, so a chain of
// gutter-separated blocks collapses into one.
func mergeGutterBlocks(g [][]bool, blocks [][2]int, blockTop, blockBottom int) [][2]int {
	if len(blocks) < 2 {
		return blocks
	}
	out := [][2]int{blocks[0]}
	for _, next := range blocks[1:] {
		cur := out[len(out)-1]
		if next[0]-cur[1] == 2 &&
			cur[1]-cur[0]+1 >= gutterMinWidth && next[1]-next[0]+1 >= gutterMinWidth &&
			rowsAligned(g, cur, next, blockTop, blockBottom) {
			out[len(out)-1] = [2]int{cur[0], next[1]}
			continue
		}
		out = append(out, next)
	}
	return out
}

// rowsAligned reports whether the two column blocks are filled on essentially
// the same rows: the rows filled in both, over the rows filled in whichever
// block has more of them, must reach gutterRowAlign. Two column groups of one
// table share their rows; a two-row legend parked beside a six-row table does
// not, and measuring against the shorter block alone would call that a match.
func rowsAligned(g [][]bool, a, b [2]int, top, bottom int) bool {
	na, nb, both := 0, 0, 0
	for r := top; r <= bottom; r++ {
		fa := !allColsEmpty(g, r, a[0], a[1])
		fb := !allColsEmpty(g, r, b[0], b[1])
		if fa {
			na++
		}
		if fb {
			nb++
		}
		if fa && fb {
			both++
		}
	}
	most := max(na, nb)
	return most > 0 && float64(both) >= gutterRowAlign*float64(most)
}

// isContinuation reports whether reg's first row reads as more data of prev,
// the overlapping region in the block above, rather than a header of its own.
// A one-column candidate is judged by the absolute bar alone; a multi-column
// candidate with at least two rows may also qualify by being
// indistinguishable from the row directly beneath it (continuationFlatDelta).
//
// The relative arm additionally requires prev to have a distinguishable
// header of its own — the same test, applied to the block above. "This block
// is more rows of the table above" presupposes that there is a table above;
// in a Steckbrief-style form every block's rows look alike by construction,
// so without that condition each block would chain into the one before it and
// the whole form would collapse into a single table region.
func isContinuation(prev, reg Region, score HeaderScoreFunc) bool {
	top := score(reg.Top, reg)
	if top < continuationHeaderScore {
		return true
	}
	if reg.Right > reg.Left && reg.Bottom > reg.Top && hasDistinctHeaderRow(prev, score) {
		return top-score(reg.Top+1, reg) < continuationFlatDelta
	}
	return false
}

// hasDistinctHeaderRow reports whether reg's first row stands out from the row
// directly beneath it by at least continuationFlatDelta — i.e. whether reg
// looks like a table with a header rather than a uniform block of rows.
func hasDistinctHeaderRow(reg Region, score HeaderScoreFunc) bool {
	if reg.Bottom <= reg.Top {
		return false
	}
	return score(reg.Top, reg)-score(reg.Top+1, reg) >= continuationFlatDelta
}

func rowHasAny(row []bool) bool {
	for _, v := range row {
		if v {
			return true
		}
	}
	return false
}

func colHasAny(g [][]bool, top, bottom, c int) bool {
	for r := top; r <= bottom; r++ {
		if g[r][c] {
			return true
		}
	}
	return false
}

func allColsEmpty(g [][]bool, row, left, right int) bool {
	for c := left; c <= right; c++ {
		if g[row][c] {
			return false
		}
	}
	return true
}
