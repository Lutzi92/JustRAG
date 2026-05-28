package fetcher

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// testRedirect builds a checkRedirect closure with the default 10-hop limit.
func testRedirect() func(*http.Request, []*http.Request) error {
	return newCheckRedirect(DefaultConfig())
}

func TestCheckRedirectRejectsSchemeDowngrade(t *testing.T) {
	t.Parallel()
	check := testRedirect()
	via := []*http.Request{{URL: mustURL("https://safe.example/")}}
	next := &http.Request{URL: mustURL("http://safe.example/")}
	if err := check(next, via); err == nil {
		t.Fatal("expected scheme downgrade to be rejected")
	}
}

func TestCheckRedirectLimit(t *testing.T) {
	t.Parallel()
	check := testRedirect()
	via := make([]*http.Request, 11)
	for i := range via {
		via[i] = &http.Request{URL: mustURL("https://example.com/")}
	}
	next := &http.Request{URL: mustURL("https://example.com/next")}
	err := check(next, via)
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("expected too many redirects, got %v", err)
	}
}

func TestCheckRedirectHonoursConfigMaxRedirects(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MaxRedirects = 3
	check := newCheckRedirect(cfg)
	via := make([]*http.Request, 3)
	for i := range via {
		via[i] = &http.Request{URL: mustURL("https://example.com/")}
	}
	next := &http.Request{URL: mustURL("https://example.com/next")}
	if err := check(next, via); err == nil {
		t.Fatal("expected too many redirects with cfg.MaxRedirects=3 and via length 3")
	}
}

func TestApplyDefaultHeaders(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequest("GET", "https://example.com/", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	applyDefaultHeaders(req, "TestUA/1.0", "en-US")
	if got := req.Header.Get("User-Agent"); got != "TestUA/1.0" {
		t.Errorf("UA = %q", got)
	}
	if got := req.Header.Get("Accept-Language"); got != "en-US" {
		t.Errorf("Accept-Language = %q", got)
	}
	if got := req.Header.Get("Accept"); !strings.Contains(got, "text/html") {
		t.Errorf("Accept = %q", got)
	}
	if got := req.Header.Get("Accept-Encoding"); !strings.Contains(got, "br") {
		t.Errorf("Accept-Encoding = %q", got)
	}
	// Chrome Client Hints
	if got := req.Header.Get("Sec-Ch-Ua"); got == "" {
		t.Error("missing Sec-Ch-Ua header")
	}
	if got := req.Header.Get("Sec-Ch-Ua-Mobile"); got != "?0" {
		t.Errorf("Sec-Ch-Ua-Mobile = %q, want ?0", got)
	}
	if got := req.Header.Get("Sec-Ch-Ua-Platform"); got == "" {
		t.Error("missing Sec-Ch-Ua-Platform header")
	}
	// Sec-Fetch navigation headers
	if got := req.Header.Get("Sec-Fetch-Dest"); got != "document" {
		t.Errorf("Sec-Fetch-Dest = %q, want document", got)
	}
	if got := req.Header.Get("Sec-Fetch-Mode"); got != "navigate" {
		t.Errorf("Sec-Fetch-Mode = %q, want navigate", got)
	}
	if got := req.Header.Get("Sec-Fetch-Site"); got != "none" {
		t.Errorf("Sec-Fetch-Site = %q, want none", got)
	}
	if got := req.Header.Get("Sec-Fetch-User"); got != "?1" {
		t.Errorf("Sec-Fetch-User = %q, want ?1", got)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic("mustURL: failed to parse " + s + ": " + err.Error())
	}
	return u
}
