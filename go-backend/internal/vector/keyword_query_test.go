package vector

import (
	"strings"
	"testing"
)

// buildOrTokensExpr is the helper introduced by the BM25 OR-fix. It takes the
// unquoted remainder produced by extractQuotedPhrases and returns a string
// suitable for to_tsquery, where tokens are joined with " | ".

func TestBuildOrTokensExpr_LongGermanQuestion(t *testing.T) {
	t.Parallel()
	in := "Wann fand der erste dokumentierte Workshop des Projekts statt?"
	got, ok := buildOrTokensExpr(in)
	if !ok {
		t.Fatalf("expected ok=true for %q, got ok=false", in)
	}
	want := "Wann | fand | der | erste | dokumentierte | Workshop | des | Projekts | statt"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestBuildOrTokensExpr_PureWhitespace(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "\t\n  "} {
		got, ok := buildOrTokensExpr(in)
		if ok {
			t.Errorf("expected ok=false for %q, got %q", in, got)
		}
		if got != "" {
			t.Errorf("expected empty string for %q, got %q", in, got)
		}
	}
}

func TestBuildOrTokensExpr_OnlyPunctuation(t *testing.T) {
	t.Parallel()
	got, ok := buildOrTokensExpr(`?!,. ;: () "" '' &&`)
	if ok {
		t.Errorf("expected ok=false for punctuation-only input, got %q", got)
	}
}

func TestBuildOrTokensExpr_OperatorInjection(t *testing.T) {
	t.Parallel()
	in := "a | b & c (d) ; drop"
	got, ok := buildOrTokensExpr(in)
	if !ok {
		t.Fatalf("expected ok=true, got ok=false")
	}
	// None of the to_tsquery operator chars may appear OUTSIDE the deliberate ' | ' separator.
	// Strip the deliberate separators first, then assert nothing forbidden remains.
	stripped := strings.ReplaceAll(got, " | ", " ")
	for _, c := range []string{"&", "|", "!", "(", ")", ":", "*", "<"} {
		if strings.Contains(stripped, c) {
			t.Errorf("forbidden char %q leaked into %q (stripped=%q)", c, got, stripped)
		}
	}
	// Content words still survive.
	for _, want := range []string{"a", "b", "c", "d", "drop"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in %q", want, got)
		}
	}
}

func TestBuildOrTokensExpr_HyphenatedTokens(t *testing.T) {
	t.Parallel()
	in := "Support-Chatbot Datenschutz-Beauftragte"
	got, ok := buildOrTokensExpr(in)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	want := "Support-Chatbot | Datenschutz-Beauftragte"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestBuildOrTokensExpr_UnderscoreTokens(t *testing.T) {
	t.Parallel()
	in := "snake_case_id and_another"
	got, ok := buildOrTokensExpr(in)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	want := "snake_case_id | and_another"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestBuildOrTokensExpr_SingleToken(t *testing.T) {
	t.Parallel()
	got, ok := buildOrTokensExpr("Workshop")
	if !ok || got != "Workshop" {
		t.Errorf("expected (\"Workshop\", true), got (%q, %v)", got, ok)
	}
}

func TestBuildOrTokensExpr_UnicodePunctuationAndQuotes(t *testing.T) {
	// Smart quotes, German quote marks, and trailing/leading punctuation must split tokens.
	t.Parallel()

	in := `Was, wie und „warum"?`
	got, ok := buildOrTokensExpr(in)
	if !ok {
		t.Fatalf("expected ok=true, got %q", got)
	}
	for _, want := range []string{"Was", "wie", "und", "warum"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in %q", want, got)
		}
	}
	for _, banned := range []string{"„", `"`, "?", ","} {
		if strings.Contains(got, banned) {
			t.Errorf("expected punctuation %q to be stripped from %q", banned, got)
		}
	}
}

func TestBuildOrTokensExpr_OrderPreserved(t *testing.T) {
	t.Parallel()
	got, ok := buildOrTokensExpr("alpha beta gamma")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if got != "alpha | beta | gamma" {
		t.Errorf("expected order-preserving join, got %q", got)
	}
}

func TestBuildOrTokensExpr_LeadingTrailingBoundaries(t *testing.T) {
	// Leading and trailing token-boundary characters (punctuation, whitespace)
	// must be stripped without producing empty tokens.
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"leading and trailing question marks", "?Workshop?", "Workshop"},
		{"surrounded by spaced hyphens", "  -  foo  -  ", "- | foo | -"},
		{"trailing comma", "alpha, beta", "alpha | beta"},
		{"leading whitespace", "   alpha beta", "alpha | beta"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := buildOrTokensExpr(tc.in)
			if !ok {
				t.Fatalf("expected ok=true for %q", tc.in)
			}
			if got != tc.want {
				t.Errorf("input %q: expected %q, got %q", tc.in, got, tc.want)
			}
		})
	}
}
