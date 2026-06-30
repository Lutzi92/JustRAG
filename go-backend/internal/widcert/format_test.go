package widcert

import (
	"strings"
	"testing"
)

func sampleAdvisory() *Advisory {
	return &Advisory{
		Name:               "WID-SEC-2026-2038",
		Title:              "OpenSSL Schwachstelle",
		BaseScore:          "7.5",
		TemporalScore:      "6.5",
		Classification:     "hoch",
		ProductDescription: "OpenSSL ist eine Krypto-Bibliothek.",
		Description:        "Ein entfernter Angreifer kann ...",
		Products:           []string{"OpenSSL 3.0", "OpenSSL 3.1"},
		CVEs:               []string{"CVE-2026-1234", "CVE-2026-5678"},
		References:         []string{"https://example.com/a", "https://example.com/b"},
		OperatingSystems:   "Linux",
		InitialRelease:     "2026-06-30",
	}
}

func TestFormatMarkdown(t *testing.T) {
	out := FormatMarkdown(sampleAdvisory())
	for _, want := range []string{
		"# WID-SEC-2026-2038: OpenSSL Schwachstelle",
		"CVSS Base: 7.5",
		"Temporal: 6.5",
		"Classification: hoch",
		"CVEs: CVE-2026-1234, CVE-2026-5678",
		"Affected products: OpenSSL 3.0, OpenSSL 3.1",
		"Operating systems: Linux",
		"Published: 2026-06-30",
		"## Produktbeschreibung",
		"## Angriffsbeschreibung",
		"## Betroffene Produkte",
		"- OpenSSL 3.0",
		"## CVEs",
		"- CVE-2026-1234",
		"## Referenzen",
		"- https://example.com/a",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestFormatMarkdown_OmitsEmptySections(t *testing.T) {
	out := FormatMarkdown(&Advisory{Name: "WID-SEC-1", Title: "Bare"})
	if strings.Contains(out, "## CVEs") || strings.Contains(out, "## Referenzen") || strings.Contains(out, "CVEs:") {
		t.Errorf("empty sections should be omitted:\n%s", out)
	}
	if !strings.Contains(out, "# WID-SEC-1: Bare") {
		t.Errorf("title header always present:\n%s", out)
	}
}

func TestFormatMarkdown_CVSSDecimal(t *testing.T) {
	// WID delivers scores as integers ×10; they must render as conventional decimals.
	out := FormatMarkdown(&Advisory{Name: "WID-SEC-1", Title: "X", BaseScore: "98", TemporalScore: "85"})
	if !strings.Contains(out, "CVSS Base: 9.8") {
		t.Errorf("expected decimal base score 9.8, got:\n%s", out)
	}
	if !strings.Contains(out, "Temporal: 8.5") {
		t.Errorf("expected decimal temporal score 8.5, got:\n%s", out)
	}
	if strings.Contains(out, "CVSS Base: 98") {
		t.Errorf("raw ×10 integer should not appear:\n%s", out)
	}
}

func TestFormatMarkdown_TrimsISODate(t *testing.T) {
	out := FormatMarkdown(&Advisory{Name: "WID-SEC-1", Title: "X", InitialRelease: "2026-06-22T22:00:00.000+00:00"})
	if !strings.Contains(out, "Published: 2026-06-22\n") {
		t.Errorf("expected trimmed date, got:\n%s", out)
	}
	if strings.Contains(out, "T22:00") {
		t.Errorf("ISO time should be trimmed:\n%s", out)
	}
}
