package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLogging_RedactsInviteTokenInAccessLog guards against the access-log
// call site in logging.go regressing back to the raw r.URL.Path. The invite
// token is a permanent, non-expiring credential (redactSecretPath in
// metrics.go) — it must never appear in the "path" field of the "request"
// access-log line.
//
// This test proves a CALL SITE redacts, not just that the helper works:
// TestRedactSecretPath (metrics_test.go) already covers the helper in
// isolation, but logging.go could stop calling it while that test stays
// green. Mutation-tested: reverting the call site to r.URL.Path turns this
// RED (verified manually; see the second-fix report).
func TestLogging_RedactsInviteTokenInAccessLog(t *testing.T) {
	const token = "kR3x9vQ2mN8pL5wZ7bT1cY6dF0hJ4sA-eG_uI2oK9rV3nB5"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Logging(false)(inner)

	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+token+"/redeem", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	out := buf.String()
	if out == "" {
		t.Fatal("expected an access-log line, got none")
	}
	if strings.Contains(out, token) {
		t.Fatalf("access log leaked the invite token verbatim: %s", out)
	}

	var logLine struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &logLine); err != nil {
		t.Fatalf("access log line is not valid JSON (%q): %v", out, err)
	}
	if logLine.Path != "/api/invites/{token}/redeem" {
		t.Errorf("path = %q, want %q", logLine.Path, "/api/invites/{token}/redeem")
	}
}
