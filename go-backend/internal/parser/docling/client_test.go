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

func TestClient_Convert_EmitsPictureDescriptionFields(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		got = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				got[k] = v[0]
			}
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/convert/file") {
			t.Errorf("expected /v1/convert/file, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{"md_content": "# T"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	c.Options = ConvertOptions{
		PictureDescription:    true,
		PictureAreaThreshold:  0.05,
		PictureClassification: true,
		TableMode:             "accurate",
		PictureAPIURL:         "http://model.local/v1/chat/completions",
		PictureAPIModel:       "jlu/gemma-4-26b-it",
		PictureAPIKey:         "secret-key",
	}
	if _, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x")); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got["do_picture_description"] != "true" {
		t.Errorf("missing do_picture_description, got %+v", got)
	}
	if got["picture_description_area_threshold"] != "0.05" {
		t.Errorf("bad area threshold: %q", got["picture_description_area_threshold"])
	}
	if got["do_picture_classification"] != "true" {
		t.Errorf("missing do_picture_classification")
	}
	if got["table_mode"] != "accurate" {
		t.Errorf("bad table_mode: %q", got["table_mode"])
	}
	if got["abort_on_error"] != "false" {
		t.Errorf("expected abort_on_error=false")
	}
	// picture_description_api must carry the endpoint, model, and bearer header
	// so Docling can call our authenticated model API.
	var api struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Params  map[string]any    `json:"params"`
	}
	if err := json.Unmarshal([]byte(got["picture_description_api"]), &api); err != nil {
		t.Fatalf("picture_description_api not valid JSON: %q (%v)", got["picture_description_api"], err)
	}
	if api.URL != "http://model.local/v1/chat/completions" {
		t.Errorf("bad api url: %q", api.URL)
	}
	if api.Headers["Authorization"] != "Bearer secret-key" {
		t.Errorf("missing/!= bearer header: %q", api.Headers["Authorization"])
	}
	if api.Params["model"] != "jlu/gemma-4-26b-it" {
		t.Errorf("bad api model: %v", api.Params["model"])
	}
}

func TestClient_Convert_PictureAPIOmittedWhenNoKey(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		got = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				got[k] = v[0]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"document": map[string]any{"md_content": "# T"}})
	}))
	defer srv.Close()

	// PictureDescription on but no endpoint resolved → enrichment flag is sent,
	// but no picture_description_api field (Docling would fall back to its own
	// default model config, or skip — never our request without a URL).
	c := NewClient(srv.URL, 10*time.Second)
	c.Options = ConvertOptions{PictureDescription: true}
	if _, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x")); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got["do_picture_description"] != "true" {
		t.Errorf("expected do_picture_description=true")
	}
	if _, ok := got["picture_description_api"]; ok {
		t.Errorf("picture_description_api should be omitted with no URL")
	}
}

func TestClient_Convert_ZeroOptionsOmitsNewFields(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		got = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				got[k] = v[0]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"document": map[string]any{"md_content": "# T"}})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second) // zero Options
	if _, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x")); err != nil {
		t.Fatalf("convert: %v", err)
	}
	for _, k := range []string{"do_picture_description", "picture_description_area_threshold", "do_picture_classification", "table_mode"} {
		if _, ok := got[k]; ok {
			t.Errorf("field %q should be omitted with zero options", k)
		}
	}
}
