package docling

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/parser"
)

func TestDoclingImageParser_CanParse(t *testing.T) {
	p := &DoclingImageParser{}
	if !p.CanParse("image/png", "x.png") {
		t.Error("should match image/png")
	}
	if !p.CanParse("", "PHOTO.JPG") {
		t.Error("should match .JPG by extension, case-insensitive")
	}
	if p.CanParse("application/pdf", "x.pdf") {
		t.Error("should not match pdf")
	}
}

func TestDoclingImageParser_Parse_ReturnsCaption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{"md_content": "A bar chart showing revenue by quarter."},
		})
	}))
	defer srv.Close()

	tmp := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(tmp, []byte("\x89PNG stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &DoclingImageParser{Client: NewClient(srv.URL, 5*time.Second)}
	res, err := p.Parse(context.Background(), parser.ParseContext{FilePath: tmp, FileName: "x.png"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text == "" || res.Text[:1] != "A" {
		t.Errorf("expected caption text, got %q", res.Text)
	}
}
