package vector

import (
	"reflect"
	"testing"
)

func TestExtractProperNounPhrases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "german role lookup with two-token name",
			in:   "Welche Rolle übernimmt Eberhard Kurz?",
			want: []string{"Eberhard Kurz"},
		},
		{
			name: "german who-question with two-token name",
			in:   "Wer ist Marcus Enger?",
			want: []string{"Marcus Enger"},
		},
		{
			name: "no two-token title-cased run",
			in:   "Was ist die Projekt-ID?",
			want: nil,
		},
		{
			name: "multiple distinct phrases",
			in:   "Welche Rollen haben Eberhard Kurz und Marcus Enger?",
			want: []string{"Eberhard Kurz", "Marcus Enger"},
		},
		{
			name: "comma breaks run",
			in:   "Eberhard, du bist toll",
			want: nil,
		},
		{
			name: "period breaks run between sentences",
			in:   "Eberhard. Kurz ist da",
			want: nil,
		},
		{
			name: "three-token name kept whole",
			in:   "Wer ist Karl Heinz Müller",
			want: []string{"Karl Heinz Müller"},
		},
		{
			name: "trailing question mark does not corrupt last token",
			in:   "Wer ist Eberhard Kurz?",
			want: []string{"Eberhard Kurz"},
		},
		{
			name: "stopword inside the query is dropped, name still extracted",
			in:   "Was hat Eberhard Kurz gesagt?",
			want: []string{"Eberhard Kurz"},
		},
		{
			name: "english query",
			in:   "What does Eberhard Kurz do?",
			want: []string{"Eberhard Kurz"},
		},
		{
			name: "single-token title-cased not extracted",
			in:   "Wo arbeitet Kurz?",
			want: nil,
		},
		{
			name: "german noun without name not extracted",
			in:   "Was kostet das Auto?",
			want: nil,
		},
		{
			name: "honorific plus surname",
			in:   "Wie heißt Herr Kurz?",
			want: []string{"Herr Kurz"},
		},
		{
			name: "empty string",
			in:   "",
			want: nil,
		},
		{
			name: "whitespace only",
			in:   "   ",
			want: nil,
		},
		{
			name: "umlaut in name (Müller)",
			in:   "Wer ist Müller Schmidt?",
			want: []string{"Müller Schmidt"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractProperNounPhrases(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("extractProperNounPhrases(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// Guards the rune-boundary behaviour of isProperNounToken, which runs per
// token per query and therefore decodes only the first rune rather than
// materialising a []rune.
func TestIsProperNounToken(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"A", false},          // single rune — too short
		{"Ä", false},          // single multi-byte rune — also too short
		{"Ab", true},          // two ASCII runes
		{"Äh", true},          // leading multi-byte uppercase
		{"Öl", true},          // German two-rune word
		{"ab", false},         // lowercase leader
		{"äh", false},         // lowercase multi-byte leader
		{"Digital-PPM", true}, // hyphen inside is allowed
		{"123", false},        // digit leader is not uppercase
		{"\xff\xfe", false},   // invalid UTF-8 must not panic or qualify
	}
	for _, tc := range cases {
		if got := isProperNounToken(tc.in); got != tc.want {
			t.Errorf("isProperNounToken(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsEntityAskingQuery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// fires — German role-asking patterns
		{"welche rolle", "Welche Rolle übernimmt Eberhard Kurz?", true},
		{"welche funktion", "Welche Funktion hat Frau Müller?", true},
		{"welche aufgabe", "Welche Aufgabe hat Marcus Enger?", true},
		{"welche position", "Welche Position bekleidet Bostick?", true},
		{"in welcher rolle", "In welcher Rolle arbeitet Eberhard Kurz?", true},

		// fires — German wh-interrogatives at sentence start
		{"wer prefix", "Wer ist Marcus Enger?", true},
		{"wer with leading whitespace", "  Wer ist Marcus Enger?", true},
		{"wessen prefix", "Wessen Aufgabe ist das?", true},

		// fires — English equivalents
		{"who prefix", "Who is Eberhard Kurz?", true},
		{"what role", "What role does Eberhard Kurz play?", true},

		// does NOT fire — common false-positive patterns from the q085fix eval
		{"welche projekte (no role)", "Welche Projekte aus dem Portfolio befassen sich mit Künstlicher Intelligenz?", false},
		{"welche ki-bezogenen", "Welche KI-bezogenen Projekte aus dem Digital-PPM haben bereits Resultate?", false},
		{"welche hochschulseiten", "Wie viele Best-Practice-Hochschulseiten zählt das Projekt?", false},
		{"was kostet", "Was kostet die Lizenz?", false},
		{"wann fand", "Wann fand der Workshop statt?", false},
		{"wo arbeitet (intentionally not gated)", "Wo arbeitet das Team?", false},

		// does NOT fire — empty / whitespace
		{"empty", "", false},
		{"whitespace", "   ", false},

		// does NOT fire — interrogative as substring not at start
		{"wer mid-string", "Sagen Sie mir, wer ist Eberhard Kurz?", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isEntityAskingQuery(c.in); got != c.want {
				t.Errorf("isEntityAskingQuery(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
