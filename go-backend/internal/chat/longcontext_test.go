package chat

import (
	"context"
	"testing"
)

// TestIsGlobalSynthesisQuery_TriggersOnDocumentedPhrases pins the
// keyword classifier's positive set. Adding new triggers is a
// deliberate operator decision (more triggers → higher firing
// rate → higher LLM cost), so every entry on the positive list
// is locked here.
func TestIsGlobalSynthesisQuery_TriggersOnDocumentedPhrases(t *testing.T) {
	t.Parallel()
	positives := []string{
		// English triggers.
		"Please summarise all findings across the documents.",
		"Summarize all the points raised in the meeting notes.",
		"Give me an overview of every document in this corpus.",
		"What are the contradictions in these sources?",
		"Compare all of the approaches mentioned.",
		"Show me the big picture of this material.",
		// German triggers.
		"Fasse alle Befunde aus den Dokumenten zusammen.",
		"Gib mir einen Überblick über alle Dokumente.",
		"Vergleiche alle Vorschläge.",
		"Welche Widersprüche in den Quellen gibt es?",
		"Bitte ein Gesamtbild der Materie.",
	}
	for _, q := range positives {
		if !IsGlobalSynthesisQuery(q) {
			t.Errorf("expected positive match, got false: %q", q)
		}
	}
}

// TestIsGlobalSynthesisQuery_DoesNotFireOnNonSynthesis pins the
// negative set. False positives are operationally costly (30×
// LLM cost when fired), so the classifier must err strongly on
// the side of NOT firing. Each query below contains a near-miss
// keyword but is decidedly NOT global-synthesis intent.
func TestIsGlobalSynthesisQuery_DoesNotFireOnNonSynthesis(t *testing.T) {
	t.Parallel()
	negatives := []string{
		// Common false-positive risks — these contain "all" /
		// "every" but ask about a SPECIFIC piece of information.
		"What is the capital of every German Bundesland?",
		"Show me all error codes that contain 4xx.",
		"List every project that uses kubernetes.",
		"Who attended all of the meetings in Q4?",
		// Pure lookup / enumeration that doesn't deserve the route.
		"What was the budget for project Alpha?",
		"List the team members of the PPM project.",
		"How does CRAG affect query latency?",
		// German equivalents.
		"Was war das Budget für Projekt Alpha?",
		"Welche Projekte verwenden Kubernetes?",
		"Wer hat im November teilgenommen?",
	}
	for _, q := range negatives {
		if IsGlobalSynthesisQuery(q) {
			t.Errorf("expected negative match, got true: %q", q)
		}
	}
}

// TestIsGlobalSynthesisQuery_EmptyAndWhitespace short-circuits to
// false. Empty queries shouldn't trigger any routing; the chat
// handler upstream already rejects them, but the classifier
// must be safe to call defensively.
func TestIsGlobalSynthesisQuery_EmptyAndWhitespace(t *testing.T) {
	t.Parallel()
	for _, q := range []string{"", "   ", "\n\t", " 　"} {
		if IsGlobalSynthesisQuery(q) {
			t.Errorf("empty/whitespace query unexpectedly matched: %q", q)
		}
	}
}

// TestNormaliseForSynthesisMatch pins the normalisation contract.
// Drift here would silently change which queries match — e.g. a
// future refactor that swapped unicode.IsSpace for ASCII-only
// would let the German Geviertraum ( ) confuse the trigger.
func TestNormaliseForSynthesisMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"  Hello   World  ", "hello world"},
		{"Tab\tBetween", "tab between"},
		{"Line\nBreak\nHere", "line break here"},
		{"Non-breaking space", "non-breaking space"},
		{"CASE Preservation? NO.", "case preservation? no."},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		got := normaliseForSynthesisMatch(c.in)
		if got != c.want {
			t.Errorf("normalise(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}

// TestShouldRouteLongContext_GatingOrder pins all three gate
// conditions. Disabled flag, wrong query type, or non-matching
// classifier each block routing.
func TestShouldRouteLongContext_GatingOrder(t *testing.T) {
	t.Parallel()
	enabledReader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_longcontext_enabled": strPtr("true"),
	}}
	disabledReader := &fakeSiteConfigReader{values: map[string]*string{}}

	cases := []struct {
		name      string
		reader    SiteConfigReader
		queryType string
		query     string
		want      bool
	}{
		{"all_gates_pass", enabledReader, "complex_reasoning", "Summarise all findings", true},
		{"flag_off", disabledReader, "complex_reasoning", "Summarise all findings", false},
		{"wrong_query_type_lookup", enabledReader, "lookup", "Summarise all findings", false},
		{"wrong_query_type_enumeration", enabledReader, "enumeration", "Summarise all findings", false},
		{"classifier_does_not_match", enabledReader, "complex_reasoning", "What is the budget of project Alpha?", false},
		{"empty_query", enabledReader, "complex_reasoning", "", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := ShouldRouteLongContext(context.Background(), c.reader, c.queryType, c.query)
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
