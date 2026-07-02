package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
)

func routerAgents() []agentteams.AgentRecord {
	return []agentteams.AgentRecord{
		{ID: "0d9c8b7a-1111-4111-8111-111111111111", Name: "Netz", Description: "network security advisories"},
		{ID: "0d9c8b7a-2222-4222-8222-222222222222", Name: "Recht", Description: "legal and compliance topics"},
		{ID: "0d9c8b7a-3333-4333-8333-333333333333", Name: "Formate", Description: "document formatting"},
	}
}

func TestRouteTeamSelectsAndCaps(t *testing.T) {
	fn := func(_ context.Context, prompt, system, kbID, model string, spec *ai.StructuredSpec) (string, error) {
		if !strings.Contains(prompt, "network security advisories") {
			t.Fatal("agent cards missing from router prompt")
		}
		if spec == nil || len(spec.Schema) == 0 {
			t.Fatal("router must send a structured spec")
		}
		return `{"selected_agent_ids":["0d9c8b7a-2222-4222-8222-222222222222","0d9c8b7a-1111-4111-8111-111111111111","0d9c8b7a-3333-4333-8333-333333333333"],"reasoning":"all"}`, nil
	}
	sel, reason, err := routeTeam(context.Background(), fn, "kb1", "fast-model", "en", "q", routerAgents(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel) != 2 {
		t.Fatalf("maxSelect must cap selection, got %d", len(sel))
	}
	if sel[0].Name != "Recht" || sel[1].Name != "Netz" {
		t.Fatalf("selection order must follow router output, got %s/%s", sel[0].Name, sel[1].Name)
	}
	if reason != "all" {
		t.Fatalf("reasoning lost: %q", reason)
	}
}

func TestRouteTeamIgnoresUnknownIDsAndEmptySelection(t *testing.T) {
	fn := func(_ context.Context, _, _, _, _ string, _ *ai.StructuredSpec) (string, error) {
		return `{"selected_agent_ids":["not-a-real-id"],"reasoning":"none fit"}`, nil
	}
	sel, _, err := routeTeam(context.Background(), fn, "kb1", "m", "de", "q", routerAgents(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel) != 0 {
		t.Fatalf("unknown ids must be dropped, got %d selections", len(sel))
	}
}

func TestRouteTeamPropagatesLLMError(t *testing.T) {
	fn := func(_ context.Context, _, _, _, _ string, _ *ai.StructuredSpec) (string, error) {
		return "", errors.New("backend down")
	}
	_, _, err := routeTeam(context.Background(), fn, "kb1", "m", "de", "q", routerAgents(), 3)
	if err == nil {
		t.Fatal("LLM error must propagate (caller falls back to standard path)")
	}
}
