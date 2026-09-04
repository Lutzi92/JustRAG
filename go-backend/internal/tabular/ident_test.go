package tabular

import "testing"

func TestSanitizeIdentifierTransliterates(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Größe": "groesse", "Fläche [m²]": "flaeche_m2", "Abnutzung (%)": "abnutzung_pct",
		"Betrag in €": "betrag_in_eur", "Straße": "strasse", "1. Halbjahr": "col_1_halbjahr",
		"": "col_", "Stammdaten / Ressort": "stammdaten_ressort", "GIS-Code gesamt": "gis_code_gesamt",
	}
	for in, want := range cases {
		if got := SanitizeIdentifier(in); got != want {
			t.Errorf("SanitizeIdentifier(%q) = %q want %q", in, got, want)
		}
	}
	long := SanitizeIdentifier("Einschätzung aus Nutzersicht / Begründung der Priorisierung und Anmerkungen zum Gebäudezustand")
	if len(long) > 63 {
		t.Errorf("not truncated: %d", len(long))
	}
}

func TestDedupeIdentifiersAndTableName(t *testing.T) {
	t.Parallel()
	got := DedupeIdentifiers([]string{"note", "note", "note", "x"})
	want := []string{"note", "note_2", "note_3", "x"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	if n := TableNameForRegion("0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b", 2, 1); n != "sheet_0f1e2d3c4b5a69788a9b0c1d2e3f4a5b_2_1" {
		t.Errorf("table name %q", n)
	}
}
