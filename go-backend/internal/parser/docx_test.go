package parser

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// createTestDocx creates a minimal DOCX file (a ZIP containing word/document.xml)
// whose body contains a single paragraph with the provided text.
func createTestDocx(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	fw, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(fw, `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:body>
</w:document>`, text)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// createTestDocxMultiParagraph creates a DOCX with multiple paragraphs.
func createTestDocxMultiParagraph(t *testing.T, paragraphs []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test_multi.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	fw, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}

	fmt.Fprint(fw, `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>`)
	for _, p := range paragraphs {
		fmt.Fprintf(fw, `<w:p><w:r><w:t>%s</w:t></w:r></w:p>`, p)
	}
	fmt.Fprint(fw, `</w:body></w:document>`)

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// createTestDocxWithRuns creates a DOCX whose single paragraph contains
// multiple <w:r><w:t> runs (to verify run concatenation within a paragraph).
func createTestDocxWithRuns(t *testing.T, runs []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test_runs.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	fw, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}

	fmt.Fprint(fw, `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body><w:p>`)
	for _, run := range runs {
		fmt.Fprintf(fw, `<w:r><w:t>%s</w:t></w:r>`, run)
	}
	fmt.Fprint(fw, `</w:p></w:body></w:document>`)

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// createTestDocxWithBreak creates a DOCX whose single paragraph contains a
// <w:br/> line break between two text runs.
func createTestDocxWithBreak(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test_break.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	fw, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(fw, `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>
  <w:p>
    <w:r><w:t>Line one</w:t></w:r>
    <w:r><w:br/><w:t>Line two</w:t></w:r>
  </w:p>
</w:body>
</w:document>`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDocxParserCanParse(t *testing.T) {
	t.Parallel()
	p := &DocxParser{}
	cases := []struct {
		mime     string
		fileName string
		want     bool
	}{
		{docxMimeType, "document.docx", true},
		{docxMimeType, "document.doc", true},            // MIME match is sufficient
		{"application/octet-stream", "file.docx", true}, // extension match
		{"application/octet-stream", "FILE.DOCX", true}, // case-insensitive extension
		{"application/pdf", "report.pdf", false},
		{"text/plain", "notes.txt", false},
		{"application/msword", "old.doc", false}, // .doc is not .docx
	}
	for _, tc := range cases {
		got := p.CanParse(tc.mime, tc.fileName)
		if got != tc.want {
			t.Errorf("CanParse(%q, %q) = %v, want %v", tc.mime, tc.fileName, got, tc.want)
		}
	}
}

func TestDocxParserName(t *testing.T) {
	t.Parallel()
	p := &DocxParser{}
	if p.Name() != "DocxParser" {
		t.Errorf("Name() = %q, want %q", p.Name(), "DocxParser")
	}
}

func TestDocxParserSingleParagraph(t *testing.T) {
	t.Parallel()
	p := &DocxParser{}
	path := createTestDocx(t, "Hello World")

	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.docx",
		MimeType: docxMimeType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "Hello World" {
		t.Errorf("Text = %q, want %q", result.Text, "Hello World")
	}
}

func TestDocxParserMultipleParagraphs(t *testing.T) {
	t.Parallel()
	p := &DocxParser{}
	path := createTestDocxMultiParagraph(t, []string{"First paragraph", "Second paragraph", "Third paragraph"})

	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.docx",
		MimeType: docxMimeType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "First paragraph\nSecond paragraph\nThird paragraph"
	if result.Text != want {
		t.Errorf("Text = %q, want %q", result.Text, want)
	}
}

func TestDocxParserMultipleRunsInParagraph(t *testing.T) {
	t.Parallel()
	p := &DocxParser{}
	// Simulate the example from the spec: "Hello " + "World" → "Hello World"
	path := createTestDocxWithRuns(t, []string{"Hello ", "World"})

	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.docx",
		MimeType: docxMimeType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "Hello World" {
		t.Errorf("Text = %q, want %q", result.Text, "Hello World")
	}
}

func TestDocxParserLineBreak(t *testing.T) {
	t.Parallel()
	p := &DocxParser{}
	path := createTestDocxWithBreak(t)

	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.docx",
		MimeType: docxMimeType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "Line one\nLine two"
	if result.Text != want {
		t.Errorf("Text = %q, want %q", result.Text, want)
	}
}

func TestDocxParserMissingDocumentXML(t *testing.T) {
	// Build a ZIP without word/document.xml.
	t.Parallel()
	path := filepath.Join(t.TempDir(), "empty.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	fw, err := w.Create("word/styles.xml")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(fw, `<w:styles/>`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	p := &DocxParser{}
	_, err = p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "empty.docx",
		MimeType: docxMimeType,
	})
	if err == nil {
		t.Fatal("expected error for missing word/document.xml, got nil")
	}
}

func TestDocxParserNotAZip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.docx")
	if err := os.WriteFile(path, []byte("this is not a zip file"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &DocxParser{}
	_, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "bad.docx",
		MimeType: docxMimeType,
	})
	if err == nil {
		t.Fatal("expected error for non-ZIP input, got nil")
	}
}
