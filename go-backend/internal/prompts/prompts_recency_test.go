package prompts

import (
	"strings"
	"testing"
)

func TestRecencyListingAddendum(t *testing.T) {
	entries := []string{
		"NEU Citrix NetScaler Schwachstellen_ab12cd34ef567890.md (added 2026-07-01)",
		"NEU OpenSSL Advisory_1234567890abcdef.md (added 2026-06-30)",
	}

	de := RecencyListingAddendum("de", entries, "2026-06-25", false)
	for _, want := range []string{
		"NEU Citrix NetScaler Schwachstellen_ab12cd34ef567890.md",
		"NEU OpenSSL Advisory_1234567890abcdef.md",
		"2026-06-25", // the window start date must be stated
	} {
		if !strings.Contains(de, want) {
			t.Errorf("de addendum missing %q:\n%s", want, de)
		}
	}

	en := RecencyListingAddendum("en", entries, "2026-06-25", false)
	if !strings.Contains(en, "NEU Citrix NetScaler Schwachstellen_ab12cd34ef567890.md") {
		t.Errorf("en addendum missing entry:\n%s", en)
	}

	// Both languages must explain name status labels (NEU/UPDATE): in
	// corpora like CERT advisories, "neue Meldungen" can target the
	// labeled subset, not ingest recency.
	if !strings.Contains(de, "UPDATE") {
		t.Errorf("de addendum missing name-label guidance:\n%s", de)
	}
	if !strings.Contains(en, "UPDATE") {
		t.Errorf("en addendum missing name-label guidance:\n%s", en)
	}

	// Empty listing → the addendum must instruct the model to say
	// nothing new was added, not to improvise from the raw chunks.
	deEmpty := RecencyListingAddendum("de", nil, "2026-06-25", false)
	if !strings.Contains(deEmpty, "keine") {
		t.Errorf("de empty addendum must state that nothing is new:\n%s", deEmpty)
	}
	enEmpty := RecencyListingAddendum("en", nil, "2026-06-25", false)
	if !strings.Contains(strings.ToLower(enEmpty), "no documents") {
		t.Errorf("en empty addendum must state that nothing is new:\n%s", enEmpty)
	}

	// Truncated listing → must disclose the cap so the model doesn't
	// present a truncated list as complete.
	deTrunc := RecencyListingAddendum("de", entries, "2026-06-25", true)
	if !strings.Contains(deTrunc, "unvollständig") {
		t.Errorf("de truncated addendum must disclose truncation:\n%s", deTrunc)
	}
	enTrunc := RecencyListingAddendum("en", entries, "2026-06-25", true)
	if !strings.Contains(strings.ToLower(enTrunc), "incomplete") {
		t.Errorf("en truncated addendum must disclose truncation:\n%s", enTrunc)
	}
}
