package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/prompts"
)

// teamStructuredFn is the injectable structured-completion surface for the
// team router + specialists (production: a thin wrapper over
// ai.GenerateCompletionStructured; tests: a canned function).
type teamStructuredFn func(ctx context.Context, prompt, system, kbID, model string, spec *ai.StructuredSpec) (string, error)

// teamRouteDecision is the router's structured output.
type teamRouteDecision struct {
	SelectedAgentIDs []string `json:"selected_agent_ids"`
	Reasoning        string   `json:"reasoning"`
}

// teamRouterSpec builds a strict schema whose selected_agent_ids items are
// enum-constrained to the actual candidate ids — the grammar itself prevents
// hallucinated ids (unknown ids are additionally dropped defensively).
func teamRouterSpec(candidateIDs []string) *ai.StructuredSpec {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"selected_agent_ids": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": candidateIDs},
			},
			"reasoning": map[string]any{"type": "string"},
		},
		"required": []string{"selected_agent_ids", "reasoning"},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		// Structurally impossible (static map); guard anyway.
		return nil
	}
	return &ai.StructuredSpec{Name: "team_route", Schema: raw}
}

// routeTeam runs the one-call routing step: agent cards in, selected subset
// out (order = router preference, capped at maxSelect). An empty selection
// is a valid outcome — the caller falls through to the standard path.
func routeTeam(
	ctx context.Context,
	structured teamStructuredFn,
	kbID, model, lang, query string,
	candidates []agentteams.AgentRecord,
	maxSelect int,
) ([]agentteams.AgentRecord, string, error) {
	if maxSelect <= 0 {
		maxSelect = 3
	}
	byID := make(map[string]agentteams.AgentRecord, len(candidates))
	ids := make([]string, 0, len(candidates))
	var cards strings.Builder
	for _, a := range candidates {
		byID[a.ID] = a
		ids = append(ids, a.ID)
		fmt.Fprintf(&cards, "- id: %s | name: %s | expertise: %s\n", a.ID, a.Name, a.Description)
	}

	raw, err := structured(ctx,
		prompts.TeamRouterUser(query, cards.String()),
		prompts.TeamRouterSystem(lang),
		kbID, model, teamRouterSpec(ids))
	if err != nil {
		return nil, "", fmt.Errorf("team router: %w", err)
	}

	var dec teamRouteDecision
	if err := json.Unmarshal([]byte(raw), &dec); err != nil {
		return nil, "", fmt.Errorf("team router: parse: %w", err)
	}

	seen := make(map[string]bool, len(dec.SelectedAgentIDs))
	selected := make([]agentteams.AgentRecord, 0, maxSelect)
	for _, id := range dec.SelectedAgentIDs {
		if len(selected) >= maxSelect {
			break
		}
		a, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		selected = append(selected, a)
	}
	return selected, dec.Reasoning, nil
}
