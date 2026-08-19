package contentgen

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/agentteams"
)

type fakeTeamLoader struct {
	agent *agentteams.AgentRecord
	team  *agentteams.TeamForChat
	err   error
}

func (f fakeTeamLoader) LoadAgentForChat(context.Context, string, string) (*agentteams.AgentRecord, error) {
	return f.agent, f.err
}
func (f fakeTeamLoader) LoadTeamForChat(context.Context, string, string) (*agentteams.TeamForChat, error) {
	return f.team, f.err
}

func TestResolveAnalysisAgentNoSelection(t *testing.T) {
	sel, reason := resolveAnalysisAgent(context.Background(),
		fakeTeamLoader{agent: &agentteams.AgentRecord{ID: "a1"}}, "kb1", "", "")
	if sel != nil {
		t.Errorf("ohne Auswahl darf nichts aufgelöst werden, got %+v", sel)
	}
	if reason != "" {
		t.Errorf("ohne Auswahl darf es keinen Degradationsgrund geben, got %q", reason)
	}
}

func TestResolveAnalysisAgentResolves(t *testing.T) {
	sel, reason := resolveAnalysisAgent(context.Background(),
		fakeTeamLoader{agent: &agentteams.AgentRecord{ID: "a1", Name: "Prüfer"}}, "kb1", "a1", "")
	if sel == nil || sel.Agent == nil || sel.Agent.ID != "a1" {
		t.Fatalf("Agent nicht aufgelöst: %+v", sel)
	}
	if reason != "" {
		t.Errorf("erfolgreiche Auflösung darf keinen Grund melden, got %q", reason)
	}
}

// Fail-soft wie in der Chat-Sendestrecke (resolveTeamSelection): ein
// gelöschter Agent darf den Lauf nicht kosten, muss aber sichtbar sein.
func TestResolveAnalysisAgentFailsSoft(t *testing.T) {
	sel, reason := resolveAnalysisAgent(context.Background(),
		fakeTeamLoader{err: errors.New("not attached")}, "kb1", "a1", "")
	if sel != nil {
		t.Errorf("ein nicht auflösbarer Agent darf keine Auswahl liefern, got %+v", sel)
	}
	if reason == "" {
		t.Error("ein nicht auflösbarer Agent MUSS einen Degradationsgrund melden — sonst degradiert der Lauf stumm")
	}
}

func TestResolveAnalysisAgentPrefersTeam(t *testing.T) {
	sel, _ := resolveAnalysisAgent(context.Background(), fakeTeamLoader{
		team:  &agentteams.TeamForChat{Team: agentteams.TeamRecord{ID: "t1"}, Members: []agentteams.AgentRecord{{ID: "m1"}}},
		agent: &agentteams.AgentRecord{ID: "a1"},
	}, "kb1", "a1", "t1")
	if sel == nil || sel.Team == nil || sel.Team.Team.ID != "t1" {
		t.Fatalf("Team muss den Einzelagenten schlagen: %+v", sel)
	}
}

func TestResolveAnalysisAgentRejectsEmptyTeam(t *testing.T) {
	sel, reason := resolveAnalysisAgent(context.Background(), fakeTeamLoader{
		team: &agentteams.TeamForChat{Team: agentteams.TeamRecord{ID: "t1"}},
	}, "kb1", "", "t1")
	if sel != nil {
		t.Errorf("ein Team ohne Mitglieder darf nicht laufen, got %+v", sel)
	}
	if reason == "" {
		t.Error("ein leeres Team MUSS einen Degradationsgrund melden")
	}
}

// TestResolveAnalysisAgentWithoutLoader covers the "unavailable" arm: the
// handler was wired without SetAgentDeps but the request still names an agent.
// Unreachable in production (routes.go always wires the deps), which is why it
// was deferred — but the arm exists and must keep reporting a reason rather
// than silently running without the agent.
func TestResolveAnalysisAgentWithoutLoader(t *testing.T) {
	sel, reason := resolveAnalysisAgent(context.Background(), nil, "kb1", "a1", "")
	if sel != nil {
		t.Errorf("no loader must yield no selection, got %+v", sel)
	}
	if reason == "" {
		t.Error("no loader MUST report a degradation reason — otherwise the run degrades silently")
	}
}
