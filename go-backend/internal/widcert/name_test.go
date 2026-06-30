package widcert

import "testing"

func TestExtractName(t *testing.T) {
	cases := []struct {
		name              string
		title, guid, link string
		want              string
	}{
		{"from title", "WID-SEC-2026-2038: OpenSSL vuln", "", "", "WID-SEC-2026-2038"},
		{"from guid", "OpenSSL vuln", "urn:WID-CERT-2026-0007", "", "WID-CERT-2026-0007"},
		{"from link", "OpenSSL", "", "https://wid.cert-bund.de/portal/wid/WID-SEC-2026-2038", "WID-SEC-2026-2038"},
		{"title wins over link", "WID-SEC-2026-0001 fix", "", "https://x/WID-SEC-2026-9999", "WID-SEC-2026-0001"},
		{"no match", "Generic advisory", "guid-123", "https://example.com/x", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractName(tc.title, tc.guid, tc.link); got != tc.want {
				t.Errorf("ExtractName(%q,%q,%q) = %q, want %q", tc.title, tc.guid, tc.link, got, tc.want)
			}
		})
	}
}
