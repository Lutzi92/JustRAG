package widcert

import "regexp"

// widNamePattern matches WID advisory IDs like WID-SEC-2026-2038 / WID-CERT-2026-0007.
var widNamePattern = regexp.MustCompile(`WID-(?:SEC|CERT)-\d{4}-\d+`)

// ExtractName returns the first WID advisory ID found in title, then guid, then
// link, or "" if none of them contain one.
func ExtractName(title, guid, link string) string {
	for _, s := range []string{title, guid, link} {
		if m := widNamePattern.FindString(s); m != "" {
			return m
		}
	}
	return ""
}
