//go:build manual

package sheetsource

import (
	"os"
	"testing"
)

func TestManualLedger(t *testing.T) {
	p := os.Getenv("LEDGER")
	if p == "" {
		t.Skip()
	}
	src, err := OpenXLSX(p)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	n := 0
	ex, err := src.ReadSheet(0, func(int, []Cell) error { n++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rows=%d maxcol=%d", ex.RowCount, ex.MaxCol)
}
