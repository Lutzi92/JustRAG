package parser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// minimalPDF is a minimal valid single-page PDF that renders "Hello World".
// The xref offsets are computed for this exact byte sequence.
const minimalPDF = "%PDF-1.0\n" +
	"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/MediaBox[0 0 612 792]/Parent 2 0 R/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n" +
	"4 0 obj<</Length 44>>stream\n" +
	"BT /F1 12 Tf 100 700 Td (Hello World) Tj ET\n" +
	"endstream endobj\n" +
	"5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\n" +
	"xref\n" +
	"0 6\n" +
	"0000000000 65535 f \n" +
	"0000000009 00000 n \n" +
	"0000000058 00000 n \n" +
	"0000000115 00000 n \n" +
	"0000000266 00000 n \n" +
	"0000000360 00000 n \n" +
	"trailer<</Size 6/Root 1 0 R>>\n" +
	"startxref\n" +
	"434\n" +
	"%%EOF"

func TestPDFParserCanParseByMIMEType(t *testing.T) {
	t.Parallel()
	p := &PDFParser{}
	if !p.CanParse("application/pdf", "document.pdf") {
		t.Error("CanParse should return true for application/pdf MIME type")
	}
}

func TestPDFParserCanParseByExtension(t *testing.T) {
	t.Parallel()
	p := &PDFParser{}
	if !p.CanParse("application/octet-stream", "document.pdf") {
		t.Error("CanParse should return true for .pdf extension")
	}
}

func TestPDFParserCannotParseNonPDF(t *testing.T) {
	t.Parallel()
	p := &PDFParser{}
	cases := []struct {
		mime     string
		fileName string
	}{
		{"text/plain", "document.txt"},
		{"application/msword", "document.doc"},
		{"image/png", "image.png"},
		{"application/octet-stream", "archive.zip"},
	}
	for _, tc := range cases {
		if p.CanParse(tc.mime, tc.fileName) {
			t.Errorf("CanParse(%q, %q) should return false", tc.mime, tc.fileName)
		}
	}
}

func TestPDFParserParse(t *testing.T) {
	// Skip if pdftotext is not installed on this machine.
	t.Parallel()
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not available; skipping PDF parse test")
	}

	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "test.pdf")
	if err := os.WriteFile(pdfPath, []byte(minimalPDF), 0o644); err != nil {
		t.Fatalf("failed to write test PDF: %v", err)
	}

	p := &PDFParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: pdfPath,
		FileName: "test.pdf",
		MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a non-nil ParseResult")
	}
	if !strings.Contains(result.Text, "Hello World") {
		t.Errorf("expected output to contain %q, got: %q", "Hello World", result.Text)
	}
}

func TestPDFParserReturnsErrorWhenFileNotFound(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not available; skipping test")
	}

	p := &PDFParser{}
	_, err := p.Parse(context.Background(), ParseContext{
		FilePath: "/nonexistent/path/missing.pdf",
		FileName: "missing.pdf",
		MimeType: "application/pdf",
	})
	if err == nil {
		t.Fatal("expected an error for missing file, got nil")
	}
}

func TestIsSparseText_Sparse(t *testing.T) {
	// Pages with very little text (avg < 50 chars) should be detected as sparse.
	t.Parallel()
	pages := []PageText{
		{PageNumber: 1, Text: "Hello"},
		{PageNumber: 2, Text: "World"},
		{PageNumber: 3, Text: "Page three text here"},
	}
	if !isSparseText(pages, 50) {
		t.Error("expected isSparseText to return true for sparse pages")
	}
}

func TestIsSparseText_Dense(t *testing.T) {
	// Pages with substantial text should not be detected as sparse.
	t.Parallel()
	longText := "Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam."
	pages := []PageText{
		{PageNumber: 1, Text: longText},
		{PageNumber: 2, Text: longText},
	}
	if isSparseText(pages, 50) {
		t.Error("expected isSparseText to return false for dense pages")
	}
}

func TestIsSparseText_Empty(t *testing.T) {
	// No pages at all should count as sparse.
	t.Parallel()
	if !isSparseText(nil, 50) {
		t.Error("expected isSparseText to return true for nil pages")
	}
}

func TestRunOCR_NonexistentBinary(t *testing.T) {
	// runOCR should return a clear error when tesseract binary doesn't exist.
	t.Parallel()
	_, err := runOCR(context.Background(), filepath.Join(os.TempDir(), "nonexistent.pdf"), "nonexistent-tesseract-binary-xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent tesseract binary")
	}
	t.Logf("got expected error: %v", err)
}
