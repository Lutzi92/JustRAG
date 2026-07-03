package eval

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/siteconfig"
	"github.com/justrag/go-backend/internal/vector"
)

// TeamLoaderForEval loads a team for eval dispatch. Satisfied by
// *agentteams.Store; fakes in tests.
type TeamLoaderForEval interface {
	LoadTeamForChat(ctx context.Context, teamID, kbID string) (*agentteams.TeamForChat, error)
}

// teamRunFn is the injectable orchestrator surface (production: RunTeamChat).
type teamRunFn func(ctx context.Context, params chat.TeamParams) (*chat.ChatContext, error)

// TeamDispatchAdapter evaluates a golden set through the real RunTeamChat
// path: EVERY question dispatches through the fixed team (the team path is
// selection-driven in production, not query-type-driven, so it deliberately
// bypasses ClassifyQueryTypeForEval/SelectOrchestrator). Errors are HARD:
// unlike chat's fail-soft, an eval that silently fell back to the standard
// pipeline would measure the wrong thing.
type TeamDispatchAdapter struct {
	aiResolver     *ai.ConfigResolver
	searchService  vector.Searcher
	siteCfg        chat.SiteConfigReader
	teams          TeamLoaderForEval
	teamID         string
	kbSystemPrompt func(ctx context.Context, kbID string) string

	runTeam teamRunFn // injectable for tests

	mu         sync.Mutex
	loaded     map[string]*agentteams.TeamForChat // kbID -> team (attachment is per-KB)
	traceCache map[string]*AgentTrace
	ctxCache   map[string]*chat.ChatContext // full per-question ChatContext (sandwich-order FinalChunks) for judge mode
}

// NewTeamDispatchAdapter constructs the adapter. teamID is fixed for the run.
func NewTeamDispatchAdapter(
	aiResolver *ai.ConfigResolver,
	searchService vector.Searcher,
	siteCfg chat.SiteConfigReader,
	teams TeamLoaderForEval,
	teamID string,
	kbSystemPrompt func(ctx context.Context, kbID string) string,
) *TeamDispatchAdapter {
	a := &TeamDispatchAdapter{
		aiResolver:     aiResolver,
		searchService:  searchService,
		siteCfg:        siteCfg,
		teams:          teams,
		teamID:         teamID,
		kbSystemPrompt: kbSystemPrompt,
		loaded:         map[string]*agentteams.TeamForChat{},
		traceCache:     map[string]*AgentTrace{},
		ctxCache:       map[string]*chat.ChatContext{},
	}
	a.runTeam = func(ctx context.Context, params chat.TeamParams) (*chat.ChatContext, error) {
		return chat.RunTeamChat(ctx, a.aiResolver, a.searchService, params, func(map[string]any) {})
	}
	return a
}

func (a *TeamDispatchAdapter) teamFor(ctx context.Context, kbID string) (*agentteams.TeamForChat, error) {
	a.mu.Lock()
	tfc, ok := a.loaded[kbID]
	a.mu.Unlock()
	if ok {
		return tfc, nil
	}
	tfc, err := a.teams.LoadTeamForChat(ctx, a.teamID, kbID)
	if err != nil {
		return nil, fmt.Errorf("team eval: load team %s for kb %s (must be attached + enabled): %w", a.teamID, kbID, err)
	}
	a.mu.Lock()
	a.loaded[kbID] = tfc
	a.mu.Unlock()
	return tfc, nil
}

// Search dispatches one question through RunTeamChat and projects
// FinalChunks into retrieval-metric shape (score-desc, trimmed to k) — the
// same projection the orchestrator adapter uses. Errors (team load failure
// or RunTeamChat failure) are hard: no silent fallback to the standard
// pipeline.
func (a *TeamDispatchAdapter) Search(ctx context.Context, q Question, k int) ([]RetrievedChunk, error) {
	tfc, err := a.teamFor(ctx, q.KbID)
	if err != nil {
		return nil, err
	}
	kbSystemPrompt := ""
	if a.kbSystemPrompt != nil {
		kbSystemPrompt = a.kbSystemPrompt(ctx, q.KbID)
	}
	params := chat.TeamParams{
		KbID:           q.KbID,
		Query:          q.Question,
		Language:       q.Language,
		KbSystemPrompt: kbSystemPrompt,
		Team:           tfc.Team,
		Members:        tfc.Members,
		RouterModel:    chat.AgentTeamRouterModel(ctx, a.siteCfg),
		PlanningModel:  chat.EnrichmentModel(ctx, a.siteCfg),
		// Per-agent retrieval-knob overlay, built the same way tryDeepChat
		// builds it (internal/chat/http_send.go ~656-670): agent config →
		// overlay reader → global. Unlike production, eval has no
		// KB-overlaid reader layer underneath — a.siteCfg is the GLOBAL
		// site_config reader (pre-existing eval-wide convention: no
		// kb_site_configs overlay anywhere in the eval path, not a new gap
		// introduced here). v1: still no tools in eval (see the plan) — that
		// omission remains deliberately deferred.
		SearcherForAgent: func(ag agentteams.AgentRecord) vector.Searcher {
			if len(ag.Config) == 0 {
				return a.searchService
			}
			overrides := make(map[string]*string, len(ag.Config))
			for k, v := range ag.Config {
				overrides[k] = &v
			}
			overlay := siteconfig.NewAgentOverlay(a.siteCfg, overrides)
			if ss, ok := a.searchService.(*vector.SearchService); ok {
				return ss.CloneWithSiteConfigReader(overlay)
			}
			return a.searchService
		},
	}
	chatCtx, err := a.runTeam(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("team eval: %s: %w", q.ID, err)
	}

	a.mu.Lock()
	a.ctxCache[q.ID] = chatCtx // full context (sandwich-order FinalChunks), for judge mode
	a.traceCache[q.ID] = &AgentTrace{
		Orchestrator: "team",
		Specialist:   tfc.Team.Name,
	}
	a.mu.Unlock()

	// Score-descending projection, trimmed to k (mirrors orchestrator_adapter).
	chunks := append([]vector.SearchChunk(nil), chatCtx.FinalChunks...)
	slices.SortStableFunc(chunks, func(x, y vector.SearchChunk) int {
		return cmp.Compare(y.Score, x.Score)
	})
	if k > 0 && k < len(chunks) {
		chunks = chunks[:k]
	}
	out := make([]RetrievedChunk, 0, len(chunks))
	for _, c := range chunks {
		idx, total := ChunkPositionFromMetadata(c.Metadata)
		out = append(out, RetrievedChunk{
			FileID:      c.FileID,
			FileName:    c.FileName,
			Score:       c.Score,
			ChunkIndex:  idx,
			TotalChunks: total,
		})
	}
	return out, nil
}

// AgentTraceForQuestion satisfies the runner's agentTracer assertion.
func (a *TeamDispatchAdapter) AgentTraceForQuestion(questionID string) *AgentTrace {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.traceCache[questionID]
}

// ChatContextForQuestion returns the cached full ChatContext for judge-mode
// answer generation (mirrors ProductionContextAdapter's contract: the judge
// reuses the exact SystemPrompt + Context text RunTeamChat assembled).
// Returns (nil, false) if no successful dispatch ran for questionID.
func (a *TeamDispatchAdapter) ChatContextForQuestion(questionID string) (*chat.ChatContext, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.ctxCache[questionID]
	return c, ok
}

// ContentsForQuestion returns chunk content + file names per question, in
// sandwich order (matches OrchestratorDispatchAdapter's contract) — used by
// judge-mode / RAGAS export.
func (a *TeamDispatchAdapter) ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool) {
	c, hit := a.ChatContextForQuestion(questionID)
	if !hit {
		return nil, nil, false
	}
	chunks := c.FinalChunks
	if k > 0 && k < len(chunks) {
		chunks = chunks[:k]
	}
	contents = make([]string, len(chunks))
	fileNames = make([]string, len(chunks))
	for i, ch := range chunks {
		contents[i] = ch.Content
		fileNames[i] = ch.FileName
	}
	return contents, fileNames, true
}
