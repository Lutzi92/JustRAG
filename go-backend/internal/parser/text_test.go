package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextParserReadsFileContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "Hello, world!\nThis is a test file.\n"
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &TextParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.txt",
		MimeType: "text/plain",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != content {
		t.Fatalf("expected %q, got %q", content, result.Text)
	}
}

func TestTextParserTruncatesAtMaxChars(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Write a file that is TextMaxChars + 100 bytes long.
	data := strings.Repeat("a", TextMaxChars+100)
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &TextParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "large.txt",
		MimeType: "text/plain",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Text) != TextMaxChars {
		t.Fatalf("expected truncated length %d, got %d", TextMaxChars, len(result.Text))
	}
}

func TestTextParserReturnsErrorForMissingFile(t *testing.T) {
	t.Parallel()
	p := &TextParser{}
	_, err := p.Parse(context.Background(), ParseContext{
		FilePath: "/nonexistent/path/file.txt",
		FileName: "file.txt",
		MimeType: "text/plain",
	})
	if err == nil {
		t.Fatal("expected an error for missing file, got nil")
	}
}

func TestTextParserCanParseVariousTypes(t *testing.T) {
	t.Parallel()
	p := &TextParser{}
	cases := []struct {
		mime     string
		fileName string
		want     bool
	}{
		{"text/plain", "file.txt", true},
		{"text/markdown", "readme.md", true},
		{"application/octet-stream", "script.sh", true},
		{"application/x-unknown", "file.xyz", true}, // catch-all
		{"", "notes.yaml", true},
	}
	for _, tc := range cases {
		got := p.CanParse(tc.mime, tc.fileName)
		if got != tc.want {
			t.Errorf("CanParse(%q, %q) = %v, want %v", tc.mime, tc.fileName, got, tc.want)
		}
	}
}
