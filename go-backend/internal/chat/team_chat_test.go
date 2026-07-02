package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/vector"
)

func teamFixture() TeamParams {
	return TeamParams{
		KbID: "kb1", Query: "what changed", Language: "en",
		Team: agentteams.TeamRecord{ID: "t1", Name: "Sec", MaxAgentsPerTurn: 2},
		Members: []agentteams.AgentRecord{
			{ID: "a1", Name: "Netz", Description: "network"},
			{ID: "a2", Name: "Recht", Description: "legal"},
		},
	}
}

func TestRunTeamChatMergesFindingsAndSetsFinalChunks(t *testing.T) {
	route := func(_ context.Context, candidates []agentteams.AgentRecord, _ int) ([]agentteams.AgentRecord, string, error) {
		return candidates, "both relevant", nil
	}
	specialist := func(_ context.Context, a agentteams.AgentRecord, _ string) (TeamFinding, error) {
		return TeamFinding{
			AgentID: a.ID, AgentName: a.Name,
			Analysis: "finding from " + a.Name,
			Chunks:   []vector.SearchChunk{{ID: "c-" + a.ID, Content: "evidence " + a.Name, Score: 0.8}},
		}, nil
	}
	var events []map[string]any
	emit := func(d map[string]any) { events = append(events, d) }

	chatCtx, err := runTeamChatTestable(context.Background(), route, specialist, teamFixture(), emit)
	if err != nil {
		t.Fatal(err)
	}
	if len(chatCtx.FinalChunks) != 2 {
		t.Fatalf("FinalChunks MUST carry the merged specialist chunks, got %d", len(chatCtx.FinalChunks))
	}
	if !strings.Contains(chatCtx.SystemPrompt, "finding from Netz") ||
		!strings.Contains(chatCtx.SystemPrompt, "finding from Recht") {
		t.Fatal("attributed findings missing from synthesis prompt")
	}
	if !strings.Contains(chatCtx.SystemPrompt, "Netz") {
		t.Fatal("agent attribution missing")
	}
	if len(events) < 3 {
		t.Fatalf("expected plan + per-specialist hops + answer trajectory, got %d events", len(events))
	}
}

func TestRunTeamChatEmptyRouteFallsThrough(t *testing.T) {
	route := func(_ context.Context, _ []agentteams.AgentRecord, _ int) ([]agentteams.AgentRecord, string, error) {
		return nil, "nothing fits", nil
	}
	_, err := runTeamChatTestable(context.Background(), route, nil, teamFixture(), func(map[string]any) {})
	if !errors.Is(err, ErrTeamNoRoute) {
		t.Fatalf("empty selection must return ErrTeamNoRoute, got %v", err)
	}
}

func TestRunTeamChatSingleAgentSkipsRouter(t *testing.T) {
	p := teamFixture()
	p.Members = p.Members[:1]
	route := func(_ context.Context, _ []agentteams.AgentRecord, _ int) ([]agentteams.AgentRecord, string, error) {
		t.Fatal("router must not run for a single agent")
		return nil, "", nil
	}
	specialist := func(_ context.Context, a agentteams.AgentRecord, _ string) (TeamFinding, error) {
		return TeamFinding{AgentID: a.ID, AgentName: a.Name, Analysis: "solo",
			Chunks: []vector.SearchChunk{{ID: "c1", Content: "x", Score: 0.5}}}, nil
	}
	chatCtx, err := runTeamChatTestable(context.Background(), route, specialist, p, func(map[string]any) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(chatCtx.FinalChunks) != 1 {
		t.Fatal("single-agent path must still produce chunks")
	}
}

func TestRunTeamChatAllSpecialistsFailedErrors(t *testing.T) {
	route := func(_ context.Context, c []agentteams.AgentRecord, _ int) ([]agentteams.AgentRecord, string, error) {
		return c, "", nil
	}
	specialist := func(_ context.Context, _ agentteams.AgentRecord, _ string) (TeamFinding, error) {
		return TeamFinding{}, errors.New("boom")
	}
	_, err := runTeamChatTestable(context.Background(), route, specialist, teamFixture(), func(map[string]any) {})
	if err == nil || errors.Is(err, ErrTeamNoRoute) {
		t.Fatalf("all-failed must be a hard error, got %v", err)
	}
}
