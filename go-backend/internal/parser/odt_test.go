package parser

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// createTestOdt builds a minimal valid ODT file in a temp directory and returns
// its path.  Each element of paragraphs becomes a <text:p> element in
// content.xml.
func createTestOdt(t *testing.T, paragraphs []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.odt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	fw, err := w.Create("content.xml")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(fw, `<?xml version="1.0"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">
<office:body><office:text>`)
	for _, p := range paragraphs {
		fmt.Fprintf(fw, `<text:p>%s</text:p>`, p)
	}
	fmt.Fprint(fw, `</office:text></office:body></office:document-content>`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOdtParserSingleParagraph(t *testing.T) {
	t.Parallel()
	path := createTestOdt(t, []string{"Hello World"})
	p := &OdtParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.odt",
		MimeType: odtMimeType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Hello World"
	if result.Text != want {
		t.Errorf("got %q, want %q", result.Text, want)
	}
}

func TestOdtParserMultipleParagraphs(t *testing.T) {
	t.Parallel()
	path := createTestOdt(t, []string{"First paragraph", "Second paragraph", "Third paragraph"})
	p := &OdtParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.odt",
		MimeType: odtMimeType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "First paragraph\nSecond paragraph\nThird paragraph"
	if result.Text != want {
		t.Errorf("got %q, want %q", result.Text, want)
	}
}

func TestOdtParserCanParse(t *testing.T) {
	t.Parallel()
	p := &OdtParser{}
	cases := []struct {
		mime     string
		fileName string
		want     bool
	}{
		{odtMimeType, "document.odt", true},
		{odtMimeType, "document.txt", true},            // MIME type alone is sufficient
		{"application/octet-stream", "file.odt", true}, // extension alone is sufficient
		{"application/octet-stream", "file.ODT", true}, // case-insensitive extension
		{"application/pdf", "report.pdf", false},
		{"", "notes.docx", false},
	}
	for _, tc := range cases {
		got := p.CanParse(tc.mime, tc.fileName)
		if got != tc.want {
			t.Errorf("CanParse(%q, %q) = %v, want %v", tc.mime, tc.fileName, got, tc.want)
		}
	}
}

func TestOdtParserSpanText(t *testing.T) {
	// A paragraph containing a <text:span> child element.
	t.Parallel()
	paragraphs := []string{
		`Hello <text:span xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">World</text:span>`,
	}
	path := createTestOdt(t, paragraphs)
	p := &OdtParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.odt",
		MimeType: odtMimeType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Hello World"
	if result.Text != want {
		t.Errorf("got %q, want %q", result.Text, want)
	}
}

func TestOdtParserMissingContentXml(t *testing.T) {
	// Create a ZIP that contains no content.xml.
	t.Parallel()
	path := filepath.Join(t.TempDir(), "empty.odt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	_, _ = w.Create("mimetype") // unrelated entry
	_ = w.Close()
	_ = f.Close()

	p := &OdtParser{}
	_, err = p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "empty.odt",
		MimeType: odtMimeType,
	})
	if err == nil {
		t.Fatal("expected error for missing content.xml, got nil")
	}
}

func TestOdtParserName(t *testing.T) {
	t.Parallel()
	p := &OdtParser{}
	if p.Name() != "OdtParser" {
		t.Errorf("Name() = %q, want %q", p.Name(), "OdtParser")
	}
}
