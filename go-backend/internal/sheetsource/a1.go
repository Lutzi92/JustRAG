package sheetsource

import (
	"fmt"
	"strings"
)

func ParseCellRef(ref string) (row, col int, err error) {
	ref = strings.ReplaceAll(strings.TrimSpace(ref), "$", "")
	i := 0
	for i < len(ref) && i < 3 && ref[i] >= 'A' && ref[i] <= 'Z' || i < len(ref) && i < 3 && ref[i] >= 'a' && ref[i] <= 'z' {
		c := ref[i]
		if c >= 'a' {
			c -= 'a' - 'A'
		}
		col = col*26 + int(c-'A'+1)
		i++
	}
	if i == 0 || i == len(ref) {
		return 0, 0, fmt.Errorf("sheetsource: bad cell ref %q", ref)
	}
	// Reject columns beyond XFD (16384 in 1-indexed, 16383 in 0-indexed)
	if col > 16384 {
		return 0, 0, fmt.Errorf("sheetsource: bad cell ref %q", ref)
	}
	n := 0
	for ; i < len(ref); i++ {
		if ref[i] < '0' || ref[i] > '9' {
			return 0, 0, fmt.Errorf("sheetsource: bad cell ref %q", ref)
		}
		n = n*10 + int(ref[i]-'0')
	}
	if n == 0 {
		return 0, 0, fmt.Errorf("sheetsource: bad cell ref %q", ref)
	}
	return n - 1, col - 1, nil
}

func ParseRange(ref string) (Range, error) {
	a, b, found := strings.Cut(ref, ":")
	r1, c1, err := ParseCellRef(a)
	if err != nil {
		return Range{}, err
	}
	if !found {
		return Range{r1, c1, r1, c1}, nil
	}
	r2, c2, err := ParseCellRef(b)
	if err != nil {
		return Range{}, err
	}
	return Range{min(r1, r2), min(c1, c2), max(r1, r2), max(c1, c2)}, nil
}

func ParseSqref(s string) ([]Range, error) {
	var out []Range
	for _, part := range strings.Fields(s) {
		r, err := ParseRange(part)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func ColumnName(col int) string {
	name := ""
	col++
	for col > 0 {
		col--
		name = string(rune('A'+col%26)) + name
		col /= 26
	}
	return name
}
