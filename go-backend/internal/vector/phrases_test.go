package vector

import (
	"reflect"
	"testing"
)

func TestExtractQuotedPhrases_NoPhrases(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases("foo bar baz")
	if len(p) != 0 {
		t.Errorf("expected no phrases, got %v", p)
	}
	if r != "foo bar baz" {
		t.Errorf("expected remainder unchanged, got %q", r)
	}
}

func TestExtractQuotedPhrases_SinglePhrase(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases(`foo "exact phrase" bar`)
	if !reflect.DeepEqual(p, []string{"exact phrase"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "foo bar" {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_MultiplePhrases(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases(`alpha "phrase one" beta "phrase two" gamma`)
	if !reflect.DeepEqual(p, []string{"phrase one", "phrase two"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "alpha beta gamma" {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_OnlyPhrase(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases(`"§323c StGB"`)
	if !reflect.DeepEqual(p, []string{"§323c StGB"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "" {
		t.Errorf("remainder should be empty, got %q", r)
	}
}

func TestExtractQuotedPhrases_UnmatchedQuoteTreatedLiterally(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases(`hello "incomplete quote`)
	if len(p) != 0 {
		t.Errorf("expected no phrases for unmatched quote, got %v", p)
	}
	if r != `hello "incomplete quote` {
		t.Errorf("expected remainder to keep the dangling quote, got %q", r)
	}
}

func TestExtractQuotedPhrases_EmptyQuotesSkipped(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases(`foo "" bar`)
	if len(p) != 0 {
		t.Errorf("expected no phrases, got %v", p)
	}
	if r != "foo bar" {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_PhraseWithInternalWhitespace(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases(`  "   spaced  phrase   "  trail  `)
	if !reflect.DeepEqual(p, []string{"spaced  phrase"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "trail" {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_EmptyInput(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases("")
	if len(p) != 0 || r != "" {
		t.Errorf("expected empties, got %v / %q", p, r)
	}
}

func TestExtractQuotedPhrases_AcademicCitations(t *testing.T) {
	// Realistic university query: legal cite + free terms.
	t.Parallel()

	p, r := extractQuotedPhrases(`Welche Voraussetzungen hat "§323c StGB" für Notwehr?`)
	if !reflect.DeepEqual(p, []string{"§323c StGB"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "Welche Voraussetzungen hat für Notwehr?" {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_AsciiSingleQuote_ProjectTitle(t *testing.T) {
	t.Parallel()
	p, r := extractQuotedPhrases(`Wer sind die Beteiligten von 'Neue Wege mit Kindern'?`)
	if !reflect.DeepEqual(p, []string{"Neue Wege mit Kindern"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "Wer sind die Beteiligten von ?" {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_AsciiSingleQuote_NotApostrophe(t *testing.T) {
	t.Parallel()
	// "don't" must NOT open a phrase; nothing should be extracted.
	p, r := extractQuotedPhrases(`don't extract this`)
	if len(p) != 0 {
		t.Errorf("expected no phrases, got %v", p)
	}
	if r != `don't extract this` {
		t.Errorf("remainder must be unchanged, got %q", r)
	}
}

func TestExtractQuotedPhrases_AsciiSingleQuote_MixedApostropheAndPhrase(t *testing.T) {
	t.Parallel()
	// "don't" stays as-is; the spaced 'do it' becomes a phrase.
	p, r := extractQuotedPhrases(`don't 'do it' please`)
	if !reflect.DeepEqual(p, []string{"do it"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != `don't please` {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_CurlyDoubleQuotes(t *testing.T) {
	t.Parallel()
	// U+201C left double, U+201D right double.
	p, r := extractQuotedPhrases("foo “exact phrase” bar")
	if !reflect.DeepEqual(p, []string{"exact phrase"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "foo bar" {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_GermanLowQuote(t *testing.T) {
	t.Parallel()
	// U+201E low double + U+201C right pairing (German „...")
	p, r := extractQuotedPhrases("foo „exact phrase“ bar")
	if !reflect.DeepEqual(p, []string{"exact phrase"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "foo bar" {
		t.Errorf("remainder wrong: %q", r)
	}
}

func TestExtractQuotedPhrases_CurlySingleQuotes(t *testing.T) {
	t.Parallel()
	// U+2018 / U+2019.
	p, r := extractQuotedPhrases("foo ‘project name’ bar")
	if !reflect.DeepEqual(p, []string{"project name"}) {
		t.Errorf("phrases wrong: %v", p)
	}
	if r != "foo bar" {
		t.Errorf("remainder wrong: %q", r)
	}
}
