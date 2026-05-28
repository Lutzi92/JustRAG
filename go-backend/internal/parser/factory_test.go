package parser

import (
	"context"
	"testing"
)

func TestFactorySelectsCSVParser(t *testing.T) {
	t.Parallel()
	f := DefaultFactory(nil)

	t.Run("by MIME type", func(t *testing.T) {
		t.Parallel()
		p := f.GetParser("text/csv", "data.csv")
		if p == nil {
			t.Fatal("expected a parser, got nil")
		}
		if p.Name() != "CSVParser" {
			t.Fatalf("expected CSVParser, got %s", p.Name())
		}
	})

	t.Run("by extension only", func(t *testing.T) {
		t.Parallel()
		p := f.GetParser("application/octet-stream", "report.csv")
		if p == nil {
			t.Fatal("expected a parser, got nil")
		}
		if p.Name() != "CSVParser" {
			t.Fatalf("expected CSVParser, got %s", p.Name())
		}
	})
}

func TestFactorySelectsTextParserAsCatchAll(t *testing.T) {
	t.Parallel()
	f := DefaultFactory(nil)

	t.Run("unknown MIME and extension", func(t *testing.T) {
		t.Parallel()
		p := f.GetParser("application/x-unknown", "file.xyz")
		if p == nil {
			t.Fatal("expected a parser, got nil")
		}
		if p.Name() != "TextParser" {
			t.Fatalf("expected TextParser, got %s", p.Name())
		}
	})

	t.Run("plain text MIME", func(t *testing.T) {
		t.Parallel()
		p := f.GetParser("text/plain", "readme.txt")
		if p == nil {
			t.Fatal("expected a parser, got nil")
		}
		if p.Name() != "TextParser" {
			t.Fatalf("expected TextParser, got %s", p.Name())
		}
	})
}

func TestFactoryReturnsNilWhenEmpty(t *testing.T) {
	t.Parallel()
	f := NewFactory()
	p := f.GetParser("text/plain", "file.txt")
	if p != nil {
		t.Fatalf("expected nil from empty factory, got %s", p.Name())
	}
}

type stubPDFParser struct{ name string }

func (s *stubPDFParser) Name() string               { return s.name }
func (s *stubPDFParser) CanParse(mt, _ string) bool { return mt == "application/pdf" }
func (s *stubPDFParser) Parse(_ context.Context, _ ParseContext) (*ParseResult, error) {
	return &ParseResult{Text: "stub"}, nil
}

func TestDefaultFactoryWith_DoclingWinsForPDF(t *testing.T) {
	t.Parallel()
	docling := &stubPDFParser{name: "docling-pdf"}
	f := DefaultFactoryWith(nil, docling)
	p := f.GetParser("application/pdf", "x.pdf")
	if p == nil || p.Name() != "docling-pdf" {
		t.Fatalf("expected docling-pdf to win, got %v", p)
	}
}

func TestDefaultFactoryWith_NilDoclingFallsBackToPDFParser(t *testing.T) {
	t.Parallel()
	f := DefaultFactoryWith(nil, nil)
	p := f.GetParser("application/pdf", "x.pdf")
	if p == nil {
		t.Fatal("expected a parser, got nil")
	}
	if p.Name() == "docling-pdf" {
		t.Errorf("did not expect docling-pdf when nil docling, got %s", p.Name())
	}
}

type stubDocxParser struct{ name string }

func (s *stubDocxParser) Name() string { return s.name }
func (s *stubDocxParser) CanParse(mt, _ string) bool {
	return mt == "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
}
func (s *stubDocxParser) Parse(_ context.Context, _ ParseContext) (*ParseResult, error) {
	return &ParseResult{Text: "stub"}, nil
}

func TestDefaultFactoryWith_MultipleFrontParsersFirstMatchWins(t *testing.T) {
	t.Parallel()
	pdf := &stubPDFParser{name: "front-pdf"}
	docx := &stubDocxParser{name: "front-docx"}
	f := DefaultFactoryWith(nil, pdf, docx)

	if p := f.GetParser("application/pdf", "x.pdf"); p == nil || p.Name() != "front-pdf" {
		t.Errorf("pdf routing: expected front-pdf, got %v", p)
	}
	if p := f.GetParser("application/vnd.openxmlformats-officedocument.wordprocessingml.document", "x.docx"); p == nil || p.Name() != "front-docx" {
		t.Errorf("docx routing: expected front-docx, got %v", p)
	}
}
