package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// createTestEpub builds a minimal in-memory EPUB (ZIP) containing:
//   - META-INF/container.xml pointing to content.opf
//   - content.opf with a manifest + spine referencing the provided chapters
//   - One XHTML file per chapter (chapter1.xhtml, chapter2.xhtml, …)
//
// It writes the ZIP to a temp file and returns its path.
func createTestEpub(t *testing.T, chapters []string) string {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// META-INF/container.xml
	containerXML := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`
	addZipFile(t, w, "META-INF/container.xml", containerXML)

	// Build manifest items and spine itemrefs.
	var manifestItems strings.Builder
	var spineItems strings.Builder
	for i, ch := range chapters {
		id := strings.ToLower(chapterID(i))
		href := chapterID(i) + ".xhtml"
		manifestItems.WriteString("    <item id=\"" + id + "\" href=\"" + href + "\" media-type=\"application/xhtml+xml\"/>\n")
		spineItems.WriteString("    <itemref idref=\"" + id + "\"/>\n")

		// Write the chapter XHTML file.
		xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>Chapter</title></head>
<body>` + ch + `</body>
</html>`
		addZipFile(t, w, "OEBPS/"+href, xhtml)
	}

	// content.opf
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest>
` + manifestItems.String() + `  </manifest>
  <spine>
` + spineItems.String() + `  </spine>
</package>`
	addZipFile(t, w, "OEBPS/content.opf", opf)

	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}

	// Write the buffer to a temp file.
	dir := t.TempDir()
	path := dir + "/test.epub"
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write epub temp file: %v", err)
	}
	return path
}

// chapterID returns a stable chapter identifier like "chapter1", "chapter2".
func chapterID(i int) string {
	return fmt.Sprintf("chapter%d", i+1)
}

func addZipFile(t *testing.T, w *zip.Writer, name, content string) {
	t.Helper()
	f, err := w.Create(name)
	if err != nil {
		t.Fatalf("zip create %q: %v", name, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("zip write %q: %v", name, err)
	}
}

// ---- Tests ---------------------------------------------------------------

func TestEpubParserCanParse(t *testing.T) {
	t.Parallel()
	p := &EpubParser{}
	cases := []struct {
		mime     string
		fileName string
		want     bool
	}{
		{"application/epub+zip", "book.epub", true},
		{"application/epub+zip", "book.pdf", true}, // mime wins
		{"application/octet-stream", "book.epub", true},
		{"application/octet-stream", "book.pdf", false},
		{"text/plain", "file.txt", false},
	}
	for _, tc := range cases {
		got := p.CanParse(tc.mime, tc.fileName)
		if got != tc.want {
			t.Errorf("CanParse(%q, %q) = %v, want %v", tc.mime, tc.fileName, got, tc.want)
		}
	}
}

func TestEpubParserSingleChapter(t *testing.T) {
	t.Parallel()
	chapter := `<p>Hello, world!</p>`
	path := createTestEpub(t, []string{chapter})

	p := &EpubParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.epub",
		MimeType: "application/epub+zip",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, "Hello, world!") {
		t.Errorf("expected text to contain %q, got: %q", "Hello, world!", result.Text)
	}
}

func TestEpubParserHTMLTagsStripped(t *testing.T) {
	t.Parallel()
	chapter := `<h1>Title</h1><p>First paragraph.</p><div>Second block.</div>`
	path := createTestEpub(t, []string{chapter})

	p := &EpubParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.epub",
		MimeType: "application/epub+zip",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text
	// Tags must not appear in the output.
	for _, tag := range []string{"<h1>", "</h1>", "<p>", "</p>", "<div>", "</div>"} {
		if strings.Contains(text, tag) {
			t.Errorf("output still contains tag %q: %q", tag, text)
		}
	}
	// Text content must be present.
	for _, want := range []string{"Title", "First paragraph.", "Second block."} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in output, got: %q", want, text)
		}
	}
}

func TestEpubParserMultipleChapters(t *testing.T) {
	t.Parallel()
	chapters := []string{
		`<p>Chapter one content.</p>`,
		`<p>Chapter two content.</p>`,
	}
	path := createTestEpub(t, chapters)

	p := &EpubParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.epub",
		MimeType: "application/epub+zip",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text
	for _, want := range []string{"Chapter one content.", "Chapter two content."} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in output", want)
		}
	}
	// Chapters must be separated by a blank line.
	if !strings.Contains(text, "\n\n") {
		t.Error("expected double newline between chapters")
	}
}

func TestEpubParserScriptAndStyleSkipped(t *testing.T) {
	t.Parallel()
	chapter := `<style>body { color: red; }</style><p>Visible text.</p><script>alert("hi")</script>`
	path := createTestEpub(t, []string{chapter})

	p := &EpubParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.epub",
		MimeType: "application/epub+zip",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text
	if strings.Contains(text, "color: red") {
		t.Error("CSS content should be stripped, found in output")
	}
	if strings.Contains(text, `alert("hi")`) {
		t.Error("JS content should be stripped, found in output")
	}
	if !strings.Contains(text, "Visible text.") {
		t.Errorf("expected %q in output", "Visible text.")
	}
}

func TestEpubParserBlockElementsProduceNewlines(t *testing.T) {
	t.Parallel()
	chapter := `<h1>Heading</h1><p>Para one.</p><p>Para two.</p><li>Item</li>`
	path := createTestEpub(t, []string{chapter})

	p := &EpubParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.epub",
		MimeType: "application/epub+zip",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text
	// Each block element should produce a distinct line.
	for _, want := range []string{"Heading", "Para one.", "Para two.", "Item"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in output", want)
		}
	}
	// There must be at least one newline separating them.
	if !strings.Contains(text, "\n") {
		t.Error("expected newlines between block elements")
	}
}
