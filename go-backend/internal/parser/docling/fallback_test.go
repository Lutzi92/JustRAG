package docling

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/parser"
)

type stubParser struct {
	name      string
	can       bool
	parseRes  *parser.ParseResult
	parseErr  error
	parseHits int
}

func (s *stubParser) Name() string              { return s.name }
func (s *stubParser) CanParse(_, _ string) bool { return s.can }
func (s *stubParser) Parse(_ context.Context, _ parser.ParseContext) (*parser.ParseResult, error) {
	s.parseHits++
	return s.parseRes, s.parseErr
}

func TestFallbackParser_PrimarySuccess_DoesNotCallFallback(t *testing.T) {
	primary := &stubParser{parseRes: &parser.ParseResult{Text: "primary"}}
	fb := &stubParser{parseRes: &parser.ParseResult{Text: "fallback"}}
	p := &FallbackParser{Primary: primary, Fallback: fb}

	res, err := p.Parse(context.Background(), parser.ParseContext{FileName: "x.pdf"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.Text != "primary" {
		t.Errorf("expected primary text, got %q", res.Text)
	}
	if fb.parseHits != 0 {
		t.Errorf("expected fallback NOT called, got %d hits", fb.parseHits)
	}
}

func TestFallbackParser_PrimaryError_DelegatesToFallback(t *testing.T) {
	primary := &stubParser{parseErr: errors.New("docling boom")}
	fb := &stubParser{parseRes: &parser.ParseResult{Text: "fallback"}}
	p := &FallbackParser{Primary: primary, Fallback: fb}

	res, err := p.Parse(context.Background(), parser.ParseContext{FileName: "x.pdf"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.Text != "fallback" {
		t.Errorf("expected fallback text, got %q", res.Text)
	}
	if fb.parseHits != 1 {
		t.Errorf("expected fallback called once, got %d hits", fb.parseHits)
	}
}

func TestFallbackParser_NilPrimary_UsesFallback(t *testing.T) {
	fb := &stubParser{parseRes: &parser.ParseResult{Text: "fallback"}}
	p := &FallbackParser{Primary: nil, Fallback: fb}

	res, err := p.Parse(context.Background(), parser.ParseContext{FileName: "x.pdf"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.Text != "fallback" {
		t.Errorf("expected fallback text, got %q", res.Text)
	}
}

func TestFallbackParser_BothErrorIsError(t *testing.T) {
	primary := &stubParser{parseErr: errors.New("boom1")}
	fb := &stubParser{parseErr: errors.New("boom2")}
	p := &FallbackParser{Primary: primary, Fallback: fb}

	_, err := p.Parse(context.Background(), parser.ParseContext{FileName: "x.pdf"})
	if err == nil {
		t.Fatal("expected error when both fail, got nil")
	}
}

func TestFallbackParser_CanParse_DelegatesToEither(t *testing.T) {
	p := &FallbackParser{Primary: &stubParser{can: true}, Fallback: &stubParser{can: false}}
	if !p.CanParse("application/pdf", "x.pdf") {
		t.Error("expected true when primary can parse")
	}

	p2 := &FallbackParser{Primary: &stubParser{can: false}, Fallback: &stubParser{can: true}}
	if !p2.CanParse("application/pdf", "x.pdf") {
		t.Error("expected true when fallback can parse")
	}
}
