package academic

import "context"

// AllowAllURLsForTest disables the SSRF check so a loopback httptest server
// can serve a PDF. Test-only; returns a restore func.
func AllowAllURLsForTest() (restore func()) {
	old := validateURL
	validateURL = func(context.Context, string) error { return nil }
	return func() { validateURL = old }
}
