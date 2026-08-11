package docling

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/parser"
)

func TestDoclingPDFParser_CanParse(t *testing.T) {
	p := &DoclingPDFParser{}
	if !p.CanParse("application/pdf", "x.pdf") {
		t.Error("should match application/pdf")
	}
	if !p.CanParse("", "report.PDF") {
		t.Error("should match .pdf extension case-insensitively")
	}
	if p.CanParse("text/plain", "x.txt") {
		t.Error("should not match text/plain")
	}
}

// serveDocling starts a stub docling-serve returning the given markdown and
// (optionally) a DoclingDocument json_content built from page-tagged items.
func serveDocling(t *testing.T, markdown string, items []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		doc := map[string]any{"md_content": markdown}
		if items != nil {
			doc["json_content"] = doclingJSONContent(items)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"document": doc, "errors": []any{}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func stubPDF(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4 stub"), 0o644); err != nil {
		t.Fatalf("write stub pdf: %v", err)
	}
	return path
}

func TestDoclingPDFParser_Parse_MapsPageSpansOntoMarkdown(t *testing.T) {
	md := "# Title\n\nfirst page body\n\n## Second\n\nsecond page body\n\nthird page body"
	srv := serveDocling(t, md, []map[string]any{
		{"text": "Title", "page": 1},
		{"text": "first page body", "page": 1},
		{"text": "Second", "page": 2},
		{"text": "second page body", "page": 2},
		{"text": "third page body", "page": 3},
	})

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: stubPDF(t), FileName: "test.pdf", MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(res.Pages) != 0 {
		t.Errorf("expected no pre-split pages, got %d", len(res.Pages))
	}
	want := []parser.PageSpan{
		{Start: 0, Page: 1},
		{Start: strings.Index(md, "Second"), Page: 2},
		{Start: strings.Index(md, "third page body"), Page: 3},
	}
	if !reflect.DeepEqual(res.PageSpans, want) {
		t.Fatalf("spans mismatch:\n got %+v\nwant %+v", res.PageSpans, want)
	}
	// The regression this guards: content on page 3 must not resolve to page 1.
	pages, _ := parser.PagesForChunk(res.PageSpans, res.Text, "third page body", 0)
	if len(pages) != 1 || pages[0] != 3 {
		t.Errorf("expected page 3 for third-page content, got %v", pages)
	}
	if !strings.Contains(res.Text, "Title") || !strings.Contains(res.Text, "second page body") {
		t.Errorf("text should contain markdown body, got %q", res.Text)
	}
}

func TestDoclingPDFParser_Parse_SkipsUnmatchableAnchors(t *testing.T) {
	// The page-2 heading is escaped in the markdown and cannot be matched;
	// the next page-2 item must anchor the page instead.
	md := "one\n\n\\*escaped heading\\*\n\ntwo body"
	srv := serveDocling(t, md, []map[string]any{
		{"text": "one", "page": 1},
		{"text": "*escaped heading*", "page": 2},
		{"text": "two body", "page": 2},
	})

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: stubPDF(t), FileName: "test.pdf", MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := []parser.PageSpan{{Start: 0, Page: 1}, {Start: strings.Index(md, "two body"), Page: 2}}
	if !reflect.DeepEqual(res.PageSpans, want) {
		t.Errorf("spans mismatch:\n got %+v\nwant %+v", res.PageSpans, want)
	}
}

func TestDoclingPDFParser_Parse_NoProvenanceYieldsNoPages(t *testing.T) {
	// Fallback path (option 2): without provenance we report no pages at all
	// rather than stamping everything as page 1.
	srv := serveDocling(t, "# T\n\nbody", nil)

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: stubPDF(t), FileName: "test.pdf", MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(res.Pages) != 0 || len(res.PageSpans) != 0 {
		t.Errorf("expected no page info, got Pages=%+v PageSpans=%+v", res.Pages, res.PageSpans)
	}
	if !strings.Contains(res.Text, "body") {
		t.Errorf("content must still be ingested, got %q", res.Text)
	}
}

func TestDoclingPDFParser_Parse_ReturnsErrorOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	pdfPath := filepath.Join(tmp, "test.pdf")
	_ = os.WriteFile(pdfPath, []byte("x"), 0o644)

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	_, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: pdfPath, FileName: "test.pdf", MimeType: "application/pdf",
	})
	if err == nil {
		t.Fatal("expected error from upstream 502, got nil")
	}
}

func TestDoclingPDFParser_Parse_ErrorWhenClientNil(t *testing.T) {
	p := &DoclingPDFParser{}
	_, err := p.Parse(context.Background(), parser.ParseContext{FileName: "x.pdf"})
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
}

func TestDoclingPDFParser_Parse_SetsIsMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{"md_content": "# T\n\nbody"},
			"errors":   []any{},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	pdfPath := filepath.Join(tmp, "test.pdf")
	_ = os.WriteFile(pdfPath, []byte("x"), 0o644)

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: pdfPath, FileName: "test.pdf", MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsMarkdown {
		t.Error("expected IsMarkdown=true for Docling output")
	}
}

func TestDoclingDocxParser_CanParse(t *testing.T) {
	p := &DoclingDocxParser{}
	if !p.CanParse("application/vnd.openxmlformats-officedocument.wordprocessingml.document", "x.docx") {
		t.Error("should match DOCX MIME")
	}
	if !p.CanParse("", "report.DOCX") {
		t.Error("should match .docx extension case-insensitively")
	}
	if p.CanParse("application/pdf", "x.pdf") {
		t.Error("should not match PDFs")
	}
	if p.CanParse("text/plain", "x.txt") {
		t.Error("should not match text/plain")
	}
}

func TestDoclingDocxParser_Parse_CallsSameEndpointAsPDF(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content": "# Heading\n\n| col1 | col2 |\n|------|------|\n| a    | b    |\n",
			},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.docx")
	_ = os.WriteFile(path, []byte("PK-docx-stub"), 0o644)

	p := &DoclingDocxParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: path, FileName: "test.docx",
		MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(res.Text, "| col1 |") {
		t.Errorf("expected markdown table preserved, got %q", res.Text)
	}
	if !res.IsMarkdown {
		t.Error("expected IsMarkdown=true")
	}
	if gotURL != "/v1/convert/file" {
		t.Errorf("expected same Docling endpoint, got %q", gotURL)
	}
}

func TestDoclingDocxParser_Parse_ErrorWhenClientNil(t *testing.T) {
	p := &DoclingDocxParser{}
	_, err := p.Parse(context.Background(), parser.ParseContext{FileName: "x.docx"})
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
}

func TestDoclingPptxParser_CanParse(t *testing.T) {
	p := &DoclingPptxParser{}
	if !p.CanParse("application/vnd.openxmlformats-officedocument.presentationml.presentation", "x.pptx") {
		t.Error("should match PPTX MIME")
	}
	if !p.CanParse("", "deck.PPTX") {
		t.Error("should match .pptx extension case-insensitively")
	}
	if p.CanParse("application/pdf", "x.pdf") {
		t.Error("should not match PDFs")
	}
	if p.CanParse("application/vnd.openxmlformats-officedocument.wordprocessingml.document", "x.docx") {
		t.Error("should not match DOCX")
	}
}

func TestDoclingPptxParser_Parse_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content": "# Slide 1\n\nBullet point.\n\n# Slide 2\n\nMore content.",
			},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "deck.pptx")
	_ = os.WriteFile(path, []byte("PK-pptx-stub"), 0o644)

	p := &DoclingPptxParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: path, FileName: "deck.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(res.Text, "Slide 1") {
		t.Errorf("expected slide headings, got %q", res.Text)
	}
	if !res.IsMarkdown {
		t.Error("expected IsMarkdown=true")
	}
}

func TestDoclingPptxParser_Parse_ErrorWhenClientNil(t *testing.T) {
	p := &DoclingPptxParser{}
	_, err := p.Parse(context.Background(), parser.ParseContext{FileName: "x.pptx"})
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
}
