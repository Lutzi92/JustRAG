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

func TestDetectRegionsTwoContinuationsSameBlock(t *testing.T) {
	t.Parallel()
	s := grid(
		"H|.|H",
		"#|.|#",
		"#|.|#",
		".|.|.",
		"#|.|#",
		"#|.|#",
	)
	regs := DetectRegions(s, func(r int, _ Region) float64 {
		if r == 0 {
			return 0.9
		}
		return 0.1
	})
	if len(regs) != 2 {
		t.Errorf("expected 2 regions, got %d: %+v", len(regs), regs)
		return
	}
	if regs[0] != (Region{0, 0, 5, 0, true}) {
		t.Errorf("region[0]: expected {0,0,5,0,true}, got %+v", regs[0])
	}
	if regs[1] != (Region{0, 2, 5, 2, true}) {
		t.Errorf("region[1]: expected {0,2,5,2,true}, got %+v", regs[1])
	}
}

func TestDetectRegionsThreeSectionChainMiddleColumn(t *testing.T) {
	t.Parallel()
	s := grid(
		"H|.|H|.|H",
		"#|.|#|.|#",
		"#|.|#|.|#",
		".|.|.|.|.",
		".|.|#|.|.",
		".|.|#|.|.",
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
	if regs[1] != (Region{0, 2, 8, 2, true}) {
		t.Errorf("region[1]: expected {0,2,8,2,true}, got %+v", regs[1])
	}
	if regs[2] != (Region{0, 4, 2, 4, false}) {
		t.Errorf("region[2]: expected {0,4,2,4,false}, got %+v", regs[2])
	}
}

func TestDetectRegionsNewColumnBeforeContinuation(t *testing.T) {
	t.Parallel()
	s := grid(
		".|.|H",
		".|.|#",
		".|.|#",
		".|.|.",
		"#|.|#",
		"#|.|#",
	)
	regs := DetectRegions(s, func(r int, _ Region) float64 {
		if r == 0 {
			return 0.9
		}
		return 0.1
	})
	if len(regs) != 2 {
		t.Errorf("expected 2 regions, got %d: %+v", len(regs), regs)
		return
	}
	if regs[0] != (Region{0, 2, 5, 2, true}) {
		t.Errorf("region[0]: expected {0,2,5,2,true}, got %+v", regs[0])
	}
	if regs[1] != (Region{4, 0, 5, 0, true}) {
		t.Errorf("region[1]: expected {4,0,5,0,true}, got %+v", regs[1])
	}
}

// realScore is DetectRegions' production scorer bound to s. The stubbed
// scorers above deliberately never exercise headerScore's actual range.
func realScore(s *sheetsource.Sample) HeaderScoreFunc {
	return func(r int, reg Region) float64 { return headerScore(s, r, reg) }
}

func TestDetectRegionsGutterColumnMerged(t *testing.T) {
	t.Parallel()
	// Two three-column blocks separated by one blank column, filled on the
	// same rows: one table with a spacer, not two regions.
	s := grid(
		"Nr|Name|Ort|.|Note|Jahr|BGF",
		"1|A|X|.|#|#|#",
		"2|B|Y|.|#|#|#",
		"3|C|Z|.|#|#|#",
	)
	regs := DetectRegions(s, realScore(s))
	if len(regs) != 1 || regs[0] != (Region{0, 0, 3, 6, true}) {
		t.Errorf("regions = %+v", regs)
	}
}

func TestDetectRegionsGutterOneColumnListsStaySplit(t *testing.T) {
	t.Parallel()
	s := grid(
		"Ja / Nein|.|Priorisierung",
		"Ja|.|hoch",
		"Nein|.|mittel",
	)
	regs := DetectRegions(s, realScore(s))
	if len(regs) != 2 || regs[0] != (Region{0, 0, 2, 0, true}) || regs[1] != (Region{0, 2, 2, 2, true}) {
		t.Errorf("regions = %+v", regs)
	}
}

func TestDetectRegionsGutterUnalignedRowsStaySplit(t *testing.T) {
	t.Parallel()
	// The narrower block is filled on 2 rows, the wider one on 6: a legend
	// parked beside a table, not a second column group of it.
	s := grid(
		"Legende|kurz|.|Nr|Name|Ort",
		"A|Alt|.|1|A|X",
		".|.|.|2|B|Y",
		".|.|.|3|C|Z",
		".|.|.|4|D|W",
		".|.|.|5|E|V",
	)
	regs := DetectRegions(s, realScore(s))
	if len(regs) != 2 || regs[0] != (Region{0, 0, 1, 1, false}) || regs[1] != (Region{0, 3, 5, 5, true}) {
		t.Errorf("regions = %+v", regs)
	}
}

func TestDetectRegionsContinuationWithRealHeaderScore(t *testing.T) {
	t.Parallel()
	// headerScore's `distinct` and `coverage` terms are both 1.0 for any dense
	// row, so a full-width data row scores ~0.49 and can never clear the
	// absolute 0.35 bar. The relative arm is what merges this Sections-shaped
	// sheet back into one region.
	s := grid(
		"Gebäude|Baujahr|BGF|Note",
		"Hörsaalgebäude|#|#|#",
		"Bibliothek|#|#|#",
		"Mensa|#|#|#",
		"Verwaltung|#|#|#",
		"Werkstatt|#|#|#",
		".|.|.|.",
		"Institutsgebäude|#|#|#",
		"Sporthalle|#|#|#",
		"Rechenzentrum|#|#|#",
		"Labor|#|#|#",
		"Pförtnerloge|#|#|#",
	)
	boldRow(s, 0)
	if got := headerScore(s, 7, Region{Top: 7, Left: 0, Bottom: 11, Right: 3, OpenEnded: true}); got < continuationHeaderScore {
		t.Fatalf("fixture no longer exercises the relative arm: score(7) = %.4f < %.2f", got, continuationHeaderScore)
	}
	regs := DetectRegions(s, realScore(s))
	if len(regs) != 1 || regs[0].Bottom != 11 {
		t.Errorf("regions = %+v", regs)
	}
}
