package vector

import (
	"reflect"
	"testing"
)

func TestExtractEmailLiterals_PlainEmail(t *testing.T) {
	t.Parallel()
	emails, rest := extractEmailLiterals(`Wer ist ppm@uni-giessen.de?`)
	if !reflect.DeepEqual(emails, []string{"ppm@uni-giessen.de"}) {
		t.Errorf("emails wrong: %v", emails)
	}
	if rest != `Wer ist ?` {
		t.Errorf("rest wrong: %q", rest)
	}
}

func TestExtractEmailLiterals_NoEmail(t *testing.T) {
	t.Parallel()
	emails, rest := extractEmailLiterals(`Welche Beteiligten hat das Projekt?`)
	if len(emails) != 0 {
		t.Errorf("expected no emails, got %v", emails)
	}
	if rest != `Welche Beteiligten hat das Projekt?` {
		t.Errorf("rest changed unexpectedly: %q", rest)
	}
}

func TestExtractEmailLiterals_MultipleEmails(t *testing.T) {
	t.Parallel()
	emails, rest := extractEmailLiterals(`mail to a@b.de und c.d-e@x.uni-giessen.de bitte`)
	if !reflect.DeepEqual(emails, []string{"a@b.de", "c.d-e@x.uni-giessen.de"}) {
		t.Errorf("emails wrong: %v", emails)
	}
	if rest != `mail to  und  bitte` {
		// Note: regex Replace leaves a doubled space; downstream Fields-join collapses it.
		t.Errorf("rest wrong: %q", rest)
	}
}

func TestExtractEmailLiterals_TrailingPunctuation(t *testing.T) {
	t.Parallel()
	// Trailing `?` is NOT part of the email; must not end up inside the literal.
	emails, _ := extractEmailLiterals(`Kontakt: ppm@uni-giessen.de?`)
	if !reflect.DeepEqual(emails, []string{"ppm@uni-giessen.de"}) {
		t.Errorf("emails wrong: %v", emails)
	}
}

func TestExtractEmailLiterals_NotAnEmail_BareAt(t *testing.T) {
	t.Parallel()
	// Stray @ (e.g. "@team") must not be promoted.
	emails, rest := extractEmailLiterals(`@team meeting`)
	if len(emails) != 0 {
		t.Errorf("expected no emails, got %v", emails)
	}
	if rest != `@team meeting` {
		t.Errorf("rest wrong: %q", rest)
	}
}

// TestExtractEmailLiterals_AfterPhraseExtraction documents the call order
// runKeywordSearch uses: phrases first, emails second. An email inside a
// quoted phrase must NOT be doubly extracted.
func TestExtractEmailLiterals_AfterPhraseExtraction(t *testing.T) {
	t.Parallel()
	phrases, remainder := extractQuotedPhrases(`Welche "Adresse" hat ppm@uni-giessen.de`)
	emails, rest := extractEmailLiterals(remainder)
	if !reflect.DeepEqual(phrases, []string{"Adresse"}) {
		t.Errorf("phrases wrong: %v", phrases)
	}
	if !reflect.DeepEqual(emails, []string{"ppm@uni-giessen.de"}) {
		t.Errorf("emails wrong: %v", emails)
	}
	// rest after both passes: extractQuotedPhrases collapses whitespace so
	// remainder is "Welche hat ppm@uni-giessen.de"; the email pass removes
	// the email (replaces with ""), leaving "Welche hat " (one trailing space
	// — the space that was before the email in the input).
	if rest != "Welche hat " {
		t.Errorf("rest wrong: %q", rest)
	}
}
