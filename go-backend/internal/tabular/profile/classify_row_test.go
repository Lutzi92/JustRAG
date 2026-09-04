package profile

import (
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
)

func cellT(s string) sheetsource.Cell {
	return sheetsource.Cell{Kind: sheetsource.KindText, Raw: s, Formatted: s}
}
func cellN(s string) sheetsource.Cell {
	return sheetsource.Cell{Kind: sheetsource.KindNumber, Raw: s, Formatted: s}
}
func cellF(formula, formatted string) sheetsource.Cell {
	return sheetsource.Cell{Kind: sheetsource.KindNumber, Raw: formatted, Formatted: formatted, IsFormula: true, Formula: formula}
}

// TestClassifyRowNullTokenRowsAreEmpty pins the half of R22 that the two
// sides disagreed on: the materialiser decided emptiness through
// ColumnAccumulator.Canonical (which maps a null token to NULL) while the
// renderer used a bare Cell.IsEmpty check, so a row of "-"/"n/a" produced no
// table row but still consumed a rendered record ordinal.
func TestClassifyRowNullTokenRowsAreEmpty(t *testing.T) {
	t.Parallel()
	kept := []int{0, 1, 2}
	cases := []struct {
		name  string
		cells []sheetsource.Cell
		empty bool
	}{
		{"all null tokens", []sheetsource.Cell{cellT("-"), cellT("n/a"), cellT("k.A.")}, true},
		{"blank cells", []sheetsource.Cell{cellT(""), {}, cellT("   ")}, true},
		{"mixed blank and null token", []sheetsource.Cell{cellT(""), cellT("-"), {}}, true},
		{"one real value", []sheetsource.Cell{cellT("-"), cellT("Haus 1"), cellT("n/a")}, false},
		{"numeric zero is data", []sheetsource.Cell{cellN("0"), cellT("-"), cellT("-")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			empty, derived := ClassifyRow(c.cells, kept, 100)
			if empty != c.empty {
				t.Errorf("empty = %v, want %v", empty, c.empty)
			}
			if empty && derived {
				t.Error("an empty row must never also be reported derived")
			}
		})
	}
}

// TestClassifyRowDerivedMatchesIsDerivedRow: the derived verdict is exactly
// IsDerivedRow's, so the two call sites cannot drift by using a different
// rule (only by passing a different regionRows — which is what RegionRows
// exists to prevent).
func TestClassifyRowDerivedMatchesIsDerivedRow(t *testing.T) {
	t.Parallel()
	kept := []int{0, 1}
	rows := [][]sheetsource.Cell{
		{cellT("Summe"), cellN("42")},
		{cellT("Haus 1"), cellN("42")},
		{cellT("Jahressumme"), cellF("=SUM(B2:B400)", "1234")},
	}
	for i, cells := range rows {
		for _, regionRows := range []int{3, 100, 1000} {
			_, derived := ClassifyRow(cells, kept, regionRows)
			if want := IsDerivedRow(cells, kept, regionRows); derived != want {
				t.Errorf("row %d regionRows %d: derived = %v, want %v", i, regionRows, derived, want)
			}
		}
	}
}

// TestRegionRows pins R22's shared regionRows: an exact height for a closed
// region; for an open-ended one, the sheet's declared row count (from the
// xlsx <dimension> ref, sheetsource.SheetInfo.RowCount) when known, else the
// shared fallback. Both the materialiser (which streams and cannot know the
// sheet's end) and the renderer (which buffers up to tabular_embed_max_rows
// and does see a maxRowSeen) must call this, or IsDerivedRow's "formula
// spans half the region" rule fires on one side only and the rendered
// markers stop addressing the same rows as _rowid.
func TestRegionRows(t *testing.T) {
	t.Parallel()
	if got := RegionRows(Region{Top: 0, Bottom: 99}, 1, 0); got != 99 {
		t.Errorf("closed region rows = %d, want 99", got)
	}
	if got := RegionRows(Region{Top: 0, Bottom: 5, OpenEnded: true}, 1, 0); got != FallbackRegionRows {
		t.Errorf("open-ended region rows with sheetRows=0 = %d, want the shared fallback %d", got, FallbackRegionRows)
	}
	// R58: a stale/placeholder dimension can declare sheetRows BELOW the
	// region's own dataStart (a raw-Go-writer xlsx stuck at "A1" is the
	// real-world example) -- that must read as unknown, not as a
	// one-row sheet, or IsDerivedRow's threshold collapses to ~0.
	if got := RegionRows(Region{Top: 0, Bottom: 5, OpenEnded: true}, 14, 1); got != FallbackRegionRows {
		t.Errorf("open-ended region rows with sheetRows=1 < dataStart=14 = %d, want the shared fallback %d", got, FallbackRegionRows)
	}
	if got := RegionRows(Region{Top: 0, Bottom: 5, OpenEnded: true}, 14, 13); got != FallbackRegionRows {
		t.Errorf("open-ended region rows with sheetRows=13 < dataStart=14 = %d, want the shared fallback %d", got, FallbackRegionRows)
	}
	// The boundary: sheetRows == dataStart means exactly one data row, and
	// it must be trusted (not treated as stale).
	if got := RegionRows(Region{Top: 0, Bottom: 5, OpenEnded: true}, 14, 14); got != 1 {
		t.Errorf("open-ended region rows with sheetRows=dataStart=14 = %d, want 1", got)
	}
	// Open-ended with a known sheet row count: sized from the sheet's end,
	// not the fallback constant.
	if got := RegionRows(Region{Top: 0, Bottom: 5, OpenEnded: true}, 14, 1000); got != 987 {
		t.Errorf("open-ended region rows with sheetRows=1000 dataStart=14 = %d, want 987", got)
	}
	// A degenerate region (data start past the bottom) must not go negative.
	if got := RegionRows(Region{Top: 0, Bottom: 2}, 10, 0); got != 0 {
		t.Errorf("degenerate region rows = %d, want 0", got)
	}
}
