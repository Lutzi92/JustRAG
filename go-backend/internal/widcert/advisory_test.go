package widcert

import (
	"os"
	"testing"
)

func TestParseAdvisory_Fixture(t *testing.T) {
	data, err := os.ReadFile("testdata/advisory.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	adv, err := parseAdvisory("WID-SEC-2026-2038", data)
	if err != nil {
		t.Fatalf("parseAdvisory: %v", err)
	}
	if adv.Name != "WID-SEC-2026-2038" {
		t.Errorf("Name = %q", adv.Name)
	}
	if adv.Title == "" {
		t.Error("Title empty")
	}
	if adv.BaseScore == "" {
		t.Error("BaseScore empty — scoreListe not detected")
	}
	if len(adv.CVEs) == 0 {
		t.Error("CVEs empty — cveIdListe not detected")
	}
	if len(adv.References) == 0 {
		t.Error("References empty — documentReferenceListe not detected")
	}
	if len(adv.Products) == 0 {
		t.Error("Products empty — productReferenceListe not detected")
	}
}

// Order-independence + missing sections must not break the parse.
func TestParseAdvisory_ReorderedAndMissing(t *testing.T) {
	j := []byte(`{
      "properties": {"title": "T", "description": "D"},
      "children": [
        {"type": "scoreListe", "children": [{"properties": {"basescore": "7.5", "temporalscore": "6.5", "classification": "C"}}]},
        {"type": "productReferenceListe", "children": [{"properties": {"name": "ProductA"}}, {"properties": {"name": "ProductB"}}]}
      ]
    }`)
	adv, err := parseAdvisory("WID-SEC-1", j)
	if err != nil {
		t.Fatalf("parseAdvisory: %v", err)
	}
	if adv.BaseScore != "7.5" || adv.TemporalScore != "6.5" || adv.Classification != "C" {
		t.Errorf("scores wrong: %+v", adv)
	}
	if len(adv.Products) != 2 || adv.Products[0] != "ProductA" {
		t.Errorf("products wrong: %+v", adv.Products)
	}
	if len(adv.CVEs) != 0 || len(adv.References) != 0 {
		t.Errorf("expected no CVEs/refs, got %+v / %+v", adv.CVEs, adv.References)
	}
}

// Numeric CVSS scores (JSON numbers) must render without a trailing ".0".
func TestParseAdvisory_NumericScores(t *testing.T) {
	j := []byte(`{"properties":{"title":"T"},"children":[
      {"type":"scoreListe","children":[{"properties":{"basescore":98,"temporalscore":85,"classification":"hoch"}}]}
    ]}`)
	adv, err := parseAdvisory("WID-SEC-2", j)
	if err != nil {
		t.Fatalf("parseAdvisory: %v", err)
	}
	if adv.BaseScore != "98" || adv.TemporalScore != "85" {
		t.Errorf("numeric scores: base=%q temporal=%q", adv.BaseScore, adv.TemporalScore)
	}
}

func TestParseAdvisory_BadJSON(t *testing.T) {
	if _, err := parseAdvisory("X", []byte("not json")); err == nil {
		t.Error("expected error on invalid JSON")
	}
}
