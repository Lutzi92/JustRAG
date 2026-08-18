package chat

import (
	"context"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/siteconfig"
	"github.com/justrag/go-backend/internal/vector"
)

// TeamParamsInput collects everything a caller needs to supply to configure
// one team or single-agent run.
type TeamParamsInput struct {
	KbID, ChatID, Query, Language   string
	CurrentDateLine, KbSystemPrompt string
	FileIDs, GraphChunkIDs          []string
	BridgeChunks                    map[string]int

	// Team takes precedence over Agent; when only Agent is set, it runs as
	// a single-member team (the chat send path has always done it this
	// way).
	Team  *agentteams.TeamForChat
	Agent *agentteams.AgentRecord

	SiteCfg        SiteConfigReader
	SearchService  vector.Searcher
	ToolDispatcher *MCPDispatcher
}

// BuildTeamParams builds the TeamParams for RunTeamChat.
//
// Exists because three callers need the same ~50-line construction: the chat
// send path (http_send.go), team eval (eval/team_adapter.go), and workspace
// analysis (contentgen). Before this extraction there were two slightly
// diverging copies (chat and eval) — see the callers for how each preserves
// its own prior behaviour for the fields where they disagreed.
func BuildTeamParams(ctx context.Context, in TeamParamsInput) TeamParams {
	p := TeamParams{
		KbID:                 in.KbID,
		ChatID:               in.ChatID,
		Query:                in.Query,
		Language:             in.Language,
		CurrentDateLine:      in.CurrentDateLine,
		KbSystemPrompt:       in.KbSystemPrompt,
		FileIDs:              in.FileIDs,
		GraphChunkIDs:        in.GraphChunkIDs,
		BridgeChunks:         in.BridgeChunks,
		HyPESearch:           HyPESearchEnabled(ctx, in.SiteCfg),
		RouterModel:          AgentTeamRouterModel(ctx, in.SiteCfg),
		PlanningModel:        EnrichmentModel(ctx, in.SiteCfg),
		ToolMaxRounds:        2,
		AllowPrivilegedTools: AgentsAllowPrivilegedTools(ctx, in.SiteCfg),
	}
	if in.Team != nil {
		p.Team = in.Team.Team
		p.Members = in.Team.Members
	} else if in.Agent != nil {
		p.Members = []agentteams.AgentRecord{*in.Agent}
	}
	if in.ToolDispatcher != nil && ChatUseMCPTools(ctx, in.SiteCfg) {
		p.ToolDispatcher = in.ToolDispatcher
	}
	// Per-agent retrieval overlay: agent config -> (KB-overlaid) reader ->
	// global. Cloning the search service mirrors forKB.
	p.SearcherForAgent = func(a agentteams.AgentRecord) vector.Searcher {
		if len(a.Config) == 0 {
			return in.SearchService
		}
		overrides := make(map[string]*string, len(a.Config))
		for k, v := range a.Config {
			overrides[k] = &v
		}
		overlay := siteconfig.NewAgentOverlay(in.SiteCfg, overrides)
		if ss, ok := in.SearchService.(*vector.SearchService); ok {
			return ss.CloneWithSiteConfigReader(overlay)
		}
		return in.SearchService
	}
	return p
}
