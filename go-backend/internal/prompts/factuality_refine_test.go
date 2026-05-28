package prompts

import (
	"strings"
	"testing"
)

// TestFactualityRefineSystemPrompt_ModeDistinct confirms surgical and
// holistic produce materially different system prompts. If the two
// converge, the adaptive switch in ai.SelectRefineMode becomes a no-op
// and the eval gate would silently lose its lever.
func TestFactualityRefineSystemPrompt_ModeDistinct(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"de", "en"} {
		surgical := FactualityRefineSystemPrompt(lang, RefineModeSurgical)
		holistic := FactualityRefineSystemPrompt(lang, RefineModeHolistic)
		if surgical == holistic {
			t.Errorf("lang=%s: surgical and holistic produced identical prompts", lang)
		}
		if surgical == "" || holistic == "" {
			t.Errorf("lang=%s: empty prompt (surgical=%d holistic=%d)", lang, len(surgical), len(holistic))
		}
	}
}

// TestFactualityRefineSystemPrompt_SurgicalKeepsCitations: the surgical
// prompt MUST instruct the LLM to keep [N] citations byte-for-byte;
// changing them between passes invalidates the verifier's second-round
// mapping.
func TestFactualityRefineSystemPrompt_SurgicalKeepsCitations(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"de", "en"} {
		p := FactualityRefineSystemPrompt(lang, RefineModeSurgical)
		if !strings.Contains(p, "[N]") {
			t.Errorf("lang=%s: surgical prompt does not mention [N] citation marker", lang)
		}
	}
}

// TestFactualityRefineUserPrompt_ContainsAllPieces ensures every
// component the prompt claims to deliver actually shows up in the
// rendered string. A missing source block or claims block silently
// degrades refine quality.
func TestFactualityRefineUserPrompt_ContainsAllPieces(t *testing.T) {
	t.Parallel()
	claims := []FlaggedClaimPromptInput{
		{ClaimText: "X tut Y", Reason: "unsupported", CitedN: 3},
		{ClaimText: "Z ist eine Zahl", Reason: "contradicted", CitedN: 0},
	}
	got := FactualityRefineUserPrompt(
		"Wie funktioniert X?",
		"Antwort mit Quelle [3] und mehr.",
		"[1] erste Quelle\n[3] dritte Quelle",
		claims,
	)
	for _, want := range []string{
		"Wie funktioniert X?",
		"Antwort mit Quelle [3]",
		"erste Quelle",
		"dritte Quelle",
		"X tut Y",
		"Z ist eine Zahl",
		"unsupported",
		"contradicted",
		"[3]",
		"[0]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered prompt missing %q\n--- prompt ---\n%s", want, got)
		}
	}
}

// TestFactualityRefineUserPrompt_NewlineInClaimQuoted: a flagged claim
// whose text contains a literal newline must not break the
// per-line bullet structure — newlines get collapsed to a space so the
// LLM still reads one bullet per claim.
func TestFactualityRefineUserPrompt_NewlineInClaimQuoted(t *testing.T) {
	t.Parallel()
	claims := []FlaggedClaimPromptInput{
		{ClaimText: "line one\nline two", Reason: "unsupported", CitedN: 1},
	}
	got := FactualityRefineUserPrompt("q", "a", "s", claims)
	if strings.Contains(got, "line one\nline two") {
		t.Error("multi-line claim text was not collapsed; the bullet list will desynchronise")
	}
	if !strings.Contains(got, "line one line two") {
		t.Errorf("claim text missing after newline collapse:\n%s", got)
	}
}
