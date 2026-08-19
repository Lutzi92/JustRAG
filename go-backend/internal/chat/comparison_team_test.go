package chat

import (
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// promptsComparisonSummary is a thin wrapper around prompts.ComparisonSummaryPrompt
// so the test doesn't need to import prompts twice for one call.
func promptsComparisonSummary(lang string) string {
	return prompts.ComparisonSummaryPrompt(lang)
}

// teamForChatFixture returns a resolved team selection with Team.ID == "t1"
// and a single member — the shape resolveTeamSelection produces for an
// explicit team pick.
func teamForChatFixture() *agentteams.TeamForChat {
	return &agentteams.TeamForChat{
		Team:    agentteams.TeamRecord{ID: "t1", Name: "Vergleichsteam"},
		Members: []agentteams.AgentRecord{{ID: "m1", Name: "Prüfer"}},
	}
}

func TestComparisonSummaryPromptCarriesBothParts(t *testing.T) {
	findings := []Finding{{Mode: "contradiction", Severity: "high", Issue: "Frist weicht ab"}}
	got := comparisonSummaryPromptFor("KB-Prompt", "de", findings)

	for _, want := range []string{"KB-Prompt", "Frist weicht ab"} {
		if !strings.Contains(got, want) {
			t.Errorf("System-Prompt enthält %q nicht:\n%s", want, got)
		}
	}
	if !strings.Contains(got, strings.SplitN(promptsComparisonSummary("de"), "\n", 2)[0]) {
		t.Error("System-Prompt enthält den ComparisonSummaryPrompt nicht")
	}
}

// Ohne KB-Prompt darf keine leere Leerzeile-Kaskade entstehen.
func TestComparisonSummaryPromptWithoutKbPrompt(t *testing.T) {
	got := comparisonSummaryPromptFor("", "de", nil)
	if strings.HasPrefix(got, "\n") {
		t.Errorf("führende Leerzeile bei leerem KB-Prompt:\n%q", got)
	}
}

// Die Spec verlangt, dass die Quellenliste beide Stufen sieht. Ohne den
// Merge fielen alle Fundstellen der Vergleichsstufe hinten raus, weil
// RunTeamChat seine eigenen FinalChunks setzt.
func TestMergeComparisonAndTeamChunks(t *testing.T) {
	cmp := []vector.SearchChunk{{ID: "c1"}, {ID: "c2"}}
	team := []vector.SearchChunk{{ID: "t1"}}

	merged := mergeComparisonChunks(team, cmp)

	ids := map[string]bool{}
	for _, c := range merged {
		ids[c.ID] = true
	}
	for _, want := range []string{"c1", "c2", "t1"} {
		if !ids[want] {
			t.Errorf("Chunk %q fehlt im Merge — %+v", want, merged)
		}
	}
}

func TestAttributionIDsCountsComparisonTeamTurns(t *testing.T) {
	sel := &teamSelection{team: teamForChatFixture()}

	if id, _ := attributionIDs(false, sel); id != nil {
		t.Error("ohne Team-Lauf darf nichts attribuiert werden")
	}
	// Der Vergleichs-Turn, dessen Zusammenfassung ein Team geschrieben hat,
	// MUSS attribuiert werden — bis 2026-08 tat er es bewusst nicht, weil das
	// Team damals gar nicht zum Zug kam.
	id, _ := attributionIDs(true, sel)
	if id == nil || *id != "t1" {
		t.Errorf("Team-ID = %v, want t1", id)
	}
}

func TestTeamAuthoredTurn(t *testing.T) {
	if !teamAuthoredTurn(OrchTeam, false) {
		t.Error("OrchTeam muss zählen")
	}
	if !teamAuthoredTurn(OrchComparison, true) {
		t.Error("Vergleich mit Team-Summary muss zählen")
	}
	if teamAuthoredTurn(OrchComparison, false) {
		t.Error("Vergleich ohne Team-Summary darf nicht zählen")
	}
	if teamAuthoredTurn(OrchStandard, false) {
		t.Error("Standardpfad darf nicht zählen")
	}
}
