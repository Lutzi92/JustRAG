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

	// DeriveToolPolicy makes BuildTeamParams fill the SiteCfg-derived tool and
	// retrieval-arm knobs: HyPESearch, ToolMaxRounds, AllowPrivilegedTools.
	// The ZERO VALUE deliberately means "leave them off". The eval harness
	// relies on that: it has never run the HyPE arm or a tool budget, and a
	// silent change there would alter what an eval measures rather than what
	// production does.
	//
	// This is an opt-IN rather than a deny-list on purpose. A deny-list (the
	// caller resetting fields afterwards) goes stale the moment a further
	// SiteCfg-derived knob is added here — the new one would reach eval
	// unnoticed. With the derivation gated as a block, anything added inside
	// stays off for callers that never opted in.
	DeriveToolPolicy bool

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
		KbID:            in.KbID,
		ChatID:          in.ChatID,
		Query:           in.Query,
		Language:        in.Language,
		CurrentDateLine: in.CurrentDateLine,
		KbSystemPrompt:  in.KbSystemPrompt,
		FileIDs:         in.FileIDs,
		GraphChunkIDs:   in.GraphChunkIDs,
		BridgeChunks:    in.BridgeChunks,
		RouterModel:     AgentTeamRouterModel(ctx, in.SiteCfg),
		PlanningModel:   EnrichmentModel(ctx, in.SiteCfg),
	}
	if in.DeriveToolPolicy {
		p.HyPESearch = HyPESearchEnabled(ctx, in.SiteCfg)
		p.ToolMaxRounds = 2
		p.AllowPrivilegedTools = AgentsAllowPrivilegedTools(ctx, in.SiteCfg)
	}
	if in.Team != nil {
		p.Team = in.Team.Team
		p.Members = in.Team.Members
	} else if in.Agent != nil {
		p.Members = []agentteams.AgentRecord{*in.Agent}
	}
	// Tools only when the deployment's MCP layer is wired + gated on.
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
