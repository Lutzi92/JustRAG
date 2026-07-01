package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDefaultCSPAllowsDataImages guards the fix for the mind-map PNG export.
// html-to-image rasterizes the graph by loading an SVG as a data: URL <img>
// and drawing it to a canvas; if img-src does not permit data: (here it would
// otherwise inherit default-src 'self'), CSP-enforcing browsers block the
// image load and the export rejects with "Export failed".
func TestDefaultCSPAllowsDataImages(t *testing.T) {
	var imgSrc string
	for d := range strings.SplitSeq(DefaultCSP, ";") {
		d = strings.TrimSpace(d)
		if strings.HasPrefix(d, "img-src") {
			imgSrc = d
			break
		}
	}
	if imgSrc == "" {
		t.Fatalf("DefaultCSP has no explicit img-src directive; img-src would inherit default-src 'self' and block data: images. CSP=%q", DefaultCSP)
	}
	if !strings.Contains(imgSrc, "data:") {
		t.Errorf("img-src must allow data: for PNG export; got %q", imgSrc)
	}
	if !strings.Contains(imgSrc, "blob:") {
		t.Errorf("img-src should allow blob: for robustness; got %q", imgSrc)
	}
}

// TestSecurityHeadersEmitsCSP confirms the directive actually reaches the wire.
func TestSecurityHeadersEmitsCSP(t *testing.T) {
	h := SecurityHeaders(false, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	got := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(got, "img-src") || !strings.Contains(got, "data:") {
		t.Errorf("emitted CSP missing img-src data:; got %q", got)
	}
}
