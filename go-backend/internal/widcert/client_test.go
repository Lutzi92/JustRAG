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
	c.http = srv.Client() // bypass the SSRF-safe transport, which blocks httptest's loopback addr

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

func TestClientFetch_UUIDNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL
	c.http = srv.Client() // bypass the SSRF-safe transport, which blocks httptest's loopback addr
	if _, err := c.Fetch(context.Background(), "WID-SEC-X"); err == nil {
		t.Error("expected error when UUID lookup 404s")
	}
}
