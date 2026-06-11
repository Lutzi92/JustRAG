package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONAPIErrors(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/widgets", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/json404", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"widget not found"}`))
	})
	mux.HandleFunc("GET /api/text404", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such report", http.StatusNotFound)
	})

	h := JSONAPIErrors("/api/", "/openai/")(mux)

	tests := []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantJSON    bool
		wantError   string // expected envelope message; empty = no envelope check
		wantRawBody string // expected raw body; empty = skip
	}{
		{
			name:       "unmatched API route gets JSON 404",
			method:     http.MethodGet,
			path:       "/api/missing",
			wantStatus: http.StatusNotFound,
			wantJSON:   true,
			wantError:  "404 page not found",
		},
		{
			name:       "method mismatch gets JSON 405",
			method:     http.MethodPost,
			path:       "/api/widgets",
			wantStatus: http.StatusMethodNotAllowed,
			wantJSON:   true,
			wantError:  "Method Not Allowed",
		},
		{
			name:       "openai namespace gets JSON 404",
			method:     http.MethodGet,
			path:       "/openai/v1/missing",
			wantStatus: http.StatusNotFound,
			wantJSON:   true,
			wantError:  "404 page not found",
		},
		{
			name:       "handler text 404 message is preserved in envelope",
			method:     http.MethodGet,
			path:       "/api/text404",
			wantStatus: http.StatusNotFound,
			wantJSON:   true,
			wantError:  "no such report",
		},
		{
			name:        "handler JSON 404 passes through untouched",
			method:      http.MethodGet,
			path:        "/api/json404",
			wantStatus:  http.StatusNotFound,
			wantJSON:    true,
			wantRawBody: `{"error":"widget not found"}`,
		},
		{
			name:        "non-API 404 stays plain text",
			method:      http.MethodGet,
			path:        "/static/missing",
			wantStatus:  http.StatusNotFound,
			wantJSON:    false,
			wantRawBody: "404 page not found",
		},
		{
			name:        "matched route unaffected",
			method:      http.MethodGet,
			path:        "/api/widgets",
			wantStatus:  http.StatusOK,
			wantJSON:    false,
			wantRawBody: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			ct := rec.Header().Get("Content-Type")
			if tt.wantJSON && !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("Content-Type = %q, want application/json", ct)
			}
			if !tt.wantJSON && strings.HasPrefix(ct, "application/json") {
				t.Fatalf("Content-Type = %q, want non-JSON", ct)
			}
			if tt.wantError != "" {
				var envelope struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("body %q is not the JSON envelope: %v", rec.Body.String(), err)
				}
				if envelope.Error != tt.wantError {
					t.Fatalf("envelope error = %q, want %q", envelope.Error, tt.wantError)
				}
			}
			if tt.wantRawBody != "" && strings.TrimSpace(rec.Body.String()) != tt.wantRawBody {
				t.Fatalf("body = %q, want %q", strings.TrimSpace(rec.Body.String()), tt.wantRawBody)
			}
		})
	}
}

func TestJSONAPIErrors_405KeepsAllowHeader(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/widgets", func(w http.ResponseWriter, _ *http.Request) {})
	h := JSONAPIErrors("/api/")(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/widgets", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Fatalf("Allow header = %q, want it to contain GET", allow)
	}
}
