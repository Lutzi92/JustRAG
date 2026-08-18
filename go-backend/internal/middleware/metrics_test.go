package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsMiddleware(t *testing.T) {
	// Create a simple handler that returns 200.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	handler := MetricsMiddleware(inner)

	req := httptest.NewRequest("GET", "/api/kb/550e8400-e29b-41d4-a716-446655440000/files", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMetricsHandler(t *testing.T) {
	// First, make a request through the middleware so there is at least one data point.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := MetricsMiddleware(inner)
	req := httptest.NewRequest("GET", "/api/test", nil)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	// Now call the metrics handler and verify output.
	metricsReq := httptest.NewRequest("GET", "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(metricsRec, metricsReq)

	if metricsRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", metricsRec.Code)
	}

	body := metricsRec.Body.String()
	if !strings.Contains(body, "http_requests_total") {
		t.Error("expected http_requests_total in metrics output")
	}
	if !strings.Contains(body, "http_request_duration_seconds") {
		t.Error("expected http_request_duration_seconds in metrics output")
	}
	if !strings.Contains(body, `app="justrag"`) {
		t.Error("expected app=justrag label in metrics output")
	}
}

func TestNormalizeRoute(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/api/kb/550e8400-e29b-41d4-a716-446655440000/files", "/api/kb/{id}/files"},
		{"/api/kb/550e8400-e29b-41d4-a716-446655440000/chats/660e8400-e29b-41d4-a716-446655440001", "/api/kb/{id}/chats/{id}"},
		{"/api/test", "/api/test"},
		{"/health", "/health"},
		{"", ""},
		{"/api/kb/550E8400-E29B-41D4-A716-446655440000/files", "/api/kb/{id}/files"},
		{"/api/users/12345", "/api/users/{id}"},
		{"/api/users/12345/posts/67890", "/api/users/{id}/posts/{id}"},
		{"/api/users/12345/posts", "/api/users/{id}/posts"},
		{"/api/abc", "/api/abc"},
	}

	for _, tt := range tests {
		got := normalizeRoute(tt.input)
		if got != tt.want {
			t.Errorf("normalizeRoute(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestRedactSecretPath covers the invite-link token redaction that feeds
// normalizeRoute (Prometheus labels + OTel span names) and the access log
// (logging.go). The invite token is a permanent, non-expiring credential —
// unlike a UUID or numeric id it must never survive into any of those sinks
// verbatim.
func TestRedactSecretPath(t *testing.T) {
	const token = "kR3x9vQ2mN8pL5wZ7bT1cY6dF0hJ4sA-eG_uI2oK9rV3nB5"

	tests := []struct {
		name  string
		input string
		// want is the redactSecretPath output AND (since neither the token
		// nor these plain paths contain a UUID or numeric segment) the
		// normalizeRoute output too, so each case exercises both the raw
		// helper and the full pipeline that feeds Prometheus labels, OTel
		// span names, and (indirectly, same helper) the access log.
		want string
	}{
		{
			name:  "invite redeem path is redacted",
			input: "/api/invites/" + token + "/redeem",
			want:  "/api/invites/{token}/redeem",
		},
		{
			name:  "join shell path is redacted",
			input: "/join/" + token,
			want:  "/join/{token}",
		},
		{
			name:  "path that merely starts similarly is not caught",
			input: "/api/invitesfoo/x",
			want:  "/api/invitesfoo/x",
		},
		{
			name:  "join-prefixed path that is not the shell route is not caught",
			input: "/joined/" + token,
			want:  "/joined/" + token,
		},
		{
			name:  "invite path missing the /redeem suffix is not caught",
			input: "/api/invites/" + token,
			want:  "/api/invites/" + token,
		},
		{
			name:  "empty path is unaffected",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactSecretPath(tt.input); got != tt.want {
				t.Errorf("redactSecretPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
			// normalizeRoute must produce the same redaction (it feeds both
			// Prometheus labels and, via NormalizeRoute, OTel span names).
			if got := normalizeRoute(tt.input); got != tt.want {
				t.Errorf("normalizeRoute(%q) = %q, want %q (redaction not applied)", tt.input, got, tt.want)
			}
		})
	}

	// Ordinary paths carrying a UUID or a numeric id must still be collapsed
	// by normalizeRoute exactly as before (TestNormalizeRoute already covers
	// this); redactSecretPath itself must leave them untouched since neither
	// shape is an invite/join path.
	ordinary := []string{
		"/api/kb/550e8400-e29b-41d4-a716-446655440000/files",
		"/api/users/12345",
	}
	for _, p := range ordinary {
		if got := redactSecretPath(p); got != p {
			t.Errorf("redactSecretPath(%q) = %q, want unchanged %q", p, got, p)
		}
	}
}
