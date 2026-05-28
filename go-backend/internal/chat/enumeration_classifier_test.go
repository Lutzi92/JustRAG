package chat

import "testing"

func TestIsEnumerationQuery_German(t *testing.T) {
	t.Parallel()
	cases := []struct {
		query string
		want  bool
	}{
		// True positives — the patterns the structured-extraction pass targets.
		{"In welchen Projekten ist Ralf Siekmann Mitarbeiter?", true},
		{"Welche Projekte hat die Abteilung IT angestoßen?", true},
		{"Wer arbeitet an der Servervirtualisierung?", true},
		{"Wer leitet das Kira-Projekt?", true},
		{"Alle Dokumente, die §323c StGB erwähnen", true},
		{"Liste alle laufenden Projekte auf", true},
		{"Nenne alle Beteiligten der Ausfallsicherheitsinitiative", true},
		{"Zeige mir alle Steckbriefe mit Dr. Weber", true},
		{"Welche Projekte gibt es zur Virtualisierung?", true},
		{"Aufzählung aller Meilensteine", true},

		// True negatives — factoid / explanatory queries don't benefit from the
		// extra LLM call.
		{"Was ist das Kira-Projekt?", false},
		{"Wie funktioniert die Ausfallsicherheit?", false},
		{"Beschreibe den Projektsteckbrief für Max Müller", false},
		{"Fasse das Dokument zusammen", false},
		{"Wann startete das Servervirtualisierungsprojekt?", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			t.Parallel()
			got := IsEnumerationQuery(c.query, "de")
			if got != c.want {
				t.Errorf("IsEnumerationQuery(%q, \"de\") = %v, want %v", c.query, got, c.want)
			}
		})
	}
}

func TestIsEnumerationQuery_English(t *testing.T) {
	t.Parallel()
	cases := []struct {
		query string
		want  bool
	}{
		{"In which projects is Ralf Siekmann involved?", true},
		{"Which projects belong to the IT department?", true},
		{"Who works on server virtualization?", true},
		{"All documents that mention §323c StGB", true},
		{"List all ongoing projects", true},
		{"Name every team member of the Kira project", true},
		{"Show all profiles with Dr. Weber", true},
		{"How many projects are tagged as critical?", true},

		{"What is the Kira project?", false},
		{"How does failover work?", false},
		{"Summarize the Müller profile", false},
		{"Describe the servervirtualization initiative", false},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			t.Parallel()
			got := IsEnumerationQuery(c.query, "en")
			if got != c.want {
				t.Errorf("IsEnumerationQuery(%q, \"en\") = %v, want %v", c.query, got, c.want)
			}
		})
	}
}

// TestIsEnumerationQuery_UnknownLangFallsBackToEnglish: non-de/non-en languages
// map to the English pattern set so the feature degrades gracefully rather
// than being silently disabled on multilingual KBs.
func TestIsEnumerationQuery_UnknownLangFallsBackToEnglish(t *testing.T) {
	t.Parallel()
	if !IsEnumerationQuery("list all projects", "fr") {
		t.Error("fallback to English patterns did not match 'list all projects'")
	}
}
