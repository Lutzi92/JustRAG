package chat

import "testing"

func TestIsRecencyListingQuery(t *testing.T) {
	positive := []string{
		// The production failure case (2026-07-02).
		"Welche Neuen Meldungen gibt es?",
		"Welche neuen Meldungen gibt es?",
		"Gibt es neue Artikel?",
		"Was gibt es Neues?",
		"was gibt es neues?",
		"Was ist neu?",
		"Zeige mir die neuesten Nachrichten",
		"Welche aktuellen Warnungen liegen vor?",
		"Was wurde zuletzt hinzugefügt?",
		"Welche Dokumente wurden kürzlich hinzugefügt?",
		"Welche Artikel wurden in den letzten 5 Tagen veröffentlicht?",
		"What's new?",
		"What is new?",
		"Are there any new articles?",
		"List the latest advisories",
		"Which documents were recently added?",
		"Show me recent news",
	}
	for _, q := range positive {
		if !IsRecencyListingQuery(q) {
			t.Errorf("expected recency-listing intent for %q", q)
		}
	}

	negative := []string{
		// "neu" as "novel", not "recently ingested" — must NOT fire.
		"Welche neuen Erkenntnisse liefert die Studie?",
		"Welche neuen Funktionen bietet Produkt X laut Handbuch?",
		"Was ist das New-York-Abkommen?",
		"Erkläre den Unterschied zwischen alt und neu",
		// Plain topical questions.
		"Welche Schwachstellen betreffen Citrix NetScaler?",
		"Wie konfiguriere ich den Reverse Proxy?",
		"What are the new employee's responsibilities?",
		"Summarize the document about renewables",
		"",
	}
	for _, q := range negative {
		if IsRecencyListingQuery(q) {
			t.Errorf("expected NO recency-listing intent for %q", q)
		}
	}
}

func TestQueryMentionsNewMarker(t *testing.T) {
	positive := []string{
		"Welche neuen Meldungen gibt es?",
		"Gibt es neue Artikel?",
		"Was ist neu?",
		"Which new advisories are there?",
		"What's new?",
	}
	for _, q := range positive {
		if !queryMentionsNewMarker(q) {
			t.Errorf("expected new-marker mention in %q", q)
		}
	}
	negative := []string{
		// Recency intent without the word "neu"/"new" — a name-marker
		// lookup would be unjustified here.
		"Welche aktuellen Warnungen liegen vor?",
		"Was wurde zuletzt hinzugefügt?",
		"Which documents were recently added?",
		"Welche Meldungen gibt es von heute?",
	}
	for _, q := range negative {
		if queryMentionsNewMarker(q) {
			t.Errorf("expected NO new-marker mention in %q", q)
		}
	}
}

func TestExplicitRecencyWindowDays(t *testing.T) {
	cases := []struct {
		query string
		days  int
		ok    bool
	}{
		{"Welche Artikel wurden in den letzten 5 Tagen veröffentlicht?", 5, true},
		{"Was kam in den letzten 2 Wochen dazu?", 14, true},
		{"neue Meldungen der letzten 3 Monate", 90, true},
		{"What was published in the last 10 days?", 10, true},
		{"new articles from the past 2 weeks", 14, true},
		{"Welche Meldungen gibt es von heute?", 0, true},
		{"Was ist seit gestern neu?", 1, true},
		{"Welche neuen Meldungen gibt es?", 0, false}, // no explicit window
		{"What's new?", 0, false},
	}
	for _, c := range cases {
		days, ok := explicitRecencyWindowDays(c.query)
		if ok != c.ok || (ok && days != c.days) {
			t.Errorf("explicitRecencyWindowDays(%q) = (%d,%v), want (%d,%v)", c.query, days, ok, c.days, c.ok)
		}
	}
}
