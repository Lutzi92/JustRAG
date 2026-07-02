package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/justrag/go-backend/internal/agents"
	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// ErrTeamNoRoute signals the router selected no specialist — a valid outcome;
// tryDeepChat treats it (like any orchestrator error) as "fall through to the
// standard PrepareChatContext path".
var ErrTeamNoRoute = errors.New("team: router selected no specialist")

// teamSynthesisMaxTokens caps the merged synthesis context (matches the
// supervisor's cap).
const teamSynthesisMaxTokens = 120_000

// teamRouteFn / teamSpecialistFn are the injectable stage surfaces for tests.
type teamRouteFn func(ctx context.Context, candidates []agentteams.AgentRecord, maxSelect int) ([]agentteams.AgentRecord, string, error)
type teamSpecialistFn func(ctx context.Context, a agentteams.AgentRecord, query string) (TeamFinding, error)

// RunTeamChat is the production entry point for the user-created agent-team
// orchestrator: router → parallel specialists → synthesis-prompt assembly.
// The final streamed answer is produced by tryDeepChat's post-dispatch
// streamer from the returned ChatContext (same contract as the other
// orchestrators).
func RunTeamChat(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	params TeamParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	structured := func(c context.Context, prompt, system, kb, model string, spec *ai.StructuredSpec) (string, error) {
		res, err := ai.GenerateCompletionStructured(c, aiResolver, prompt, system, kb, model, spec)
		if err != nil {
			return "", err
		}
		return res.Content, nil
	}
	toolLoop := func(c context.Context, p AnswerToolsParams) (string, error) {
		p.AIResolver = aiResolver
		var sb strings.Builder
		err := RunAnswerWithTools(c, p, func(ev ai.StreamEvent) {
			if ev.Content != "" {
				sb.WriteString(ev.Content)
			}
		}, nil)
		return sb.String(), err
	}
	route := func(c context.Context, candidates []agentteams.AgentRecord, maxSelect int) ([]agentteams.AgentRecord, string, error) {
		return routeTeam(c, structured, params.KbID, params.RouterModel, params.Language, params.Query, candidates, maxSelect)
	}
	specialist := func(c context.Context, a agentteams.AgentRecord, query string) (TeamFinding, error) {
		deps := teamSpecialistDeps{
			retrieve: func(rc context.Context, ag agentteams.AgentRecord, in agents.Input) (agents.Output, error) {
				searcher := searchSvc
				if params.SearcherForAgent != nil {
					searcher = params.SearcherForAgent(ag)
				}
				return agents.NewRetrieverAgent(searcher, params.PlanningModel).Execute(rc, in)
			},
			structured: structured,
			toolLoop:   toolLoop,
		}
		return runTeamSpecialist(c, deps, params, a, query)
	}
	return runTeamChatTestable(ctx, route, specialist, params, emit)
}

// runTeamChatTestable accepts the route + specialist stages as injectable
// arguments so unit tests drive the loop deterministically.
func runTeamChatTestable(
	ctx context.Context,
	route teamRouteFn,
	specialist teamSpecialistFn,
	params TeamParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	if len(params.Members) == 0 {
		return nil, fmt.Errorf("team %s: %w", params.Team.ID, ErrTeamNoRoute)
	}

	// ---------- ROUTE ----------
	selected := params.Members
	reasoning := "single agent"
	if len(params.Members) > 1 {
		maxSelect := params.Team.MaxAgentsPerTurn
		var err error
		selected, reasoning, err = route(ctx, params.Members, maxSelect)
		if err != nil {
			return nil, fmt.Errorf("team %s: %w", params.Team.ID, err)
		}
		if len(selected) == 0 {
			emitTrajectory(emit, TrajectoryEvent{
				Stage: "decision", Decision: "team_route",
				Reason: "no specialist selected: " + reasoning,
			}, nil)
			return nil, ErrTeamNoRoute
		}
	}
	names := make([]string, len(selected))
	for i, a := range selected {
		names[i] = a.Name
	}
	emitTrajectory(emit, TrajectoryEvent{
		Stage: "plan", Decision: "team_route",
		Reason:   reasoning,
		Queries:  names,
		Findings: len(selected),
	}, map[string]any{"teamRoute": map[string]any{"agents": names, "reasoning": reasoning}})

	// ---------- SPECIALISTS (parallel, fail-soft) ----------
	type result struct {
		finding TeamFinding
		err     error
	}
	results := make([]result, len(selected))
	var wg sync.WaitGroup
	for i, a := range selected {
		wg.Go(func() {
			defer safego.RecoverError(&results[i].err)
			sctx, cancel := context.WithTimeout(ctx, TeamSpecialistTimeout)
			defer cancel()
			results[i].finding, results[i].err = specialist(sctx, a, params.Query)
		})
	}
	wg.Wait()

	var findings []TeamFinding
	var chunkLists [][]vector.SearchChunk
	var firstErr error
	for i, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			logctx.From(ctx).Warn("team: specialist failed",
				"agent", selected[i].Name, "error", r.err)
			continue
		}
		findings = append(findings, r.finding)
		if len(r.finding.Chunks) > 0 {
			chunkLists = append(chunkLists, r.finding.Chunks)
		}
		emitTrajectory(emit, TrajectoryEvent{
			Stage: "hop", Step: i + 1,
			Query:    r.finding.AgentName,
			Findings: len(r.finding.Chunks),
			Reason:   "specialist_complete",
		}, nil)
	}
	if len(findings) == 0 {
		return nil, fmt.Errorf("team %s: all %d specialist(s) failed: %w",
			params.Team.ID, len(selected), firstErr)
	}

	// ---------- SYNTHESIS PROMPT ----------
	merged := agents.MergeChunksRRF(0, chunkLists...)
	merged = TruncateChunksToFit(merged, teamSynthesisMaxTokens)
	merged = SandwichOrder(merged)
	sources, contextText := buildChatSourcesAndContext(merged)

	var sb strings.Builder
	if params.KbSystemPrompt != "" {
		sb.WriteString(params.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPromptWithDate(params.Language, params.CurrentDateLine))
	if IsLowConfidence(merged) {
		sb.WriteString(prompts.ChatLowConfidenceNotice(params.Language))
	}
	sb.WriteString("\n\nTEAM FINDINGS (attributed specialist analyses — synthesize them into one coherent, cited answer; on conflicts, weigh the evidence):\n")
	for _, f := range findings {
		fmt.Fprintf(&sb, "\n--- Finding from %s ---\n%s\n", f.AgentName, f.Analysis)
	}
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(contextText)

	emitTrajectory(emit, TrajectoryEvent{
		Stage: "answer", Decision: "team_synthesis",
		Step: len(findings), Findings: len(merged),
	}, nil)
	logctx.From(ctx).Info("rag.team_chat.complete",
		"team_id", params.Team.ID,
		"specialists", len(findings),
		"chunks", len(merged),
	)

	return &ChatContext{
		SystemPrompt: sb.String(),
		Sources:      sources,
		Context:      contextText,
		FinalChunks:  merged,
	}, nil
}
