//go:build integration

package sheetsource

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xuri/excelize/v2"
)

// TestStreamingMemoryGuard generates a 300 000-row workbook and asserts the
// streaming reader's heap growth stays bounded (spec §1.1 F5, §7.1
// large_synthetic). Fix-round-1 tightening: 100k rows fully buffered was
// itself only ~60-100 MiB, comfortably under a 150 MiB bound, so the guard
// could not fail even on a regression to whole-sheet buffering; 300k rows
// and a 64 MiB bound close that gap. runtime.GC() runs immediately before
// BOTH snapshots so retained rows show up in the delta and transient
// garbage does not.
func TestStreamingMemoryGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.xlsx")
	f := excelize.NewFile()
	sw, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.SetRow("A1", []any{"Beleg", "Lieferant", "Material", "Menge", "Preis"}); err != nil {
		t.Fatal(err)
	}
	const numRows = 300_000
	for r := 2; r <= numRows+1; r++ {
		if err := sw.SetRow(fmt.Sprintf("A%d", r), []any{4008000000 + r, fmt.Sprintf("Lieferant %d", r%500), 931404826 + r%10000, float64(r%50) + 0.5, float64(r%1000) * 1.25}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	src, err := OpenXLSX(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	rows := 0
	ex, err := src.ReadSheet(0, func(int, []Cell) error { rows++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if ex.RowCount != numRows+1 || rows != numRows+1 {
		t.Fatalf("rows=%d count=%d", rows, ex.RowCount)
	}
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew > 64<<20 {
		t.Fatalf("heap grew by %d MiB while streaming %d rows; whole-sheet buffering suspected", grew>>20, numRows)
	}
}
