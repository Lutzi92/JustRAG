package chat

import "testing"

func TestIsCorpusComparisonQuery(t *testing.T) {
	positives := []struct{ q, lang string }{
		{"please prepare a comparative report that also gives a table listing all the farms in the case studies", "en"},
		{"compare all the farms and put them in a table", "en"},
		{"give me a table comparing every document", "en"},
		{"for each case study, list the herd size in a table", "en"},
		{"erstelle eine vergleichstabelle aller höfe", "de"},
		{"vergleiche alle dokumente in einer tabelle", "de"},
	}
	for _, p := range positives {
		if !IsCorpusComparisonQuery(p.q, p.lang) {
			t.Errorf("expected positive: %q (%s)", p.q, p.lang)
		}
	}
	negatives := []string{
		"what is the herd size of the Genofond farm?",
		"summarize the Genofond case study",
		"compare these two sentences for me",
		"how do I format a table in markdown?",
		"list all the steps to reset my password",
		"compare this table to that one and highlight differences",
		"can you make a table for each section of this report",
		"compare the two farms KZ 1 and KZ 2 in a table",
	}
	for _, q := range negatives {
		if IsCorpusComparisonQuery(q, "en") {
			t.Errorf("expected negative: %q", q)
		}
	}
}
