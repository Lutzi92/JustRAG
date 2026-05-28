package docling

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_Convert_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content":   "# Title\n\nBody text\n",
				"text_content": "Title\n\nBody text\n",
			},
			"errors": []any{},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	res, err := c.Convert(context.Background(), "test.pdf", strings.NewReader("%PDF-1.4 stub"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Markdown, "Title") {
		t.Errorf("expected markdown to contain Title, got %q", res.Markdown)
	}
	if len(res.Pages) == 0 {
		t.Errorf("expected at least one page entry, got 0")
	}
}

func TestClient_Convert_PreservesPerPagePages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content": "# T",
				"pages": []map[string]any{
					{"number": 1, "text": "p1"},
					{"number": 2, "text": "p2"},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	res, _ := c.Convert(context.Background(), "x.pdf", strings.NewReader("x"))
	if len(res.Pages) != 2 || res.Pages[1].Number != 2 || res.Pages[1].Text != "p2" {
		t.Errorf("unexpected pages: %+v", res.Pages)
	}
}

func TestClient_Convert_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	_, err := c.Convert(context.Background(), "test.pdf", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestClient_Convert_TimeoutPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 50*time.Millisecond)
	_, err := c.Convert(context.Background(), "test.pdf", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestClient_Convert_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	c := NewClient(srv.URL, 10*time.Second)
	_, err := c.Convert(ctx, "test.pdf", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected ctx-cancel error, got nil")
	}
}

func TestNewClient_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	c := NewClient("http://example.com/", 1*time.Second)
	if c.BaseURL() != "http://example.com" {
		t.Errorf("expected trimmed URL, got %q", c.BaseURL())
	}
}

func TestClient_EmptyResponseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"document": map[string]any{}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, 1*time.Second)
	_, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected error for empty document, got nil")
	}
}
