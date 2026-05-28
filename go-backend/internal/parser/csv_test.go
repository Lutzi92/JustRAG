package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCSV(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCSVParserCanParse(t *testing.T) {
	t.Parallel()
	p := &CSVParser{}
	cases := []struct {
		name     string
		mime     string
		fileName string
		want     bool
	}{
		{"text-csv-mime", "text/csv", "data.csv", true},
		{"octet-stream-with-csv-ext", "application/octet-stream", "report.csv", true},
		{"plain-text-rejected", "text/plain", "file.txt", false},
		{"json-rejected", "application/json", "data.json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := p.CanParse(tc.mime, tc.fileName)
			if got != tc.want {
				t.Errorf("CanParse(%q, %q) = %v, want %v", tc.mime, tc.fileName, got, tc.want)
			}
		})
	}
}

func TestCSVParserConvertsToMarkdownTable(t *testing.T) {
	dir := t.TempDir()
	csv := "name,age,city\nAlice,30,Berlin\nBob,25,Munich\n"
	path := writeCSV(t, dir, "people.csv", csv)

	p := &CSVParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath:  path,
		FileName:  "people.csv",
		MimeType:  "text/csv",
		ChunkSize: 512,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text

	// Header separator row must be present.
	if !strings.Contains(text, "| --- |") {
		t.Error("expected markdown header separator, not found")
	}
	// Column headers must appear.
	for _, col := range []string{"name", "age", "city"} {
		if !strings.Contains(text, col) {
			t.Errorf("expected column %q in output", col)
		}
	}
	// Data must appear.
	for _, val := range []string{"Alice", "30", "Berlin", "Bob", "25", "Munich"} {
		if !strings.Contains(text, val) {
			t.Errorf("expected value %q in output", val)
		}
	}
}

func TestCSVParserChunksLargeTableWithRepeatedHeaders(t *testing.T) {
	dir := t.TempDir()

	// Build a CSV with 3 columns and enough rows to force multiple chunks when
	// chunkSize is small (32 tokens → budget ~25 tokens).
	var sb strings.Builder
	sb.WriteString("id,value,label\n")
	for i := 0; i < 100; i++ {
		sb.WriteString(fmt.Sprintf("%d,val%d,label%d\n", i, i, i))
	}
	path := writeCSV(t, dir, "large.csv", sb.String())

	p := &CSVParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath:  path,
		FileName:  "large.csv",
		MimeType:  "text/csv",
		ChunkSize: 32, // very small → forces many chunks
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The output should contain multiple chunks separated by blank lines.
	chunks := strings.Split(result.Text, "\n\n")
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	// Every chunk must start with the header row.
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, "| id |") {
			t.Errorf("chunk %d does not start with header row: %q", i, chunk[:min(len(chunk), 60)])
		}
		if !strings.Contains(chunk, "| --- |") {
			t.Errorf("chunk %d missing header separator", i)
		}
	}
}

func TestCSVParserEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeCSV(t, dir, "empty.csv", "")

	p := &CSVParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "empty.csv",
		MimeType: "text/csv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "" {
		t.Fatalf("expected empty text for empty CSV, got %q", result.Text)
	}
}

func TestCSVParserHeaderOnlyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeCSV(t, dir, "headers.csv", "col1,col2,col3\n")

	p := &CSVParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "headers.csv",
		MimeType: "text/csv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should include header and separator but no data rows.
	if !strings.Contains(result.Text, "col1") {
		t.Error("expected header columns in output")
	}
	if !strings.Contains(result.Text, "| --- |") {
		t.Error("expected separator row in output")
	}
}

// min is provided for Go versions before 1.21 that lack the builtin.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
