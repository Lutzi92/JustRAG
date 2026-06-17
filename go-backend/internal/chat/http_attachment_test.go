package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/parser"
)

func testParserFactory() *parser.Factory {
	return parser.DefaultFactoryWith(nil)
}

func TestParseAndSplitAttachmentMarkdown(t *testing.T) {
	sections, full, err := parseAndSplitAttachment(context.Background(), testParserFactory(),
		"module.md", "text/markdown",
		[]byte("# Modul A\n\nECTS: 6\n\n# Modul B\n\nECTS: 4"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(sections) < 1 {
		t.Fatalf("expected >=1 section, got %d", len(sections))
	}
	if full == "" {
		t.Fatal("expected non-empty full text")
	}
}

func TestParseAndSplitAttachmentEmpty(t *testing.T) {
	_, _, err := parseAndSplitAttachment(context.Background(), testParserFactory(),
		"empty.md", "text/markdown", []byte("   "))
	if err != errNoText {
		t.Fatalf("expected errNoText, got %v", err)
	}
}

// TestUploadAttachmentDisabledReturns503 exercises the gate short-circuit. The
// CompareEnabled check fires before any store/parser use, so a Handler with only
// a fake siteConfigReader (compare disabled by default) is sufficient.
func TestUploadAttachmentDisabledReturns503(t *testing.T) {
	h := &Handler{siteConfigReader: &fakeSiteConfigReader{values: map[string]*string{}}}

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat/attachment", nil)
	req.SetPathValue("id", "kb1")
	// Inject an authenticated user so the gate (not the auth check) is what
	// returns; the disabled gate is checked first regardless.
	rec := httptest.NewRecorder()

	h.UploadAttachment(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when compare disabled, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}
