package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/justrag/go-backend/internal/agents"
	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// TeamSpecialistTimeout bounds one specialist's retrieval + findings call.
// Wider than agents.SupervisorTimeout (12s, search-only) because a
// specialist also runs an LLM findings pass (and optionally tool rounds).
const TeamSpecialistTimeout = 90 * time.Second

// teamSpecialistMaxTokens caps the per-specialist context (well under the
// synthesis cap so N specialists' merged output still fits).
const teamSpecialistMaxTokens = 24_000

// TeamParams configures one RunTeamChat run. Mirrors the other orchestrators'
// params surface so tryDeepChat can build it the same way. Defined here (the
// specialist file) because runTeamSpecialist reads a subset; RunTeamChat
// (team_chat.go) consumes the rest.
type TeamParams struct {
	KbID            string
	ChatID          string
	Query           string
	Language        string
	CurrentDateLine string
	KbSystemPrompt  string
	FileIDs         []string
	GraphChunkIDs   []string
	BridgeChunks    map[string]int
	HyPESearch      bool

	// Team is the selected team (zero-value when a single agent was picked
	// directly). Members carries the enabled member agents — or exactly one
	// agent for the direct pick (router is skipped for len==1).
	Team    agentteams.TeamRecord
	Members []agentteams.AgentRecord

	// RouterModel is the resolved fast-tier model for the routing call.
	// PlanningModel is the enrichment-tier model handed to the retrieval
	// agent (same role as SupervisorChatParams.PlanningModel).
	RouterModel   string
	PlanningModel string

	// Tool wiring (nil ToolDispatcher = specialists run without tools).
	ToolDispatcher       *MCPDispatcher
	AllowPrivilegedTools bool
	ToolMaxRounds        int

	// SearcherForAgent returns the (possibly config-overlaid) searcher for
	// one agent. nil = use the orchestrator's base searcher for everyone.
	SearcherForAgent func(a agentteams.AgentRecord) vector.Searcher
}

// TeamFinding is one specialist's attributed output.
type TeamFinding struct {
	AgentID   string
	AgentName string
	Icon      string
	Analysis  string
	Chunks    []vector.SearchChunk
}

// teamSpecialistDeps carries the injectable surfaces so unit tests drive the
// runner deterministically (mirrors runSupervisorChatTestable's pattern).
type teamSpecialistDeps struct {
	retrieve   func(ctx context.Context, a agentteams.AgentRecord, in agents.Input) (agents.Output, error)
	structured teamStructuredFn
	// toolLoop runs the capped answer-tools loop and returns the accumulated
	// assistant text. nil = the no-tools findings path is always used.
	toolLoop func(ctx context.Context, p AnswerToolsParams) (string, error)
}

// specialistFindingSpec is the strict schema for the findings call.
func specialistFindingSpec() *ai.StructuredSpec {
	raw, err := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"analysis": map[string]any{"type": "string"}},
		"required":             []string{"analysis"},
	})
	if err != nil {
		return nil
	}
	return &ai.StructuredSpec{Name: "specialist_finding", Schema: raw}
}

// runTeamSpecialist executes one specialist: retrieval with the agent's
// (overlaid) knobs, then a findings LLM call under the agent's persona —
// via the tool loop when the agent has tools and a dispatcher is wired.
func runTeamSpecialist(
	ctx context.Context,
	deps teamSpecialistDeps,
	p TeamParams,
	a agentteams.AgentRecord,
	query string,
) (TeamFinding, error) {
	finding := TeamFinding{AgentID: a.ID, AgentName: a.Name, Icon: a.Icon}

	out, err := deps.retrieve(ctx, a, agents.Input{
		KbID:          p.KbID,
		Query:         query,
		Language:      p.Language,
		FileIDs:       p.FileIDs,
		GraphChunkIDs: p.GraphChunkIDs,
		BridgeChunks:  p.BridgeChunks,
		HyPESearch:    p.HyPESearch,
	})
	if err != nil {
		return finding, fmt.Errorf("specialist %s: retrieve: %w", a.Name, err)
	}

	if len(out.Chunks) == 0 {
		if p.Language == "de" {
			finding.Analysis = "Keine relevanten Belege im Wissensbestand gefunden."
		} else {
			finding.Analysis = "No relevant evidence found in the knowledge base."
		}
		return finding, nil
	}

	chunks := TruncateChunksToFit(out.Chunks, teamSpecialistMaxTokens)
	_, contextText := buildChatSourcesAndContext(chunks)

	persona := ""
	if a.SystemPrompt != "" {
		persona = prompts.TeamAgentPersonaBlock(a.Name, a.SystemPrompt)
	}
	system := prompts.TeamSpecialistSystem(p.Language, persona, p.CurrentDateLine)
	user := prompts.TeamSpecialistUser(p.Language, query, contextText)

	// Tool path: the agent has an allowlist and a dispatcher is wired.
	if len(a.ToolNames) > 0 && deps.toolLoop != nil && p.ToolDispatcher != nil {
		restricted := NewRestrictedDispatcher(p.ToolDispatcher, a.ToolNames, p.AllowPrivilegedTools)
		tools := restricted.AnswerToolCatalog(p.KbID)
		if len(tools) > 0 {
			text, terr := deps.toolLoop(ctx, AnswerToolsParams{
				KbID:         p.KbID,
				ChatID:       p.ChatID,
				SystemPrompt: system,
				UserPrompt:   user,
				Tools:        tools,
				Dispatcher:   restricted,
				MaxRounds:    p.ToolMaxRounds,
			})
			if terr == nil && text != "" {
				finding.Analysis = text
				finding.Chunks = chunks
				return finding, nil
			}
			// Tool loop failed — fall through to the plain findings call
			// (specialists are fail-soft; the team layer logs).
		}
	}

	raw, err := deps.structured(ctx, user, system, p.KbID, a.ChatModel, specialistFindingSpec())
	if err != nil {
		return finding, fmt.Errorf("specialist %s: findings: %w", a.Name, err)
	}
	var parsed struct {
		Analysis string `json:"analysis"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed.Analysis == "" {
		// Tolerant fallback: treat the raw content as the analysis.
		parsed.Analysis = raw
	}
	finding.Analysis = parsed.Analysis
	finding.Chunks = chunks
	return finding, nil
}
