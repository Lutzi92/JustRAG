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

// TestJudgePromptsCarryJSONHygiene pins the W6-R4 JSON-hygiene line: all
// eight judge system prompts (four metrics x two languages) must instruct
// the model to escape newlines inside strings and avoid trailing commas —
// the two shapes that actually caused live decoder failures (Wave 5, fix
// wave finding F4). Losing this line on any one prompt would silently widen
// the retry rate for that metric.
func TestJudgePromptsCarryJSONHygiene(t *testing.T) {
	deWant := "nachgestellten Kommas"
	enWant := "trailing commas"

	dePrompts := map[string]string{
		"faithfulness":      FaithfulnessSystemPrompt("de"),
		"answer_relevance":  AnswerRelevanceSystemPrompt("de"),
		"context_precision": ContextPrecisionSystemPrompt("de"),
		"coverage":          CoverageSystemPrompt("de"),
	}
	enPrompts := map[string]string{
		"faithfulness":      FaithfulnessSystemPrompt("en"),
		"answer_relevance":  AnswerRelevanceSystemPrompt("en"),
		"context_precision": ContextPrecisionSystemPrompt("en"),
		"coverage":          CoverageSystemPrompt("en"),
	}
	for name, p := range dePrompts {
		if !strings.Contains(p, deWant) {
			t.Errorf("DE %s prompt missing %q:\n%s", name, deWant, p)
		}
	}
	for name, p := range enPrompts {
		if !strings.Contains(p, enWant) {
			t.Errorf("EN %s prompt missing %q:\n%s", name, enWant, p)
		}
	}
}
