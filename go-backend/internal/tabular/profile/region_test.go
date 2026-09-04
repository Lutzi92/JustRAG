package profile

import (
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
)

func grid(rows ...string) *sheetsource.Sample {
	// each string: cells separated by '|', "." = empty, "#" = number, otherwise text
	var out [][]sheetsource.Cell
	width := 0
	for _, r := range rows {
		var cells []sheetsource.Cell
		for _, tok := range splitPipe(r) {
			switch tok {
			case ".":
				cells = append(cells, sheetsource.Cell{})
			case "#":
				cells = append(cells, sheetsource.Cell{Kind: sheetsource.KindNumber, Raw: "1", Formatted: "1"})
			default:
				cells = append(cells, sheetsource.Cell{Kind: sheetsource.KindText, Raw: tok, Formatted: tok})
			}
		}
		if len(cells) > width {
			width = len(cells)
		}
		out = append(out, cells)
	}
	for i := range out {
		for len(out[i]) < width {
			out[i] = append(out[i], sheetsource.Cell{})
		}
	}
	return &sheetsource.Sample{Rows: out, Width: width, TotalRows: len(out)}
}

func splitPipe(s string) []string {
	var out []string
	cur := ""
	for _, ch := range s {
		if ch == '|' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(ch)
	}
	return append(out, cur)
}

func TestDetectRegionsSideBySideAndStacked(t *testing.T) {
	t.Parallel()
	s := grid(
		".|.|.|.|.",
		".|Ja / Nein|.|Priorisierung|.",
		".|Ja|.|#|hoch",
		".|Nein|.|#|mittel",
		".|.|.|#|niedrig",
		".|.|.|.|.",
		".|Qualität|.|.|.",
		".|gut|.|.|.",
		".|mittel|.|.|.",
	)
	regs := DetectRegions(s, func(int, Region) float64 { return 0.9 })
	if len(regs) != 3 {
		t.Fatalf("regions = %+v", regs)
	}
	if regs[0] != (Region{1, 1, 3, 1, false}) || regs[1] != (Region{1, 3, 4, 4, false}) || regs[2] != (Region{6, 1, 8, 1, true}) {
		t.Errorf("regions = %+v", regs)
	}
}

func TestDetectRegionsContinuationMerge(t *testing.T) {
	t.Parallel()
	s := grid(
		"Gebäude|Baujahr|BGF",
		"A|#|#",
		"B|#|#",
		".|.|.",
		"C|#|#",
		"D|#|#",
	)
	// second block starts with a data-like row (score 0.1) -> merged into the first
	regs := DetectRegions(s, func(r int, _ Region) float64 {
		if r == 0 {
			return 0.9
		}
		return 0.1
	})
	if len(regs) != 1 || regs[0] != (Region{0, 0, 5, 2, true}) {
		t.Errorf("regions = %+v", regs)
	}
}

func TestDetectRegionsTitleThenTable(t *testing.T) {
	t.Parallel()
	s := grid(
		".|Titel|.|.",
		".|.|.|.",
		".|.|.|.",
		"H1|H2|H3|H4",
		"a|#|#|x",
		"b|#|#|y",
		"c|#|#|z",
	)
	regs := DetectRegions(s, func(int, Region) float64 { return 0.9 })
	if len(regs) != 2 || regs[0] != (Region{0, 1, 0, 1, false}) || regs[1] != (Region{3, 0, 6, 3, true}) {
		t.Errorf("regions = %+v", regs)
	}
}

func TestDetectRegionsTrimsTop(t *testing.T) {
	t.Parallel()
	s := grid(
		"A|.|.|.",
		".|.|B|X",
		".|.|C|Y",
	)
	regs := DetectRegions(s, func(int, Region) float64 { return 0.9 })
	if len(regs) != 2 || regs[0] != (Region{0, 0, 0, 0, false}) || regs[1] != (Region{1, 2, 2, 3, true}) {
		t.Errorf("regions = %+v", regs)
	}
}

func TestDetectRegionsContinuationUnderMiddleBlock(t *testing.T) {
	t.Parallel()
	s := grid(
		"H|.|H|.|H",
		"#|.|#|.|#",
		"#|.|#|.|#",
		".|.|.|.|.",
		".|.|#|.|.",
		".|.|#|.|.",
	)
	regs := DetectRegions(s, func(r int, _ Region) float64 {
		if r == 0 {
			return 0.9
		}
		return 0.1
	})
	if len(regs) != 3 {
		t.Errorf("expected 3 regions, got %d: %+v", len(regs), regs)
		return
	}
	if regs[0] != (Region{0, 0, 2, 0, false}) {
		t.Errorf("region[0]: expected {0,0,2,0,false}, got %+v", regs[0])
	}
	if regs[1] != (Region{0, 2, 5, 2, true}) {
		t.Errorf("region[1]: expected {0,2,5,2,true}, got %+v", regs[1])
	}
	if regs[2] != (Region{0, 4, 2, 4, false}) {
		t.Errorf("region[2]: expected {0,4,2,4,false}, got %+v", regs[2])
	}
}
