package docling

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
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
	if len(res.Pages) != 0 {
		t.Errorf("expected no page info, got Pages=%+v", res.Pages)
	}
	if !strings.Contains(res.Text, "body") {
		t.Errorf("content must still be ingested, got %q", res.Text)
	}
}

func TestDoclingPDFParser_Parse_BuildsPerPageTextFromProvenance(t *testing.T) {
	// The regression this guards: page numbers must come from item provenance,
	// not from locating item text inside the markdown blob. Here the markdown
	// deliberately disagrees with the items (escaped underscores, furniture
	// dropped) exactly as docling's real output does.
	md := "## Kapitel 1\n\nDie Variable heisst max\\_value.\n\n## Kapitel 2\n\nZweite Seite."
	srv := serveDocling(t, md, []map[string]any{
		{"text": "Kopfzeile", "page": 1, "label": "page_header", "layer": "furniture"},
		{"text": "Kapitel 1", "page": 1, "label": "section_header"},
		{"text": "Die Variable heisst max_value.", "page": 1},
		{"text": "Seite 1 von 2", "page": 1, "label": "page_footer", "layer": "furniture"},
		{"text": "Kapitel 2", "page": 2, "label": "section_header"},
		{"text": "Zweite Seite.", "page": 2},
	})

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: stubPDF(t), FileName: "test.pdf", MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	want := []parser.PageText{
		{PageNumber: 1, Text: "## Kapitel 1\n\nDie Variable heisst max_value."},
		{PageNumber: 2, Text: "## Kapitel 2\n\nZweite Seite."},
	}
	if !reflect.DeepEqual(res.Pages, want) {
		t.Fatalf("pages mismatch:\n got %+v\nwant %+v", res.Pages, want)
	}
	// Text must be the concatenation of the pages, since chunk text is taken
	// from the pages and the section index is built from Text.
	if res.Text != want[0].Text+"\n\n"+want[1].Text {
		t.Errorf("Text should be the joined pages, got %q", res.Text)
	}
	if strings.Contains(res.Text, "Kopfzeile") || strings.Contains(res.Text, "Seite 1 von 2") {
		t.Errorf("running header/footer must not be ingested, got %q", res.Text)
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

func TestDoclingPDFParser_Parse_FigureCaptionLandsOnItsPage(t *testing.T) {
	// End-to-end over the HTTP boundary: a picture-description caption must
	// reach ParseResult.Pages, because processor.buildIndexedChunks chunks
	// Pages whenever they are present and never looks at the markdown blob.
	// Note the markdown here *does* carry the caption — that is what made the
	// loss invisible: only the page rebuild dropped it.
	md := "Vor der Abbildung.\n\nAbbildung 3: Meldungen je Monat.\n\nEin Balkendiagramm; 12 auf 47."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"document":{"md_content":` + strconv.Quote(md) + `,"json_content":{
		  "body": {"children": [{"$ref": "#/texts/0"}, {"$ref": "#/pictures/0"}, {"$ref": "#/texts/2"}]},
		  "texts": [
		    {"label": "text", "content_layer": "body", "text": "Vor der Abbildung.", "prov": [{"page_no": 4}]},
		    {"label": "caption", "content_layer": "body", "text": "Abbildung 3: Meldungen je Monat.", "prov": [{"page_no": 4}]},
		    {"label": "text", "content_layer": "body", "text": "Auf der naechsten Seite.", "prov": [{"page_no": 5}]}
		  ],
		  "pictures": [
		    {"label": "picture", "content_layer": "body", "prov": [{"page_no": 4}],
		     "captions": [{"$ref": "#/texts/1"}],
		     "annotations": [{"kind": "description", "text": "Ein Balkendiagramm; 12 auf 47.",
		                      "provenance": "jlu/gemma-4-26b-it"}]}
		  ]
		}}}`))
	}))
	defer srv.Close()

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: stubPDF(t), FileName: "test.pdf", MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	want := []parser.PageText{
		{PageNumber: 4, Text: "Vor der Abbildung.\n\nAbbildung 3: Meldungen je Monat.\n\nEin Balkendiagramm; 12 auf 47."},
		{PageNumber: 5, Text: "Auf der naechsten Seite."},
	}
	if !reflect.DeepEqual(res.Pages, want) {
		t.Fatalf("pages mismatch:\n got %+v\nwant %+v", res.Pages, want)
	}
	// The caption must be attributed to the figure's own page, not merely
	// present somewhere in the document.
	if strings.Contains(res.Pages[1].Text, "Balkendiagramm") {
		t.Errorf("caption leaked onto the wrong page: %q", res.Pages[1].Text)
	}
}

func TestDoclingPDFParser_Parse_LogsConfidence(t *testing.T) {
	// docling's confidence block is the first objective signal for "this PDF
	// parsed badly"; it must land in the structured log with the file name so
	// it can be grepped by request_id like every other pipeline stage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"document":{"md_content":"# T"},
		  "confidence":{"parse_score":1.0,"layout_score":0.41,"mean_score":0.7,"low_score":0.41,
		                "mean_grade":"good","low_grade":"poor"}}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logctx.SetBase(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { logctx.SetBase(nil) })

	p := &DoclingPDFParser{Client: NewClient(srv.URL, 10*time.Second)}
	if _, err := p.Parse(context.Background(), parser.ParseContext{
		FilePath: stubPDF(t), FileName: "scan.pdf", MimeType: "application/pdf",
	}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`"msg":"docling.confidence"`, `"fileName":"scan.pdf"`, `"low_grade":"poor"`, `"layout_score":0.41`} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %s; got %s", want, out)
		}
	}
}
