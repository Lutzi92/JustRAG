package middleware

import "net/http"

// DefaultCSP is the built-in Content-Security-Policy. The script-src hash
// covers the inline script Vite emits in the production frontend build. If
// the frontend build changes that inline content the hash drifts, and
// CSP-enforcing browsers silently refuse to execute the script — there is no
// server-side signal of the mismatch. Override via the csp argument to
// SecurityHeaders (wired through the CSP_HEADER env var in internal/config)
// so a hash drift can be fixed at deploy time without a rebuild.
//
// To regenerate the hash after a frontend build, extract the inline script
// body from web/dist/index.html and sha256/base64 it, e.g.:
//
//	# grab the text between the inline <script>…</script> tags, no tags, no
//	# surrounding whitespace trimming beyond what the browser hashes:
//	openssl dgst -sha256 -binary inline-script.js | openssl base64
//
// then splice the result into the 'sha256-…' token below. The browser
// console logs the expected hash on a CSP violation, which is the quickest
// source of truth when the two disagree.
// img-src permits data: and blob: so the mind-map PNG export works: the
// html-to-image library rasterizes the graph by loading an SVG as a data: URL
// image and drawing it to a canvas. Without this, img-src inherits
// default-src 'self' and browsers block that image load, failing the export.
const DefaultCSP = "default-src 'self'; script-src 'self' 'sha256-ieoeWczDHkReVBsRBqaal5AFMlBtNjMzgwKvLqi/tSU='; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; frame-src 'self' blob:; media-src 'self' blob:"

// SecurityHeaders sets the standard security response headers. emitHSTS
// gates Strict-Transport-Security: HSTS has no effect over plain HTTP, and
// emitting it from a dev server reachable over both HTTP and HTTPS (e.g. a
// port-forward into a cluster) can poison the browser's HSTS cache for the
// hostname. Pass true in production where TLS termination is guaranteed
// upstream; false for local development.
//
// csp is the Content-Security-Policy header value. Empty falls back to
// DefaultCSP.
func SecurityHeaders(emitHSTS bool, csp string) func(http.Handler) http.Handler {
	if csp == "" {
		csp = DefaultCSP
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			// X-XSS-Protection: 0 disables the legacy IE/old-Chrome reflective
			// XSS auditor, which had its own bypass vulnerabilities and could be
			// abused to create xs-leaks. Modern browsers either ignore the header
			// or treat 0 as the safe default; explicitly opting out is the
			// current OWASP recommendation. Not an accidental zero — keep as-is.
			w.Header().Set("X-XSS-Protection", "0")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			// Permissions-Policy denies access to browser APIs the app has
			// no business using. The RAG chat UI never needs camera, mic,
			// geolocation, payment, or USB — denying them up front means a
			// compromised script or a malicious iframe can't trigger the
			// browser permission prompt that a curious user might accept.
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
			w.Header().Set("Content-Security-Policy", csp)
			if emitHSTS {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}

			next.ServeHTTP(w, r)
		})
	}
}
