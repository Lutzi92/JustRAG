package profile

import "github.com/justrag/go-backend/internal/sheetsource"

// HeaderScoreFunc scores how header-like a row is inside a region (0..1).
type HeaderScoreFunc func(rowIdx int, reg Region) float64

const continuationHeaderScore = 0.35

// regionWithBlockBottom tracks a region and its untrimmed row-block bottom.
type regionWithBlockBottom struct {
	region      Region
	blockBottom int
}

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
	var outInternal []regionWithBlockBottom
	r := 0
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
			if len(outInternal) > 0 {
				prevBlockBottom := outInternal[len(outInternal)-1].blockBottom
				if blockTop == prevBlockBottom+2 { // exactly one blank row between blocks
					// Find overlapping region from previous block with largest overlap
					bestIdx := -1
					bestOverlap := 0
					for i := len(outInternal) - 1; i >= 0 && outInternal[i].blockBottom == prevBlockBottom; i-- {
						prevReg := outInternal[i].region
						overlap := min(prevReg.Right, right) - max(prevReg.Left, left) + 1
						narrow := min(prevReg.Right-prevReg.Left, right-left) + 1
						if overlap > 0 && float64(overlap) >= 0.8*float64(narrow) {
							if overlap > bestOverlap {
								bestOverlap = overlap
								bestIdx = i
							}
						}
					}
					if bestIdx >= 0 && score(trimmedTop, reg) < continuationHeaderScore {
						// Merge into found region
						outInternal[bestIdx].region.Bottom = trimmedBottom
						outInternal[bestIdx].region.OpenEnded = reg.OpenEnded
						if reg.Left < outInternal[bestIdx].region.Left {
							outInternal[bestIdx].region.Left = reg.Left
						}
						if reg.Right > outInternal[bestIdx].region.Right {
							outInternal[bestIdx].region.Right = reg.Right
						}
						outInternal[bestIdx].blockBottom = blockBottom
						merged = true
					}
				}
			}
			if !merged {
				outInternal = append(outInternal, regionWithBlockBottom{region: reg, blockBottom: blockBottom})
			}
		}
	}

	// Extract regions from internal representation
	var out []Region
	for _, rb := range outInternal {
		out = append(out, rb.region)
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
