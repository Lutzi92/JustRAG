package widcert

import (
	"strconv"
	"strings"
)

// FormatMarkdown renders an Advisory as searchable markdown: a flat metadata
// header (scores, CVEs, products as plain text so they survive chunking) plus
// per-field sections. Empty sections are omitted.
func FormatMarkdown(adv *Advisory) string {
	var sb strings.Builder

	sb.WriteString("# ")
	sb.WriteString(adv.Name)
	if adv.Title != "" {
		sb.WriteString(": ")
		sb.WriteString(adv.Title)
	}
	sb.WriteString("\n\n")

	if adv.BaseScore != "" || adv.Classification != "" {
		var parts []string
		if adv.BaseScore != "" {
			parts = append(parts, "CVSS Base: "+formatCVSSScore(adv.BaseScore))
		}
		if adv.TemporalScore != "" {
			parts = append(parts, "Temporal: "+formatCVSSScore(adv.TemporalScore))
		}
		if adv.Classification != "" {
			parts = append(parts, "Classification: "+adv.Classification)
		}
		sb.WriteString(strings.Join(parts, " | "))
		sb.WriteString("\n")
	}
	if len(adv.CVEs) > 0 {
		sb.WriteString("CVEs: ")
		sb.WriteString(strings.Join(adv.CVEs, ", "))
		sb.WriteString("\n")
	}
	if len(adv.Products) > 0 {
		sb.WriteString("Affected products: ")
		sb.WriteString(strings.Join(adv.Products, ", "))
		sb.WriteString("\n")
	}
	if adv.OperatingSystems != "" {
		sb.WriteString("Operating systems: ")
		sb.WriteString(adv.OperatingSystems)
		sb.WriteString("\n")
	}
	if adv.InitialRelease != "" {
		sb.WriteString("Published: ")
		sb.WriteString(shortDate(adv.InitialRelease))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	writeSection(&sb, "Produktbeschreibung", adv.ProductDescription)
	writeSection(&sb, "Angriffsbeschreibung", adv.Description)
	writeList(&sb, "Betroffene Produkte", adv.Products)
	writeList(&sb, "CVEs", adv.CVEs)
	writeList(&sb, "Referenzen", adv.References)

	return sb.String()
}

// formatCVSSScore renders a WID CVSS score in conventional decimal notation.
// WID stores scores as integers ×10 (e.g. 98 == CVSS 9.8), so an integer value
// is divided by 10 and shown with one decimal place ("9.8"). A value that is
// already non-integer (or unparseable) passes through unchanged.
func formatCVSSScore(s string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return strconv.FormatFloat(float64(n)/10, 'f', 1, 64)
	}
	return s
}

// shortDate trims an ISO-8601 timestamp to its YYYY-MM-DD date prefix; strings
// that don't look like a timestamp pass through unchanged.
func shortDate(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return s
}

func writeSection(sb *strings.Builder, heading, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	sb.WriteString("## ")
	sb.WriteString(heading)
	sb.WriteString("\n")
	sb.WriteString(body)
	sb.WriteString("\n\n")
}

func writeList(sb *strings.Builder, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	sb.WriteString("## ")
	sb.WriteString(heading)
	sb.WriteString("\n")
	for _, it := range items {
		sb.WriteString("- ")
		sb.WriteString(it)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}
