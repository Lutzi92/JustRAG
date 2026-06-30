package widcert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientFetch_TwoStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "kurzinfo-uuid-by-name"):
			w.Write([]byte(`"uuid-abc"`)) // WID returns a bare JSON string
		case strings.Contains(r.URL.Path, "/content/public/content/uuid-abc"):
			w.Write([]byte(`{"properties":{"title":"T","description":"D"},
				"children":[{"type":"cveIdListe","children":[{"properties":{"cveId":"CVE-2026-1"}}]}]}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL

	adv, err := c.Fetch(context.Background(), "WID-SEC-2026-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if adv.Title != "T" || len(adv.CVEs) != 1 || adv.CVEs[0] != "CVE-2026-1" {
		t.Errorf("unexpected advisory: %+v", adv)
	}
	if adv.Name != "WID-SEC-2026-1" {
		t.Errorf("Name = %q", adv.Name)
	}
}

func TestCheckRedirect_RefusesPrivateAndCapsHops(t *testing.T) {
	// Redirect to a literal private/metadata IP must be refused (the SSRF
	// protection we keep after dropping the dial-time block). No DNS needed.
	priv, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://169.254.169.254/latest/meta-data/", nil)
	if err := checkRedirect(priv, nil); err == nil {
		t.Error("redirect to 169.254.169.254 must be refused")
	}

	// A public target is allowed. Use a literal public IP to avoid a DNS lookup.
	pub, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://93.184.216.34/x", nil)
	if err := checkRedirect(pub, nil); err != nil {
		t.Errorf("public redirect target should be allowed, got: %v", err)
	}

	// Hop cap fires regardless of target.
	if err := checkRedirect(pub, make([]*http.Request, maxRedirects)); err == nil {
		t.Error("expected refusal once the hop cap is reached")
	}
}

func TestClientFetch_UUIDNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL
	if _, err := c.Fetch(context.Background(), "WID-SEC-X"); err == nil {
		t.Error("expected error when UUID lookup 404s")
	}
}
