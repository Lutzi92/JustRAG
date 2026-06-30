// Package widcert fetches CERT-Bund / WID security advisories from the public
// WID content API and formats them as structured, searchable markdown.
//
// The advisory's rich data (CVSS scores, affected products, CVE list,
// references) is JSON keyed by advisory name. Fetch is a two-step call:
// name -> uuid -> content JSON. See FormatMarkdown for the output shape.
package widcert

// Host is the only host this package ever contacts. HTTPS only.
const Host = "wid.cert-bund.de"

const (
	// uuidByNamePath resolves an advisory name (e.g. WID-SEC-2026-2038) to a UUID.
	uuidByNamePath = "/content/public/securityAdvisory/kurzinfo-uuid-by-name/"
	// contentPath returns the full advisory JSON for a UUID.
	contentPath = "/content/public/content/"
)

// Top-level child section discriminators in the WID content JSON. Each
// top-level node carries a stable "type"; the parser keys on these values
// rather than section order.
const (
	sectionScores     = "scoreListe"
	sectionCVEIDs     = "cveIdListe"
	sectionReferences = "documentReferenceListe"
	sectionProducts   = "productReferenceListe"
)
