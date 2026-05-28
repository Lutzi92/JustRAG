package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeStore returns canned site_config values keyed by config key. A nil
// entry means "missing"; an entry with non-nil string means "present".
type fakeStore struct {
	vals map[string]*string
	err  error
}

func (f *fakeStore) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vals[key], nil
}

func ptr(s string) *string { return &s }

// reset restores the package-level Google endpoint to its production value
// after a test that overrode it.
func restoreEndpoint(t *testing.T, prev string) {
	t.Helper()
	t.Cleanup(func() { googleSearchEndpoint = prev })
}

func TestWebSearch_InputValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"invalid JSON", `{not json`, http.StatusBadRequest},
		{"missing query", `{"query":"  "}`, http.StatusBadRequest},
		{"empty body", ``, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := NewHandler(&fakeStore{})
			req := httptest.NewRequest(http.MethodPost, "/websearch", bytes.NewBufferString(c.body))
			rec := httptest.NewRecorder()
			h.WebSearch(rec, req)
			if rec.Code != c.wantCode {
				t.Errorf("status = %d, want %d (body=%q)", rec.Code, c.wantCode, rec.Body.String())
			}
		})
	}
}

func TestWebSearch_ConfigGating(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		store    *fakeStore
		wantCode int
	}{
		{
			name:     "site config read fails -> 500",
			store:    &fakeStore{err: errors.New("boom")},
			wantCode: http.StatusInternalServerError,
		},
		{
			name:     "web_search_enabled missing -> 403",
			store:    &fakeStore{vals: map[string]*string{}},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "web_search_enabled=false -> 403",
			store:    &fakeStore{vals: map[string]*string{"web_search_enabled": ptr("false")}},
			wantCode: http.StatusForbidden,
		},
		{
			name: "missing api key -> 500",
			store: &fakeStore{vals: map[string]*string{
				"web_search_enabled": ptr("true"),
			}},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "missing cx -> 500",
			store: &fakeStore{vals: map[string]*string{
				"web_search_enabled":    ptr("true"),
				"google_search_api_key": ptr("key"),
			}},
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := NewHandler(c.store)
			req := httptest.NewRequest(http.MethodPost, "/websearch",
				bytes.NewBufferString(`{"query":"hello"}`))
			rec := httptest.NewRecorder()
			h.WebSearch(rec, req)
			if rec.Code != c.wantCode {
				t.Errorf("status = %d, want %d (body=%q)", rec.Code, c.wantCode, rec.Body.String())
			}
		})
	}
}

// Full success path: httptest.Server returns a canned Google response;
// handler parses it and returns the expected shape. We also assert the
// outbound query params so a future regression that drops `lr=lang_*`,
// `num`, or `q` is caught immediately.
func TestWebSearch_GoogleSuccess(t *testing.T) {
	restoreEndpoint(t, googleSearchEndpoint)

	var calls atomic.Int32
	var seenQuery, seenLang, seenNum string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		seenQuery = q.Get("q")
		seenLang = q.Get("lr")
		seenNum = q.Get("num")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(googleSearchResponse{
			Items: []googleSearchItem{
				{Title: "T1", Snippet: "S1", Link: "https://example.com/1"},
				{Title: "T2", Snippet: "S2", Link: "https://example.com/2"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	googleSearchEndpoint = srv.URL

	store := &fakeStore{vals: map[string]*string{
		"web_search_enabled":    ptr("true"),
		"google_search_api_key": ptr("api-key"),
		"google_search_cx":      ptr("cx-id"),
	}}
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/websearch",
		bytes.NewBufferString(`{"query":"golang","limit":2,"language":"de"}`))
	rec := httptest.NewRecorder()
	h.WebSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("Google API called %d times, want 1", got)
	}
	if seenQuery != "golang" {
		t.Errorf("outbound q = %q, want %q", seenQuery, "golang")
	}
	if seenLang != "lang_de" {
		t.Errorf("outbound lr = %q, want %q", seenLang, "lang_de")
	}
	if seenNum != "2" {
		t.Errorf("outbound num = %q, want %q", seenNum, "2")
	}

	var resp webSearchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	if resp.Results[0].URL != "https://example.com/1" {
		t.Errorf("first URL = %q, want %q", resp.Results[0].URL, "https://example.com/1")
	}
}

// Defaults: limit out-of-range and empty language are normalised before
// the Google call. Captured query params prove the defaults are applied.
func TestWebSearch_DefaultsApplied(t *testing.T) {
	restoreEndpoint(t, googleSearchEndpoint)

	var seenLang, seenNum string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenLang = r.URL.Query().Get("lr")
		seenNum = r.URL.Query().Get("num")
		_ = json.NewEncoder(w).Encode(googleSearchResponse{})
	}))
	t.Cleanup(srv.Close)
	googleSearchEndpoint = srv.URL

	store := &fakeStore{vals: map[string]*string{
		"web_search_enabled":    ptr("true"),
		"google_search_api_key": ptr("k"),
		"google_search_cx":      ptr("c"),
	}}
	h := NewHandler(store)

	t.Run("limit 0 -> default 5, language '' -> 'en'", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/websearch",
			bytes.NewBufferString(`{"query":"q"}`))
		rec := httptest.NewRecorder()
		h.WebSearch(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
		}
		if seenNum != "5" {
			t.Errorf("num = %q, want 5", seenNum)
		}
		if seenLang != "lang_en" {
			t.Errorf("lr = %q, want lang_en", seenLang)
		}
	})

	t.Run("limit 99 (>10) -> clamped to 5", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/websearch",
			bytes.NewBufferString(`{"query":"q","limit":99}`))
		rec := httptest.NewRecorder()
		h.WebSearch(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		if seenNum != "5" {
			t.Errorf("num = %q, want 5 (clamped)", seenNum)
		}
	})
}

func TestWebSearch_Google5xx(t *testing.T) {
	// Not t.Parallel(): mutates the package-level googleSearchEndpoint.
	restoreEndpoint(t, googleSearchEndpoint)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	googleSearchEndpoint = srv.URL

	store := &fakeStore{vals: map[string]*string{
		"web_search_enabled":    ptr("true"),
		"google_search_api_key": ptr("k"),
		"google_search_cx":      ptr("c"),
	}}
	h := NewHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/websearch",
		bytes.NewBufferString(`{"query":"q"}`))
	rec := httptest.NewRecorder()
	h.WebSearch(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("Google 500 should surface as 502, got %d (body=%q)", rec.Code, rec.Body.String())
	}
}

// CallGoogle is the public reuse point used by the research agent. It
// exercises the same callGoogleSearch path; pin its contract too.
func TestCallGoogle_HappyPath(t *testing.T) {
	restoreEndpoint(t, googleSearchEndpoint)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(googleSearchResponse{
			Items: []googleSearchItem{{Title: "X", Snippet: "Y", Link: "https://z/"}},
		})
	}))
	t.Cleanup(srv.Close)
	googleSearchEndpoint = srv.URL

	hits, err := CallGoogle(context.Background(), "k", "c", "q", 1, "en")
	if err != nil {
		t.Fatalf("CallGoogle: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	want := Hit{Title: "X", Snippet: "Y", URL: "https://z/"}
	if hits[0] != want {
		t.Errorf("hit = %+v, want %+v", hits[0], want)
	}
}

func TestCallGoogle_BadJSON(t *testing.T) {
	// Not t.Parallel(): mutates the package-level googleSearchEndpoint.
	restoreEndpoint(t, googleSearchEndpoint)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	t.Cleanup(srv.Close)
	googleSearchEndpoint = srv.URL

	_, err := CallGoogle(context.Background(), "k", "c", "q", 1, "en")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}
