package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func corsHandler(origins []string) http.Handler {
	return CORS(origins)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
}

// An allowed origin gets the reflected ACAO plus credentials. Credentials are
// the reason the allowlist must be explicit in production: with credentials on,
// a wildcard/reflecting ACAO would let any site read authenticated responses.
func TestCORS_AllowedOrigin(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/kb", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	corsHandler([]string{"https://app.example.com"}).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the allowed origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want \"true\"", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// A non-allowlisted origin must get no ACAO header at all. The request itself
// still reaches the handler (CORS is a browser-enforced policy, not server-side
// authz — that is what the auth chain is for), but the browser will refuse to
// hand the response to the calling page.
func TestCORS_DisallowedOriginGetsNoACAO(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/kb", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	corsHandler([]string{"https://app.example.com"}).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a disallowed origin", got)
	}
}

// Preflight must advertise the methods and headers the SPA actually sends
// (Authorization + X-Request-Id are the non-obvious ones) and cache the answer.
//
// Access-Control-Request-Headers is spelled lowercase on purpose: per the Fetch
// spec a browser byte-lowercases and sorts that list, and rs/cors matches it
// case-sensitively against its own lowercased allowlist. Writing "Authorization"
// here makes the preflight fail with no CORS headers at all — a realistic-looking
// but wrong test that would misreport this middleware as broken.
func TestCORS_Preflight(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodOptions, "/api/kb", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	rec := httptest.NewRecorder()
	corsHandler([]string{"https://app.example.com"}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 200 or 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the allowed origin", got)
	}
	allowMethods := rec.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(allowMethods, "POST") {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to include POST", allowMethods)
	}
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(strings.ToLower(allowHeaders), "authorization") {
		t.Errorf("Access-Control-Allow-Headers = %q, want it to include authorization", allowHeaders)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "86400" {
		t.Errorf("Access-Control-Max-Age = %q, want \"86400\"", got)
	}
}

// Content-Disposition must be exposed or the SPA's file-download path cannot
// read the server-chosen filename.
func TestCORS_ExposesContentDisposition(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/files/1/download", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	corsHandler([]string{"https://app.example.com"}).ServeHTTP(rec, req)

	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	if !strings.Contains(exposed, "Content-Disposition") {
		t.Errorf("Access-Control-Expose-Headers = %q, want it to include Content-Disposition", exposed)
	}
}

// A request with no Origin (curl, server-to-server, same-origin navigation)
// must pass through untouched.
func TestCORS_NoOriginPassesThrough(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	corsHandler([]string{"https://app.example.com"}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty when no Origin was sent", got)
	}
}
