//go:build integration

package sheetsource

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xuri/excelize/v2"
)

// TestStreamingMemoryGuard generates a 100 000-row workbook and asserts the
// streaming reader's heap growth stays bounded (spec §1.1 F5, §7.1
// large_synthetic). The bound is generous; it guards against a return to
// whole-sheet buffering, not against small regressions.
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
	for r := 2; r <= 100_001; r++ {
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
	runtime.ReadMemStats(&after)
	if ex.RowCount != 100_001 || rows != 100_001 {
		t.Fatalf("rows=%d count=%d", rows, ex.RowCount)
	}
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew > 150<<20 {
		t.Fatalf("heap grew by %d MiB while streaming 100k rows; whole-sheet buffering suspected", grew>>20)
	}
}
