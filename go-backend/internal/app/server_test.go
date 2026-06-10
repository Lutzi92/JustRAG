package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIsUploadExempt pins the global-body-cap exemption to exactly the
// upload routes that set their own larger caps internally. A route that
// wrongly matches here would let a handler read an unbounded request body.
func TestIsUploadExempt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		// The exempt upload routes.
		{http.MethodPost, "/api/kb/123/files", true},
		{http.MethodPost, "/api/kb/123/eval/golden-sets", true},
		{http.MethodPost, "/api/site-config/logo", true},
		{http.MethodPost, "/api/describe-image", true},
		{http.MethodPost, "/api/admin/agent/template", true},
		{http.MethodPost, "/api/admin/eval/golden-sets", true},

		// Non-POST never exempt.
		{http.MethodGet, "/api/kb/123/files", false},
		{http.MethodPut, "/api/site-config/logo", false},

		// Sibling /api/kb/ routes must NOT be exempt (the old prefix-based
		// exemption wrongly skipped the cap for all of these).
		{http.MethodPost, "/api/kb", false},
		{http.MethodPost, "/api/kb/", false},
		{http.MethodPost, "/api/kb/123/text", false},
		{http.MethodPost, "/api/kb/123/add-sources", false},
		{http.MethodPost, "/api/kb/123/fetch-url", false},
		{http.MethodPost, "/api/kb/123/websearch", false},
		{http.MethodPost, "/api/kb/123/eval/golden-sets/generate", false},
		{http.MethodPost, "/api/kb/123/eval/runs", false},
		{http.MethodPost, "/api/kb/123/files/extra", false},

		// Other near-misses.
		{http.MethodPost, "/api/admin/eval/golden-sets/generate", false},
		{http.MethodPost, "/api/chat", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := isUploadExempt(r); got != tc.want {
			t.Errorf("isUploadExempt(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
