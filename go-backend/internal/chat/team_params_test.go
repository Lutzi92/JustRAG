package chat

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/agentteams"
)

// This test pins the field values http_send.go set inline before the
// extraction. It is a characterization test: if it goes red, the extraction
// changed behaviour — the test's expectation is not what's wrong.
func TestBuildTeamParamsMirrorsInlineConstruction(t *testing.T) {
	agent := agentteams.AgentRecord{ID: "a1", Name: "Prüfer"}
	in := TeamParamsInput{
		KbID: "kb1", ChatID: "c1", Query: "Frage", Language: "de",
		CurrentDateLine: "Heute ist der 18.08.2026.", KbSystemPrompt: "KB-Prompt",
		FileIDs: []string{"f1"}, Agent: &agent, SiteCfg: &fakeSiteConfigReader{},
	}
	p := BuildTeamParams(context.Background(), in)

	if p.KbID != "kb1" || p.ChatID != "c1" || p.Query != "Frage" || p.Language != "de" {
		t.Errorf("head fields wrong: %+v", p)
	}
	if p.ToolMaxRounds != 2 {
		t.Errorf("ToolMaxRounds = %d, want 2 (value from http_send.go)", p.ToolMaxRounds)
	}
	if len(p.Members) != 1 || p.Members[0].ID != "a1" {
		t.Errorf("a single agent must run as a one-member team, got %+v", p.Members)
	}
	if p.SearcherForAgent == nil {
		t.Error("SearcherForAgent must be set, otherwise the per-agent retrieval overlay does not apply")
	}
}

func TestBuildTeamParamsPrefersTeamOverAgent(t *testing.T) {
	team := &agentteams.TeamForChat{
		Team:    agentteams.TeamRecord{ID: "t1"},
		Members: []agentteams.AgentRecord{{ID: "m1"}, {ID: "m2"}},
	}
	p := BuildTeamParams(context.Background(), TeamParamsInput{
		KbID: "kb1", Team: team, Agent: &agentteams.AgentRecord{ID: "a1"}, SiteCfg: &fakeSiteConfigReader{},
	})
	if p.Team.ID != "t1" || len(p.Members) != 2 {
		t.Errorf("team must beat the single agent, got Team=%q Members=%d", p.Team.ID, len(p.Members))
	}
}
