package contentgen

import (
	"context"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/logctx"
)

// TeamLoader lädt und autorisiert eine Agent-/Team-Auswahl für eine KB.
// Erfüllt von *agentteams.Store.
type TeamLoader interface {
	LoadTeamForChat(ctx context.Context, teamID, kbID string) (*agentteams.TeamForChat, error)
	LoadAgentForChat(ctx context.Context, agentID, kbID string) (*agentteams.AgentRecord, error)
}

// agentSelection ist die aufgelöste Auswahl. Genau eines der Felder ist gesetzt.
type agentSelection struct {
	Team  *agentteams.TeamForChat
	Agent *agentteams.AgentRecord
}

// resolveAnalysisAgent löst die im Dialog gewählte Agent-/Team-Auswahl auf.
//
// Fail-soft wie chat.resolveTeamSelection: ein gelöschter, deaktivierter oder
// von der KB gelöster Agent kostet den Lauf nicht. Anders als dort wird der
// Grund aber zurückgegeben und bis in die Antwort durchgereicht — der Nutzer
// hat im Dialog bewusst gewählt, ein stumm ohne Agent erzeugtes Artefakt sähe
// agentengeneriert aus, ohne es zu sein.
func resolveAnalysisAgent(ctx context.Context, loader TeamLoader, kbID, agentID, teamID string) (*agentSelection, string) {
	if loader == nil {
		if agentID != "" || teamID != "" {
			return nil, "unavailable"
		}
		return nil, ""
	}
	switch {
	case teamID != "":
		tfc, err := loader.LoadTeamForChat(ctx, teamID, kbID)
		if err != nil {
			logctx.From(ctx).Warn("workspace.analysis.team_load_failed", "team_id", teamID, "kb_id", kbID, "error", err)
			return nil, "load_failed"
		}
		if len(tfc.Members) == 0 {
			logctx.From(ctx).Warn("workspace.analysis.empty_team", "team_id", teamID)
			return nil, "empty_team"
		}
		return &agentSelection{Team: tfc}, ""
	case agentID != "":
		a, err := loader.LoadAgentForChat(ctx, agentID, kbID)
		if err != nil {
			logctx.From(ctx).Warn("workspace.analysis.agent_load_failed", "agent_id", agentID, "kb_id", kbID, "error", err)
			return nil, "load_failed"
		}
		return &agentSelection{Agent: a}, ""
	}
	return nil, ""
}
