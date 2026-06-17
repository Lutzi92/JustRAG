package prompts

import (
	"strings"
	"testing"
)

func TestComparisonModePromptCoversModes(t *testing.T) {
	for _, mode := range []string{"contradiction", "formal", "completeness"} {
		for _, lang := range []string{"de", "en"} {
			p := ComparisonModePrompt(mode, lang)
			if len(p) < 40 {
				t.Fatalf("prompt too short for mode=%s lang=%s", mode, lang)
			}
		}
	}
}

func TestComparisonModePromptUnknownModeFallsBack(t *testing.T) {
	if ComparisonModePrompt("bogus", "en") == "" {
		t.Fatal("unknown mode should return a non-empty generic prompt")
	}
}

func TestComparisonSummaryPrompt(t *testing.T) {
	if !strings.Contains(strings.ToLower(ComparisonSummaryPrompt("en")), "summar") {
		t.Fatal("expected summary instruction")
	}
}
