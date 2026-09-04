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
	for r < len(g) {
		if !rowHasAny(g[r]) {
			r++
			continue
		}
		top := r
		for r < len(g) && rowHasAny(g[r]) {
			r++
		}
		bottom := r - 1
		c := 0
		for c < s.Width {
			if !colHasAny(g, top, bottom, c) {
				c++
				continue
			}
			left := c
			for c < s.Width && colHasAny(g, top, bottom, c) {
				c++
			}
			right := c - 1
			// Trim rows from the bottom where all columns in [left, right] are empty
			trimmedBottom := bottom
			for trimmedBottom >= top && allColsEmpty(g, trimmedBottom, left, right) {
				trimmedBottom--
			}
			reg := Region{Top: top, Left: left, Bottom: trimmedBottom, Right: right, OpenEnded: trimmedBottom == len(g)-1}
			if n := len(out); n > 0 && continues(out[n-1], reg, score) {
				out[n-1].Bottom = reg.Bottom
				out[n-1].OpenEnded = reg.OpenEnded
				if reg.Left < out[n-1].Left {
					out[n-1].Left = reg.Left
				}
				if reg.Right > out[n-1].Right {
					out[n-1].Right = reg.Right
				}
				continue
			}
			out = append(out, reg)
		}
	}
	return out
}

func continues(prev, next Region, score HeaderScoreFunc) bool {
	if next.Top != prev.Bottom+2 { // exactly one blank row between
		return false
	}
	overlap := min(prev.Right, next.Right) - max(prev.Left, next.Left) + 1
	narrow := min(prev.Right-prev.Left, next.Right-next.Left) + 1
	if overlap <= 0 || float64(overlap) < 0.8*float64(narrow) {
		return false
	}
	return score(next.Top, next) < continuationHeaderScore
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
