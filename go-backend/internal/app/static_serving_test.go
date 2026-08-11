package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newStaticTestDir builds a dist tree that mirrors what `vite build` emits:
// content-hashed bundles under /assets, plus the verbatim copies of web/public
// at the root (which carry NO content hash).
func newStaticTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"index.html":              "<!doctype html>",
		"sw.js":                   "// service worker",
		"registerSW.js":           "// register",
		"manifest.webmanifest":    "{}",
		"workbox-835c8c05.js":     "// workbox",
		"vite.svg":                "<svg/>",
		"apple-touch-icon.png":    "\x89PNG",
		"logo-test.svg":           "<svg/>",
		"assets/app-BVbTMSpP.js":  "console.log(1)",
		"assets/app-D9n9_Yjv.css": "body{}",
		"onboarding/chat.png":     "\x89PNG",
		"legal/privacy-de.html":   "<p>",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// TestStaticServingCacheControl pins which responses may be cached immutably.
//
// The invariant: `immutable` is a promise that the bytes at a URL will never
// change. That promise only holds for the content-hashed files Vite emits under
// /assets — every other file in dist keeps a stable name across deploys and is
// overwritten in place, so pinning it strands clients on the old copy for a
// year (stale favicons, stale onboarding screenshots).
func TestStaticServingCacheControl(t *testing.T) {
	t.Parallel()

	const immutable = "public, max-age=31536000, immutable"
	const revalidate = "no-cache"

	cases := []struct {
		name string
		path string
		want string
	}{
		// Content-hashed — the ONLY things safe to pin.
		{"hashed js bundle", "/assets/app-BVbTMSpP.js", immutable},
		{"hashed css bundle", "/assets/app-D9n9_Yjv.css", immutable},

		// Non-hashed public/ assets — overwritten in place on every deploy.
		{"favicon", "/vite.svg", revalidate},
		{"apple touch icon", "/apple-touch-icon.png", revalidate},
		{"logo", "/logo-test.svg", revalidate},
		{"onboarding screenshot", "/onboarding/chat.png", revalidate},

		// PWA bootstrap files gate update discovery for every installed client.
		{"service worker", "/sw.js", revalidate},
		{"sw registration shim", "/registerSW.js", revalidate},
		{"web manifest", "/manifest.webmanifest", revalidate},

		// The workbox runtime IS content-hashed, but lives at the dist root
		// rather than under /assets, so it revalidates. Cheap and correct.
		{"workbox runtime", "/workbox-835c8c05.js", revalidate},

		// HTML must always revalidate — it names the hashed bundles.
		//
		// "/" is the critical one: filepath.Join(staticDir, "/") is the dist
		// DIRECTORY, and os.Stat on a directory succeeds — so the app shell
		// takes the "file exists" branch rather than the SPA fallback. Pinning
		// it freezes the bundle hashes a client resolves for a year.
		{"app shell at root", "/", revalidate},
		{"legal page", "/legal/privacy-de.html", revalidate},

		// SPA catch-all for client-side routes.
		{"spa route", "/kb/123/chat", revalidate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			setupStaticServing(mux, newStaticTestDir(t))

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200", tc.path, rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Errorf("GET %s: Cache-Control = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestStaticServingMissingBundleIs404 pins the stale-shell response.
//
// After a deploy the previous build's hashed bundles are gone. A client still
// running the old shell will request them. Answering with index.html under a
// 200 turns a missing file into a module parse error and makes the failure
// invisible to retry logic (and to the access log).
func TestStaticServingMissingBundleIs404(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	setupStaticServing(mux, newStaticTestDir(t))

	cases := []struct {
		name   string
		path   string
		status int
	}{
		{"bundle from a previous deploy", "/assets/app-OLDHASH1.js", http.StatusNotFound},
		{"stylesheet from a previous deploy", "/assets/app-OLDHASH2.css", http.StatusNotFound},
		// Client-side routes must still resolve to the shell.
		{"spa route", "/kb/123/chat", http.StatusOK},
		{"spa route that looks like a file", "/kb/report.2024", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rec.Code != tc.status {
				t.Errorf("GET %s: status = %d, want %d", tc.path, rec.Code, tc.status)
			}
			if tc.status == http.StatusNotFound &&
				strings.Contains(rec.Body.String(), "<!doctype html>") {
				t.Errorf("GET %s: served the app shell instead of a 404 body", tc.path)
			}
		})
	}
}

// TestStaticServingImmutableOnlyUnderAssets guards the rule directly: a new
// file dropped into web/public/ later must default to revalidating, not to
// being pinned for a year. This is the regression that shipped stale favicons.
func TestStaticServingImmutableOnlyUnderAssets(t *testing.T) {
	t.Parallel()

	dir := newStaticTestDir(t)
	// A file nobody thought about when writing the cache rules.
	if err := os.WriteFile(filepath.Join(dir, "brand-new-icon.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	mux := http.NewServeMux()
	setupStaticServing(mux, dir)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/brand-new-icon.svg", nil))

	if got := rec.Header().Get("Cache-Control"); got == "public, max-age=31536000, immutable" {
		t.Errorf("an unhashed file added to public/ was served immutable: %q", got)
	}
}
