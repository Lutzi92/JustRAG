package docling

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/parser"
)

func TestFallbackParser_DegradedOnlyWhenFallbackUsed(t *testing.T) {
	ctx := context.Background()
	failing := &FallbackParser{
		Primary:  &stubParser{parseErr: errors.New("boom")},
		Fallback: &stubParser{parseRes: &parser.ParseResult{Text: "fb"}},
	}
	res, err := failing.Parse(ctx, parser.ParseContext{FileName: "x.pdf"})
	if err != nil || !res.Degraded {
		t.Fatalf("fallback result must be Degraded: res=%+v err=%v", res, err)
	}
	ok := &FallbackParser{
		Primary:  &stubParser{parseRes: &parser.ParseResult{Text: "p"}},
		Fallback: &stubParser{parseRes: &parser.ParseResult{Text: "fb"}},
	}
	res, err = ok.Parse(ctx, parser.ParseContext{FileName: "x.pdf"})
	if err != nil || res.Degraded {
		t.Fatalf("primary result must not be Degraded: res=%+v err=%v", res, err)
	}
}
