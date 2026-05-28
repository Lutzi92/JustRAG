package files

import "testing"

func TestSanitizeContentDispositionFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "report.pdf", "report.pdf"},
		{"double_quote", `eo".pdf`, "eo_.pdf"},
		{"backslash", `a\b.pdf`, "a_b.pdf"},
		{"newline", "a\nb.pdf", "a_b.pdf"},
		{"carriage_return", "a\rb.pdf", "a_b.pdf"},
		{"del_char", "a\x7fb.pdf", "a_b.pdf"},
		{"unicode_kept", "Bären.pdf", "Bären.pdf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeContentDispositionFilename(c.in); got != c.want {
				t.Errorf("sanitizeContentDispositionFilename(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
