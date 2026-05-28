package prompts

import (
	"strings"
	"testing"
)

// TestPlanQueriesDAGToolAwareSystemPrompt_EmbedsCatalog: every tool
// the caller passed in must show up by name AND description AND
// schema in the rendered prompt. Without this, the LLM has no idea
// what's available and falls back to picking kb_search for
// everything (defeating B3 entirely).
func TestPlanQueriesDAGToolAwareSystemPrompt_EmbedsCatalog(t *testing.T) {
	t.Parallel()
	tools := []PlanToolEntry{
		{Name: "kb_search", Description: "vector search", InputSchemaJSON: `{"type":"object"}`},
		{Name: "calculator", Description: "arithmetic", InputSchemaJSON: `{"type":"object","required":["expression"]}`},
	}
	for _, lang := range []string{"de", "en"} {
		got := PlanQueriesDAGToolAwareSystemPrompt(lang, tools)
		for _, want := range []string{
			"kb_search", "vector search",
			"calculator", "arithmetic",
			"required", // from the calculator schema
		} {
			if !strings.Contains(got, want) {
				t.Errorf("lang=%s: prompt missing %q", lang, want)
			}
		}
	}
}

// TestPlanQueriesDAGToolAwareSystemPrompt_EmptyCatalog: when the
// registry is empty (no tools wired), the prompt must still be
// well-formed and tell the LLM to fall back to kb_search.
func TestPlanQueriesDAGToolAwareSystemPrompt_EmptyCatalog(t *testing.T) {
	t.Parallel()
	got := PlanQueriesDAGToolAwareSystemPrompt("en", nil)
	if !strings.Contains(got, "no tools available") {
		t.Errorf("empty-catalog prompt should hint fallback: %s", got[len(got)-200:])
	}
	if !strings.Contains(got, "kb_search") {
		t.Errorf("kb_search should still be referenced as fallback")
	}
}

// TestPlanQueriesDAGToolAwareSystemPrompt_NewlinesInSchemaCollapse:
// JSON schemas often arrive pretty-printed; embedding them with
// newlines breaks the per-tool block layout. Ensure newlines are
// collapsed.
func TestPlanQueriesDAGToolAwareSystemPrompt_NewlinesInSchemaCollapse(t *testing.T) {
	t.Parallel()
	tools := []PlanToolEntry{
		{Name: "x", Description: "y", InputSchemaJSON: "{\n  \"type\": \"object\"\n}"},
	}
	got := PlanQueriesDAGToolAwareSystemPrompt("en", tools)
	// The schema line should be on a single line — find the schema
	// label and verify the next "\n" is the per-tool separator, not
	// inside the JSON.
	idx := strings.Index(got, "schema:")
	if idx < 0 {
		t.Fatal("schema: marker missing from prompt")
	}
	// Read until next newline; that segment must contain the closing
	// brace of the JSON.
	segment := got[idx:]
	end := strings.Index(segment, "\n")
	if end < 0 {
		t.Fatal("schema line never terminates")
	}
	line := segment[:end]
	if !strings.Contains(line, "}") {
		t.Errorf("schema JSON spread across multiple lines: %q", line)
	}
}
