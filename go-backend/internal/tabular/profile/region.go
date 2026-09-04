package profile

import "github.com/justrag/go-backend/internal/sheetsource"

// HeaderScoreFunc scores how header-like a row is inside a region (0..1).
type HeaderScoreFunc func(rowIdx int, reg Region) float64

const continuationHeaderScore = 0.35

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

		c := 0
		for c < s.Width {
			if !colHasAny(g, blockTop, blockBottom, c) {
				c++
				continue
			}
			left := c
			for c < s.Width && colHasAny(g, blockTop, blockBottom, c) {
				c++
			}
			right := c - 1
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
				if bestIdx >= 0 && score(trimmedTop, reg) < continuationHeaderScore {
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
