package prompts

import (
	"strings"
	"testing"
)

func TestFaithfulnessPrompt_Bilingual(t *testing.T) {
	de := FaithfulnessSystemPrompt("de")
	en := FaithfulnessSystemPrompt("en")
	if !strings.Contains(de, "Kontext") || !strings.Contains(de, "JSON") {
		t.Errorf("DE prompt missing anchors: %s", de)
	}
	if !strings.Contains(en, "context") || !strings.Contains(en, "JSON") {
		t.Errorf("EN prompt missing anchors: %s", en)
	}
}

func TestFaithfulnessUserPrompt_IncludesAllInputs(t *testing.T) {
	p := FaithfulnessUserPrompt("Was sind Gaußsche Verteilungen?", "Die Normalverteilung ist eine Gaußsche Verteilung.", "[1] Kontext zur Normalverteilung.")
	for _, needle := range []string{"Was sind Gaußsche Verteilungen?", "Normalverteilung", "Kontext zur Normalverteilung"} {
		if !strings.Contains(p, needle) {
			t.Errorf("prompt missing %q\nGot: %s", needle, p)
		}
	}
}

func TestAnswerRelevancePrompt_Bilingual(t *testing.T) {
	de := AnswerRelevanceSystemPrompt("de")
	en := AnswerRelevanceSystemPrompt("en")
	for _, want := range []string{"JSON", "score"} {
		if !strings.Contains(de, want) {
			t.Errorf("DE prompt missing %q", want)
		}
		if !strings.Contains(en, want) {
			t.Errorf("EN prompt missing %q", want)
		}
	}
}

func TestContextPrecisionPrompt_Bilingual(t *testing.T) {
	de := ContextPrecisionSystemPrompt("de")
	en := ContextPrecisionSystemPrompt("en")
	for _, want := range []string{"JSON", "relevant"} {
		if !strings.Contains(strings.ToLower(de), strings.ToLower(want)) {
			t.Errorf("DE prompt missing %q", want)
		}
		if !strings.Contains(strings.ToLower(en), strings.ToLower(want)) {
			t.Errorf("EN prompt missing %q", want)
		}
	}
}
