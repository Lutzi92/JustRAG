package chat

import (
	"reflect"
	"testing"
)

func TestDetectTabularCues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		q            string
		agg, filter  bool
		wantLiterals []TabularLiteral
		fired        bool
	}{
		{"Wie viele Gebäude haben Denkmalschutz?", true, false, nil, true},
		{"Welche Räume sind über 100 m²?", false, true, []TabularLiteral{{"100", "number"}}, true},
		{"Was ist die BGF von Gebäude 0002001919?", false, false, []TabularLiteral{{"0002001919", "id"}}, true},
		{"Zeige mir Raum 01.1440.055_.10", false, false, []TabularLiteral{{"01.1440.055_.10", "id"}}, true},
		{"Wer ist Ansprechpartner für \"Goethestraße 55\"?", false, false, []TabularLiteral{{"Goethestraße 55", "quoted"}}, false},
		{"Gibt es Unterlagen zur Goethestraße 55?", false, false, []TabularLiteral{{"Goethestraße 55", "span"}}, false},
		{"How many buildings were built between 1950 and 1970?", true, true, []TabularLiteral{{"1950", "number"}, {"1970", "number"}}, true},
		{"Erkläre mir das Brandschutzkonzept.", false, false, nil, false},
		// R31: "pro " is an aggregation cue; no filter pattern matches here, so filter=false.
		{"Summe der Flächen pro Ressort", true, false, nil, true},
		// Extra: English filter cues.
		{"Rooms with more than 200 employees", false, true, []TabularLiteral{{"200", "number"}}, true},
		{"Show buildings with at least 300 rooms", false, true, []TabularLiteral{{"300", "number"}}, true},
		// Extra: German low-quote-style quoted literal.
		{"Wo liegt „Musterstraße 12“?", false, false, []TabularLiteral{{"Musterstraße 12", "quoted"}}, false},
		// Extra: sentence-initial capitalised span, not on the stop list —
		// isolates the sentence-initial exclusion from the stop-word one
		// ("Gibt" above is caught by both, so it alone can't prove this).
		{"Musterstraße 55 ist im Sanierungsplan enthalten", false, false, nil, false},
	}
	for _, c := range cases {
		got := DetectTabularCues(c.q)
		if got.Aggregation != c.agg || got.Filter != c.filter || got.Fired() != c.fired {
			t.Errorf("%q: agg=%v filter=%v fired=%v (want %v %v %v)", c.q, got.Aggregation, got.Filter, got.Fired(), c.agg, c.filter, c.fired)
		}
		if !reflect.DeepEqual(got.Literals, c.wantLiterals) {
			t.Errorf("%q: literals=%+v want %+v", c.q, got.Literals, c.wantLiterals)
		}
	}
}

func TestPromoteIdentifierPhrases(t *testing.T) {
	t.Parallel()
	c := DetectTabularCues(`Fläche von Raum 01.1440.055_.10 und "0002001919"`)
	got := PromoteIdentifierPhrases(`Fläche von Raum 01.1440.055_.10 und "0002001919"`, c.Literals)
	if got != `Fläche von Raum "01.1440.055_.10" und "0002001919"` {
		t.Fatalf("got %q", got)
	}

	// Directly exercises the "not already inside double quotes" guard: an
	// id-kind literal whose text already sits inside quotes (constructed by
	// hand, since DetectTabularCues itself always classifies quoted text as
	// Kind "quoted" and so never hands PromoteIdentifierPhrases an "id"
	// literal that's already quoted) must not be double-wrapped.
	already := `Fläche von "01.1440.055_.10"`
	gotAlready := PromoteIdentifierPhrases(already, []TabularLiteral{{Text: "01.1440.055_.10", Kind: "id"}})
	if gotAlready != already {
		t.Fatalf("got %q, want unchanged %q", gotAlready, already)
	}
}
