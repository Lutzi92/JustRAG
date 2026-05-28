package docling

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestDoclingPDFParser_Parse_ReturnsPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content": "# T\n\nbody",
				"pages": []map[string]any{
					{"number": 1, "text": "page one"},
					{"number": 2, "text": "page two"},
				},
			},
			"errors": []any{},
		})
	}))
	defer srv.Close()

	tmp := t.TempDir()
	pdfPath := filepath.Join(tmp, "test.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4 stub"), 0o644); err != nil {
		t.Fatalf("write stub pdf: %v", err)
	}

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: pdfPath, FileName: "test.pdf", MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(res.Pages) != 2 {
		t.Errorf("expected 2 pages, got %d", len(res.Pages))
	}
	if !strings.Contains(res.Text, "T") || !strings.Contains(res.Text, "body") {
		t.Errorf("text should contain markdown body, got %q", res.Text)
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
	if gotURL != "/v1alpha/convert/file" {
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
