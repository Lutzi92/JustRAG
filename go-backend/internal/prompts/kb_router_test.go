package prompts

import (
	"strings"
	"testing"
)

// TestKBRouterUserPrompt_RendersAllCandidates ensures every candidate
// makes it into the prompt and the description "(no description)"
// fallback fires for empty descriptions. Without it, the LLM ranks
// purely on KB names which is much weaker.
func TestKBRouterUserPrompt_RendersAllCandidates(t *testing.T) {
	t.Parallel()
	cands := []KBRouterCandidate{
		{ID: "kb-1", Name: "Verträge", Description: "Vertragsdokumente und SLAs"},
		{ID: "kb-2", Name: "Richtlinien", Description: ""},
	}
	got := KBRouterUserPrompt("Welche Kündigungsfrist hat der Müller-Vertrag?", cands)

	for _, want := range []string{
		"Welche Kündigungsfrist", // query
		"kb-1", "kb-2",           // both ids
		"Verträge", "Richtlinien", // both names
		"Vertragsdokumente und SLAs", // non-empty description
		"(no description)",           // fallback for the empty one
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered prompt missing %q\n--- prompt ---\n%s", want, got)
		}
	}
}

// TestKBRouterSystemPrompt_StableShape: the system prompt is the
// natural caching boundary. Tests pin the contract that it does NOT
// embed the per-call query (which would break caching) and DOES
// instruct the LLM to emit empty matches when no KB fits.
func TestKBRouterSystemPrompt_StableShape(t *testing.T) {
	t.Parallel()
	p := KBRouterSystemPrompt()
	if strings.Contains(strings.ToLower(p), "query:") {
		t.Error("system prompt must not contain the query — that breaks caching")
	}
	for _, must := range []string{"matches", "kb_id", "confidence"} {
		if !strings.Contains(p, must) {
			t.Errorf("system prompt missing %q (the JSON contract)", must)
		}
	}
}
