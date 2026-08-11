package docling

import (
	"context"
	"fmt"
	"os"
	"strings"
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
// API returns — and that is exactly how two page-number bugs shipped. First a
// mock served a `document.pages` array docling-serve has never emitted, so
// every PDF was labelled page 1. Then page boundaries were recovered by
// searching item text inside md_content, which drifts on any real document
// (docling escapes `max_value` to `max\_value`, drops furniture, re-renders
// tables), producing confidently wrong pages. Both passed the unit suite.
//
// testdata/report10.pdf is a 10-page document with a per-page marker, a
// running header and footer, repeated boilerplate and escaped characters —
// the exact features that broke text anchoring.
func TestIntegration_RealSidecar_RecoversPageNumbers(t *testing.T) {
	baseURL := os.Getenv("DOCLING_TEST_URL")
	if baseURL == "" {
		t.Skip("set DOCLING_TEST_URL to run against a live docling-serve")
	}

	p := &DoclingPDFParser{Client: NewClient(baseURL, 180*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: "testdata/report10.pdf",
		FileName: "report10.pdf",
		MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("parse against live sidecar: %v", err)
	}
	if len(res.Pages) != 10 {
		t.Fatalf("expected 10 pages, got %d: %+v", len(res.Pages), res.Pages)
	}

	for i, pg := range res.Pages {
		if pg.PageNumber != i+1 {
			t.Errorf("page %d out of order: got page number %d", i, pg.PageNumber)
		}
		// Every page carries two markers unique to it. Both must land on it,
		// and no other page's marker may.
		for _, marker := range []string{
			fmt.Sprintf("UNIQUEMARK%d\n", pg.PageNumber),
			fmt.Sprintf("PAGEBODY%d.", pg.PageNumber),
		} {
			if !strings.Contains(pg.Text, marker) {
				t.Errorf("page %d is missing its own marker %q; text=%q", pg.PageNumber, marker, pg.Text)
			}
		}
		for n := 1; n <= 10; n++ {
			if n == pg.PageNumber {
				continue
			}
			// Trailing period, or "PAGEBODY1" would match inside "PAGEBODY10".
			if strings.Contains(pg.Text, fmt.Sprintf("PAGEBODY%d.", n)) {
				t.Errorf("page %d contains page %d's content; text=%q", pg.PageNumber, n, pg.Text)
			}
		}
		// Running header/footer are furniture: docling keeps them out of its
		// markdown and so do we, or every page's chunks carry the boilerplate.
		if strings.Contains(pg.Text, "Vertraulich") || strings.Contains(pg.Text, "von 10") {
			t.Errorf("page %d ingested running header/footer: %q", pg.PageNumber, pg.Text)
		}
	}
}

// TestIntegration_RealSidecar_KeepsTables guards the other half of building
// page text ourselves: tables are re-rendered from the DoclingDocument cell
// grid rather than taken from docling's markdown, so a real detected table has
// to survive that round trip. Losing table content would quietly gut the main
// reason for running Docling at all.
func TestIntegration_RealSidecar_KeepsTables(t *testing.T) {
	baseURL := os.Getenv("DOCLING_TEST_URL")
	if baseURL == "" {
		t.Skip("set DOCLING_TEST_URL to run against a live docling-serve")
	}

	p := &DoclingPDFParser{Client: NewClient(baseURL, 180*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: "testdata/table.pdf",
		FileName: "table.pdf",
		MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("parse against live sidecar: %v", err)
	}
	if len(res.Pages) == 0 {
		t.Fatal("expected at least one page")
	}

	text := res.Pages[0].Text
	for _, want := range []string{
		"| Produkt | Version | Status |", // header row
		"| --- |",                        // separator, or it is not a markdown table
		"| Alpha | 1.0 | aktiv |",
		"| Beta | 2.1 | geplant |",
		"TABMARK1", // surrounding prose survives too
	} {
		if !strings.Contains(text, want) {
			t.Errorf("page text missing %q; got:\n%s", want, text)
		}
	}
}
