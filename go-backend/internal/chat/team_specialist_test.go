package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/agents"
	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

func specialistAgent() agentteams.AgentRecord {
	return agentteams.AgentRecord{
		ID: "a1", Name: "Netz", Icon: "shield",
		Description:  "network advisories",
		SystemPrompt: "Focus on CVE severity.",
	}
}

func TestRunTeamSpecialistNoTools(t *testing.T) {
	deps := teamSpecialistDeps{
		retrieve: func(_ context.Context, _ agentteams.AgentRecord, in agents.Input) (agents.Output, error) {
			if in.Query != "what changed" {
				t.Fatalf("query not forwarded: %q", in.Query)
			}
			return agents.Output{Chunks: []vector.SearchChunk{
				{ID: "c1", Content: "CVE-2026-1 critical", Score: 0.9, FileName: "advisory.md"},
			}}, nil
		},
		structured: func(_ context.Context, prompt, system, _, model string, spec *ai.StructuredSpec) (string, error) {
			if !strings.Contains(system, "Focus on CVE severity.") {
				t.Fatal("persona missing from specialist system prompt")
			}
			if !strings.Contains(system, "AGENT PERSONA") {
				t.Fatal("persona must be spotlighted/delimited")
			}
			if !strings.Contains(prompt, "CVE-2026-1 critical") {
				t.Fatal("retrieved context missing from specialist prompt")
			}
			return `{"analysis":"One critical CVE found."}`, nil
		},
	}
	p := TeamParams{KbID: "kb1", Language: "en"}
	f, err := runTeamSpecialist(context.Background(), deps, p, specialistAgent(), "what changed")
	if err != nil {
		t.Fatal(err)
	}
	if f.Analysis != "One critical CVE found." {
		t.Fatalf("analysis wrong: %q", f.Analysis)
	}
	if len(f.Chunks) != 1 || f.Chunks[0].ID != "c1" {
		t.Fatal("chunks must ride along on the finding")
	}
	if f.AgentName != "Netz" {
		t.Fatal("finding must carry the agent name for attribution")
	}
}

func TestRunTeamSpecialistEmptyRetrievalStillReports(t *testing.T) {
	deps := teamSpecialistDeps{
		retrieve: func(_ context.Context, _ agentteams.AgentRecord, _ agents.Input) (agents.Output, error) {
			return agents.Output{}, nil
		},
		structured: func(_ context.Context, _, _, _, _ string, _ *ai.StructuredSpec) (string, error) {
			t.Fatal("no findings call without context")
			return "", nil
		},
	}
	f, err := runTeamSpecialist(context.Background(), deps, TeamParams{KbID: "kb1", Language: "de"}, specialistAgent(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if f.Analysis == "" || len(f.Chunks) != 0 {
		t.Fatal("empty retrieval must yield a no-evidence note, not an LLM call")
	}
}

func TestRunTeamSpecialistRetrieveErrorPropagates(t *testing.T) {
	deps := teamSpecialistDeps{
		retrieve: func(_ context.Context, _ agentteams.AgentRecord, _ agents.Input) (agents.Output, error) {
			return agents.Output{}, errors.New("search down")
		},
	}
	_, err := runTeamSpecialist(context.Background(), deps, TeamParams{KbID: "kb1"}, specialistAgent(), "q")
	if err == nil {
		t.Fatal("retrieval error must propagate (team layer counts failures)")
	}
}
