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
		{"Wie viele Gebäude haben Denkmalschutz?", true, false, []TabularLiteral{{"Gebäude", "word"}, {"Denkmalschutz", "word"}}, true},
		{"Welche Räume sind über 100 m²?", false, true, []TabularLiteral{{"Räume", "word"}, {"100", "number"}}, true},
		{"Was ist die BGF von Gebäude 0002001919?", false, false, []TabularLiteral{{"Gebäude", "word"}, {"0002001919", "id"}}, true},
		{"Zeige mir Raum 01.1440.055_.10", false, false, []TabularLiteral{{"Raum", "word"}, {"01.1440.055_.10", "id"}}, true},
		{"Wer ist Ansprechpartner für \"Goethestraße 55\"?", false, false, []TabularLiteral{{"Ansprechpartner", "word"}, {"Goethestraße 55", "quoted"}}, false},
		{"Gibt es Unterlagen zur Goethestraße 55?", false, false, []TabularLiteral{{"Unterlagen", "word"}, {"Goethestraße 55", "span"}}, false},
		{"How many buildings were built between 1950 and 1970?", true, true, []TabularLiteral{{"1950", "number"}, {"1970", "number"}}, true},
		{"Erkläre mir das Brandschutzkonzept.", false, false, []TabularLiteral{{"Brandschutzkonzept", "word"}}, false},
		// R31: "pro " is an aggregation cue; no filter pattern matches here, so filter=false.
		{"Summe der Flächen pro Ressort", true, false, []TabularLiteral{{"Flächen", "word"}, {"Ressort", "word"}}, true},
		// Extra: English filter cues.
		{"Rooms with more than 200 employees", false, true, []TabularLiteral{{"200", "number"}}, true},
		{"Show buildings with at least 300 rooms", false, true, []TabularLiteral{{"300", "number"}}, true},
		// Extra: German low-quote-style quoted literal.
		{"Wo liegt „Musterstraße 12“?", false, false, []TabularLiteral{{"Musterstraße 12", "quoted"}}, false},
		// Extra: sentence-initial capitalised span, not on the stop list —
		// isolates the sentence-initial exclusion from the stop-word one
		// ("Gibt" above is caught by both, so it alone can't prove this).
		// "Musterstraße" is therefore not even a lone-word literal;
		// "Sanierungsplan" is.
		{"Musterstraße 55 ist im Sanierungsplan enthalten", false, false, []TabularLiteral{{"Sanierungsplan", "word"}}, false},

		// Phase-4 acceptance, sst-q17: a German CLOSED COMPOUND carrying
		// the aggregation ("Gesamtbetrag"). boundary() cannot see it —
		// this question was skipped_no_cue in the live run.
		{"Wie hoch ist der Gesamtbetrag (Spalte 'Betrag') aller Gebäude in numbers_formats.xlsx?",
			true, false, []TabularLiteral{{"Gesamtbetrag Spalte", "span"}, {"Betrag", "quoted"}, {"Gebäude", "word"}}, true},
		// Phase-4 acceptance, sst-q36/q37: a row named by a lone
		// capitalised noun. No cue fires on the text alone — the router
		// only fires when the word EXACTLY matches a stored cell value —
		// but the word must at least reach the lookup as a literal.
		{"Wie groß ist die Fläche der Bibliothek laut decimal_comma.csv?",
			false, false, []TabularLiteral{{"Fläche", "word"}, {"Bibliothek", "word"}}, false},
		// ("1252" comes out of the file name cp1252 as a bare number
		// literal — pre-existing behaviour, and harmless: a number fires
		// only on an exact stored-value match.)
		{"Wie groß ist die Fläche der Werkstatt laut bom_semicolon_cp1252.csv?",
			false, false, []TabularLiteral{{"Fläche", "word"}, {"Werkstatt", "word"}, {"1252", "number"}}, false},
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

// TestTabularAggregationCompounds pins the compound-aggregation cue added
// for sst-q17 and, just as importantly, its limits: extending an
// aggregation regex is only safe if prose questions still do NOT become
// aggregations.
//
// Mutation guard: dropping tabularAggCompoundRe from DetectTabularCues
// makes every "want fire" row red; widening its head-noun list to bare
// "zahl", or its modifier arm to \p{L}{1,}, makes "Postleitzahl" /
// "gesamten Anlage" red.
func TestTabularAggregationCompounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		q    string
		want bool
	}{
		// Compounds whose aggregation word is welded to a head noun.
		{"Wie hoch ist der Gesamtbetrag aller Gebäude?", true},
		{"Nenne die Gesamtfläche der Liegenschaft.", true},
		{"Wie hoch sind die Gesamtkosten?", true},
		{"Wie groß ist die Durchschnittsfläche pro Raum?", true},
		{"Nenne die Jahressumme.", true},
		{"Wie hoch ist die Gebäudeanzahl?", true},
		{"Nenne den Flächendurchschnitt.", true},
		{"Nenne den Monatsmittelwert.", true},
		// Negatives: ordinary prose that must NOT become an aggregation.
		{"Wie ist der Zustand des Dachs?", false},
		{"Welche Postleitzahl hat das Gebäude?", false},
		{"Wer ist für die Bezahlung zuständig?", false},
		{"Wie ist der Zustand der gesamten Anlage?", false},
		{"Was steht im Brandschutzkonzept?", false},
		{"Wer ist Ansprechpartner für die Werkstatt?", false},
	}
	for _, c := range cases {
		if got := DetectTabularCues(c.q).Aggregation; got != c.want {
			t.Errorf("%q: aggregation=%v, want %v", c.q, got, c.want)
		}
	}
}

// TestTabularWordLiteralsAreBounded pins the two guards on the lone
// capitalised-word literal added for sst-q36/q37: each one costs a
// tabular_column_values lookup, so short tokens are skipped and the count
// per question is capped.
func TestTabularWordLiteralsAreBounded(t *testing.T) {
	t.Parallel()

	// Short capitalised tokens (< 4 runes) are not worth a lookup.
	for _, lit := range DetectTabularCues("Was ist die BGF der Uni und des Amt?").Literals {
		if lit.Kind == "word" {
			t.Errorf("short token %q must not become a word literal", lit.Text)
		}
	}

	// Six candidate nouns, each isolated by a lower-case word (adjacent
	// capitalised words would form a multi-word span instead), at most
	// tabularMaxWordLiterals kept — the FIRST ones in question order.
	got := DetectTabularCues("Wer verantwortet die Bibliothek und die Werkstatt und die Turnhalle und die Mensa und das Rechenzentrum und die Sporthalle?")
	var words []string
	for _, lit := range got.Literals {
		if lit.Kind == "word" {
			words = append(words, lit.Text)
		}
	}
	if len(words) != tabularMaxWordLiterals {
		t.Fatalf("word literals = %v, want %d of them", words, tabularMaxWordLiterals)
	}
	if words[0] != "Bibliothek" || words[tabularMaxWordLiterals-1] != "Mensa" {
		t.Errorf("cap must keep the first %d in question order, got %v", tabularMaxWordLiterals, words)
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
