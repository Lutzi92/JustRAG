package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// createTestXLSX creates a temporary XLSX file with the given headers and rows
// in Sheet1 and returns its path.
func createTestXLSX(t *testing.T, headers []string, rows [][]string) string {
	t.Helper()
	f := excelize.NewFile()
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue("Sheet1", cell, h)
	}
	for r, row := range rows {
		for c, val := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+2)
			f.SetCellValue("Sheet1", cell, val)
		}
	}
	path := filepath.Join(t.TempDir(), "test.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("createTestXLSX: %v", err)
	}
	return path
}

func TestXLSXParserCanParse(t *testing.T) {
	t.Parallel()
	p := &XLSXParser{}
	cases := []struct {
		mime     string
		fileName string
		want     bool
	}{
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "data.xlsx", true},
		{"application/octet-stream", "report.xlsx", true},
		{"application/octet-stream", "legacy.xls", false},                                       // .xls handled by XLSParser
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "file.csv", true}, // mime alone is sufficient
		{"text/plain", "file.txt", false},
		{"application/json", "data.json", false},
		{"text/csv", "data.csv", false},
	}
	for _, tc := range cases {
		got := p.CanParse(tc.mime, tc.fileName)
		if got != tc.want {
			t.Errorf("CanParse(%q, %q) = %v, want %v", tc.mime, tc.fileName, got, tc.want)
		}
	}
}

func TestXLSXParserSimpleSpreadsheet(t *testing.T) {
	t.Parallel()
	headers := []string{"name", "age", "city"}
	rows := [][]string{
		{"Alice", "30", "Berlin"},
		{"Bob", "25", "Munich"},
	}
	path := createTestXLSX(t, headers, rows)

	p := &XLSXParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath:  path,
		FileName:  "test.xlsx",
		MimeType:  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		ChunkSize: 512,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text

	// Sheet heading must be present.
	if !strings.Contains(text, "### Sheet: Sheet1") {
		t.Error("expected sheet heading '### Sheet: Sheet1', not found")
	}
	// Markdown header separator must be present.
	if !strings.Contains(text, "| --- |") {
		t.Error("expected markdown header separator, not found")
	}
	// Column headers must appear.
	for _, col := range headers {
		if !strings.Contains(text, col) {
			t.Errorf("expected column %q in output", col)
		}
	}
	// Data values must appear.
	for _, val := range []string{"Alice", "30", "Berlin", "Bob", "25", "Munich"} {
		if !strings.Contains(text, val) {
			t.Errorf("expected value %q in output", val)
		}
	}
}

func TestXLSXParserChunksLargeSheetWithRepeatedHeaders(t *testing.T) {
	t.Parallel()
	headers := []string{"id", "value", "label"}
	var rows [][]string
	for i := 0; i < 100; i++ {
		rows = append(rows, []string{
			fmt.Sprintf("%d", i),
			fmt.Sprintf("val%d", i),
			fmt.Sprintf("label%d", i),
		})
	}
	path := createTestXLSX(t, headers, rows)

	p := &XLSXParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath:  path,
		FileName:  "large.xlsx",
		MimeType:  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		ChunkSize: 32, // very small → forces many chunks
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Strip the sheet heading to analyse chunk boundaries.
	// The heading is followed by two newlines, then the table chunks.
	afterHeading := strings.SplitN(result.Text, "\n\n", 2)
	if len(afterHeading) < 2 {
		t.Fatalf("expected content after sheet heading, got: %q", result.Text)
	}
	tableText := afterHeading[1]

	chunks := strings.Split(tableText, "\n\n")
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	// Every chunk must start with the header row and contain the separator.
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, "| id |") {
			t.Errorf("chunk %d does not start with header row: %q", i, chunk[:min(len(chunk), 60)])
		}
		if !strings.Contains(chunk, "| --- |") {
			t.Errorf("chunk %d missing header separator", i)
		}
	}
}

func TestXLSXParserMultipleSheets(t *testing.T) {
	t.Parallel()
	f := excelize.NewFile()

	// Sheet1 is created by default; add data to it.
	f.SetCellValue("Sheet1", "A1", "fruit")
	f.SetCellValue("Sheet1", "A2", "apple")

	// Create a second sheet.
	_, err := f.NewSheet("Summary")
	if err != nil {
		t.Fatalf("NewSheet: %v", err)
	}
	f.SetCellValue("Summary", "A1", "total")
	f.SetCellValue("Summary", "A2", "1")

	path := filepath.Join(t.TempDir(), "multi.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}

	p := &XLSXParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath:  path,
		FileName:  "multi.xlsx",
		MimeType:  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		ChunkSize: 512,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text

	// Both sheet headings must be present.
	if !strings.Contains(text, "### Sheet: Sheet1") {
		t.Error("expected '### Sheet: Sheet1' heading")
	}
	if !strings.Contains(text, "### Sheet: Summary") {
		t.Error("expected '### Sheet: Summary' heading")
	}
	// Content from both sheets must be present.
	for _, val := range []string{"fruit", "apple", "total", "1"} {
		if !strings.Contains(text, val) {
			t.Errorf("expected value %q in output", val)
		}
	}
}

func TestXLSXParserHeaderOnly(t *testing.T) {
	t.Parallel()
	path := createTestXLSX(t, []string{"col1", "col2", "col3"}, nil)

	p := &XLSXParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "headers.xlsx",
		MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should include heading, header row, and separator.
	if !strings.Contains(result.Text, "### Sheet:") {
		t.Error("expected sheet heading in output")
	}
	if !strings.Contains(result.Text, "col1") {
		t.Error("expected header columns in output")
	}
	if !strings.Contains(result.Text, "| --- |") {
		t.Error("expected separator row in output")
	}
}
