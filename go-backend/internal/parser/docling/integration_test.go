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

// TestIntegration_RealSidecar_KeepsFigureCaptions guards the third thing the
// page rebuild has to carry: figures. A picture reaches the page only through
// the `#/pictures/N` branch of the item walk — its printed caption is a `$ref`
// into `texts` that is not a body child of its own, and the vision model's
// description lives on the picture. Between 2026-08-11 and this test, neither
// was walked, so every caption was silently dropped for any document with page
// provenance while the unit suite stayed green.
//
// testdata/figure-2p.pdf is two pages: prose on page 1, and on page 2 a bar
// chart with a printed caption below it.
//
// The caption half needs only a sidecar. The description half additionally
// needs a reachable vision model, so it is gated separately:
//
//	DOCLING_TEST_URL=http://localhost:5001 \
//	DOCLING_TEST_VLM_URL=https://<host>/v1/chat/completions \
//	DOCLING_TEST_VLM_MODEL=jlu/gemma-4-26b-it \
//	DOCLING_TEST_VLM_KEY=<key> \
//	go test ./internal/parser/docling -run Integration -v
func TestIntegration_RealSidecar_KeepsFigureCaptions(t *testing.T) {
	baseURL := os.Getenv("DOCLING_TEST_URL")
	if baseURL == "" {
		t.Skip("set DOCLING_TEST_URL to run against a live docling-serve")
	}

	c := NewClient(baseURL, 300*time.Second)
	vlmURL := os.Getenv("DOCLING_TEST_VLM_URL")
	if vlmURL != "" {
		c.Options = ConvertOptions{
			PictureDescription:    true,
			PictureClassification: true,
			PictureAreaThreshold:  0.05,
			PictureAPIURL:         vlmURL,
			PictureAPIModel:       os.Getenv("DOCLING_TEST_VLM_MODEL"),
			PictureAPIKey:         os.Getenv("DOCLING_TEST_VLM_KEY"),
		}
	}

	p := &DoclingPDFParser{Client: c}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: "testdata/figure-2p.pdf",
		FileName: "figure-2p.pdf",
		MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("parse against live sidecar: %v", err)
	}
	if len(res.Pages) != 2 {
		t.Fatalf("expected 2 pages, got %d: %+v", len(res.Pages), res.Pages)
	}
	byNumber := map[int]string{}
	for _, pg := range res.Pages {
		byNumber[pg.PageNumber] = pg.Text
	}

	// The caption is on the figure's page, not merely somewhere in the doc.
	if !strings.Contains(byNumber[2], "CAPTIONMARK") {
		t.Errorf("figure caption missing from page 2; text=%q", byNumber[2])
	}
	if strings.Contains(byNumber[1], "CAPTIONMARK") {
		t.Errorf("figure caption leaked onto page 1; text=%q", byNumber[1])
	}
	// It must appear exactly once: the caption is reachable both as a picture
	// caption ref and (depending on layout detection) as a body child.
	if n := strings.Count(byNumber[2], "CAPTIONMARK"); n != 1 {
		t.Errorf("caption appears %d times on page 2, want 1; text=%q", n, byNumber[2])
	}

	if vlmURL == "" {
		t.Log("DOCLING_TEST_VLM_URL unset: caption provenance checked, vision description NOT exercised")
		return
	}

	// With a vision model wired, the chart must contribute prose of its own —
	// more than the caption line it sits under. Asserting on the model's exact
	// words would be a flaky test of gemma-4, not of this package, so the
	// assertion is that a description arrived and is substantial.
	body := strings.ReplaceAll(byNumber[2], "CAPTIONMARK Abbildung 1: Stoerungsmeldungen je Monat.", "")
	body = strings.ReplaceAll(body, "UNIQUEPAGE2 Der folgende Abschnitt zeigt die Entwicklung der Meldungen.", "")
	body = strings.ReplaceAll(body, "PAGETWO Auswertung", "")
	if len(strings.Fields(body)) < 8 {
		t.Errorf("no vision description landed on page 2 beyond the known prose; page text=%q", byNumber[2])
	}
}

// TestIntegration_RealSidecar_AcceptsTheDefaultRequest sends what a worker
// with default site-config sends (accurate tables, de+en OCR, document
// timeout, heading hierarchy) through Probe and asserts the sidecar accepts
// the field set. A field the pinned image does not know is a 422, and a 422
// here means every real conversion would fall back to pdftotext.
func TestIntegration_RealSidecar_AcceptsTheDefaultRequest(t *testing.T) {
	baseURL := os.Getenv("DOCLING_TEST_URL")
	if baseURL == "" {
		t.Skip("set DOCLING_TEST_URL to run against a live docling-serve")
	}
	c := NewClient(baseURL, 120*time.Second)
	c.Options = ConvertOptions{
		TableMode:              "accurate",
		OCRLanguages:           []string{"de", "en"},
		DocumentTimeoutSeconds: 600,
	}
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("sidecar rejected the default request: %v", err)
	}
}
