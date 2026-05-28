package tabular

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCSV(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.csv")
	if err := os.WriteFile(p, []byte("Name,Age\nAnn,30\nBob,25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sheets, err := ReadSheets(p, "x.csv")
	if err != nil {
		t.Fatalf("ReadSheets: %v", err)
	}
	if len(sheets) != 1 {
		t.Fatalf("want 1 sheet, got %d", len(sheets))
	}
	s := sheets[0]
	if len(s.Rows) != 3 || s.Rows[0][0] != "Name" {
		t.Fatalf("unexpected rows: %v", s.Rows)
	}
}

func TestIsSpreadsheet(t *testing.T) {
	if !IsSpreadsheet("", "data.csv") || !IsSpreadsheet("text/csv", "x") {
		t.Fatal("csv not detected")
	}
	if !IsSpreadsheet("", "report.xlsx") {
		t.Fatal("xlsx not detected")
	}
	if IsSpreadsheet("", "notes.pdf") {
		t.Fatal("pdf wrongly detected")
	}
}
