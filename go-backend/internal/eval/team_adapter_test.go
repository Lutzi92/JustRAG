package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

type fakeTeamLoader struct {
	tfc *agentteams.TeamForChat
	err error
}

func (f *fakeTeamLoader) LoadTeamForChat(_ context.Context, teamID, kbID string) (*agentteams.TeamForChat, error) {
	return f.tfc, f.err
}

func teamAdapterFixture(runTeam teamRunFn) *TeamDispatchAdapter {
	a := NewTeamDispatchAdapter(nil, nil, nil, &fakeTeamLoader{
		tfc: &agentteams.TeamForChat{
			Team:    agentteams.TeamRecord{ID: "t1", Name: "Sec-Team", MaxAgentsPerTurn: 2},
			Members: []agentteams.AgentRecord{{ID: "a1", Name: "Netz"}},
		},
	}, "t1", func(context.Context, string) string { return "kb prompt" })
	a.runTeam = runTeam
	return a
}

func TestTeamAdapterDispatchesEveryQuestionThroughTeam(t *testing.T) {
	calls := 0
	a := teamAdapterFixture(func(_ context.Context, params chat.TeamParams) (*chat.ChatContext, error) {
		calls++
		if params.Team.ID != "t1" || len(params.Members) != 1 {
			t.Fatalf("team not threaded: %+v", params.Team)
		}
		if params.KbSystemPrompt != "kb prompt" {
			t.Fatal("kb system prompt not threaded")
		}
		return &chat.ChatContext{FinalChunks: []vector.SearchChunk{
			{ID: "c1", FileID: "f1", Content: "x", Score: 0.9},
			{ID: "c2", FileID: "f2", Content: "y", Score: 0.4},
		}}, nil
	})
	q := Question{ID: "q1", Question: "test?", KbID: "kb1"}
	got, err := a.Search(context.Background(), q, 2)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(got) != 2 {
		t.Fatalf("calls=%d chunks=%d", calls, len(got))
	}
	tr := a.AgentTraceForQuestion("q1")
	if tr == nil || tr.Orchestrator != "team" || tr.Specialist != "Sec-Team" {
		t.Fatalf("trace wrong: %+v", tr)
	}
}

// TestTeamAdapterPreservesToolAndHyPEDefaults is the FIX round-1 #2
// regression test: chat.BuildTeamParams derives HyPESearch/ToolMaxRounds/
// AllowPrivilegedTools from SiteCfg (matching the chat send path), but the
// eval path never wired any of the three pre-extraction — Search resets all
// three back to their old zero values right after the BuildTeamParams call
// (see the comment in team_adapter.go). This test pins that reset. SiteCfg
// is deliberately configured to flip both readable flags to true, so a
// deleted reset line is visible here — with an empty/nil SiteCfg both
// readers already default to false and the assertion would pass either way,
// hiding a dropped reset.
func TestTeamAdapterPreservesToolAndHyPEDefaults(t *testing.T) {
	teamLoader := &fakeTeamLoader{
		tfc: &agentteams.TeamForChat{
			Team:    agentteams.TeamRecord{ID: "t1"},
			Members: []agentteams.AgentRecord{{ID: "a1"}},
		},
	}
	siteCfg := &stubSiteCfg{values: map[string]string{
		"hype_search_enabled":           "true",
		"agents_allow_privileged_tools": "true",
	}}
	a := NewTeamDispatchAdapter(nil, nil, siteCfg, teamLoader, "t1", func(context.Context, string) string { return "" })
	var captured chat.TeamParams
	a.runTeam = func(_ context.Context, params chat.TeamParams) (*chat.ChatContext, error) {
		captured = params
		return &chat.ChatContext{}, nil
	}
	if _, err := a.Search(context.Background(), Question{ID: "q1", KbID: "kb1"}, 4); err != nil {
		t.Fatal(err)
	}
	if captured.HyPESearch {
		t.Error("HyPESearch must stay false on the eval path even when SiteCfg enables it — eval never wired HyPE search pre-extraction")
	}
	if captured.ToolMaxRounds != 0 {
		t.Errorf("ToolMaxRounds = %d, want 0 — eval has no ToolDispatcher and never budgeted tool rounds pre-extraction", captured.ToolMaxRounds)
	}
	if captured.AllowPrivilegedTools {
		t.Error("AllowPrivilegedTools must stay false on the eval path even when SiteCfg enables it")
	}
}

// TestTeamAdapterCachesChatContextForJudge verifies the judge-mode surface:
// after a successful Search, ChatContextForQuestion serves the exact
// ChatContext RunTeamChat produced (SystemPrompt + Context + sandwich-order
// FinalChunks), and ContentsForQuestion projects from the same cache —
// mirroring ProductionContextAdapter's contract so --judge composes
// unchanged for team runs.
func TestTeamAdapterCachesChatContextForJudge(t *testing.T) {
	produced := &chat.ChatContext{
		SystemPrompt: "team system prompt",
		Context:      "assembled context text",
		FinalChunks: []vector.SearchChunk{
			{ID: "c1", FileID: "f1", FileName: "a.md", Content: "alpha", Score: 0.9},
			{ID: "c2", FileID: "f2", FileName: "b.md", Content: "beta", Score: 0.4},
		},
	}
	a := teamAdapterFixture(func(context.Context, chat.TeamParams) (*chat.ChatContext, error) {
		return produced, nil
	})
	if _, err := a.Search(context.Background(), Question{ID: "q1", Question: "test?", KbID: "kb1"}, 2); err != nil {
		t.Fatal(err)
	}

	got, ok := a.ChatContextForQuestion("q1")
	if !ok || got == nil {
		t.Fatal("expected cached ChatContext after successful Search")
	}
	if got != produced {
		t.Fatalf("expected the exact ChatContext RunTeamChat produced, got %+v", got)
	}
	if got.SystemPrompt != "team system prompt" || got.Context != "assembled context text" {
		t.Fatalf("SystemPrompt/Context not preserved: %+v", got)
	}
	if len(got.FinalChunks) != 2 || got.FinalChunks[0].ID != "c1" {
		t.Fatalf("FinalChunks not preserved in sandwich order: %+v", got.FinalChunks)
	}

	// ContentsForQuestion projects from the same cache (sandwich order, trimmed to k).
	contents, fileNames, ok := a.ContentsForQuestion("q1", 1)
	if !ok || len(contents) != 1 || contents[0] != "alpha" || fileNames[0] != "a.md" {
		t.Fatalf("ContentsForQuestion projection wrong: ok=%v contents=%v files=%v", ok, contents, fileNames)
	}

	// Unknown question id → miss (judge loop records a JudgeError, no panic).
	if _, ok := a.ChatContextForQuestion("nope"); ok {
		t.Fatal("expected miss for unknown question id")
	}
}

func TestTeamAdapterHardFailsOnLoadError(t *testing.T) {
	a := NewTeamDispatchAdapter(nil, nil, nil, &fakeTeamLoader{err: errors.New("not attached")}, "t1",
		func(context.Context, string) string { return "" })
	a.runTeam = func(context.Context, chat.TeamParams) (*chat.ChatContext, error) {
		t.Fatal("must not dispatch when the team fails to load")
		return nil, nil
	}
	if _, err := a.Search(context.Background(), Question{ID: "q1", KbID: "kb1"}, 4); err == nil {
		t.Fatal("load failure must be a hard error — evaluating a nonexistent configuration is meaningless")
	}
}

func TestTeamAdapterOrchestratorErrorIsHard(t *testing.T) {
	a := teamAdapterFixture(func(context.Context, chat.TeamParams) (*chat.ChatContext, error) {
		return nil, errors.New("router down")
	})
	if _, err := a.Search(context.Background(), Question{ID: "q1", KbID: "kb1"}, 4); err == nil {
		t.Fatal("team dispatch errors must surface per-question (NO silent fallback to standard — that would silently measure the wrong pipeline)")
	}
}

// fakeBaseSearcher is a minimal vector.Searcher stand-in (not a
// *vector.SearchService), used to verify the SearcherForAgent overlay's
// fallback branch: when the underlying searcher isn't a concrete
// *vector.SearchService, the clone-with-overlay branch can't apply and the
// closure returns the base searcher unchanged (documented behaviour — the
// clone branch itself mirrors reviewed production code in http_send.go and
// is only exercised end-to-end with a real *vector.SearchService).
type fakeBaseSearcher struct{}

func (fakeBaseSearcher) Search(context.Context, string, string, int, vector.SearchOptions) (*vector.SearchResult, error) {
	return &vector.SearchResult{}, nil
}

func (fakeBaseSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

// TestTeamAdapterWiresSearcherForAgentOverlay is the FIX-2 regression test:
// the eval team adapter must build a per-agent SearcherForAgent overlay the
// same way tryDeepChat does (http_send.go ~656-670), not leave it nil as
// the pre-fix "v1" comment claimed. It asserts the closure is wired and
// exercises both of its branches: empty agent Config returns the base
// searcher untouched; a non-empty Config still returns the base searcher
// here because the fake isn't a *vector.SearchService (the clone-into-
// overlay branch only fires for the real production type).
func TestTeamAdapterWiresSearcherForAgentOverlay(t *testing.T) {
	base := fakeBaseSearcher{}
	siteCfg := &stubSiteCfg{values: map[string]string{}}
	teamLoader := &fakeTeamLoader{
		tfc: &agentteams.TeamForChat{
			Team:    agentteams.TeamRecord{ID: "t1", Name: "Sec-Team", MaxAgentsPerTurn: 2},
			Members: []agentteams.AgentRecord{{ID: "a1", Name: "Netz"}},
		},
	}
	a := NewTeamDispatchAdapter(nil, base, siteCfg, teamLoader, "t1", func(context.Context, string) string { return "" })

	var captured func(agentteams.AgentRecord) vector.Searcher
	a.runTeam = func(_ context.Context, params chat.TeamParams) (*chat.ChatContext, error) {
		captured = params.SearcherForAgent
		return &chat.ChatContext{}, nil
	}

	if _, err := a.Search(context.Background(), Question{ID: "q1", KbID: "kb1"}, 4); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("SearcherForAgent must be set (nil disables per-agent config overlay entirely)")
	}

	if got := captured(agentteams.AgentRecord{ID: "a1"}); got != base {
		t.Fatalf("empty Config: expected base searcher passthrough, got %v", got)
	}
	if got := captured(agentteams.AgentRecord{ID: "a1", Config: map[string]string{"rerank_blend_alpha": "0.5"}}); got != base {
		t.Fatalf("non-*vector.SearchService base: expected base searcher fallback, got %v", got)
	}
}
