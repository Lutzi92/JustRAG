package docling

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/parser"
)

// TestIntegration_RealSidecar_RecoversPageNumbers runs against a live
// docling-serve instance:
//
//	DOCLING_TEST_URL=http://localhost:5001 go test ./internal/parser/docling -run Integration -v
//
// It exists because the unit tests can only assert what our mocks claim the
// API returns — and that is exactly how the "every PDF citation says page 1"
// bug survived: the mock served a `document.pages` array docling-serve has
// never emitted, so the fabricated-page-1 fallback ran in production while the
// suite stayed green. This test pins the response contract to the real thing.
func TestIntegration_RealSidecar_RecoversPageNumbers(t *testing.T) {
	baseURL := os.Getenv("DOCLING_TEST_URL")
	if baseURL == "" {
		t.Skip("set DOCLING_TEST_URL to run against a live docling-serve")
	}

	p := &DoclingPDFParser{Client: NewClient(baseURL, 120*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: "testdata/three-pages.pdf",
		FileName: "three-pages.pdf",
		MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("parse against live sidecar: %v", err)
	}
	if len(res.PageSpans) < 3 {
		t.Fatalf("expected spans for 3 pages, got %+v (text=%q)", res.PageSpans, res.Text)
	}

	// Each page carries a unique marker; the marker must resolve to its page.
	for _, tc := range []struct {
		marker string
		want   int
	}{
		{"ALPHA marker on page one", 1},
		{"BRAVO marker on page two", 2},
		{"CHARLIE marker on page three", 3},
	} {
		pages, _ := parser.PagesForChunk(res.PageSpans, res.Text, tc.marker, 0)
		if len(pages) != 1 || pages[0] != tc.want {
			t.Errorf("%q: expected page %d, got %v", tc.marker, tc.want, pages)
		}
	}
}
