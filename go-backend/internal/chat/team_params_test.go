package chat

import (
	"context"
	"reflect"
	"testing"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/vector"
)

// This test pins the field values http_send.go set inline before the
// extraction. It is a characterization test: if it goes red, the extraction
// changed behaviour — the test's expectation is not what's wrong.
func TestBuildTeamParamsMirrorsInlineConstruction(t *testing.T) {
	agent := agentteams.AgentRecord{ID: "a1", Name: "Prüfer"}
	in := TeamParamsInput{
		KbID: "kb1", ChatID: "c1", Query: "Frage", Language: "de",
		CurrentDateLine: "Heute ist der 18.08.2026.", KbSystemPrompt: "KB-Prompt",
		FileIDs: []string{"f1"}, Agent: &agent, SiteCfg: &fakeSiteConfigReader{},
	}
	p := BuildTeamParams(context.Background(), in)

	if p.KbID != "kb1" || p.ChatID != "c1" || p.Query != "Frage" || p.Language != "de" {
		t.Errorf("head fields wrong: %+v", p)
	}
	if p.ToolMaxRounds != 2 {
		t.Errorf("ToolMaxRounds = %d, want 2 (value from http_send.go)", p.ToolMaxRounds)
	}
	if len(p.Members) != 1 || p.Members[0].ID != "a1" {
		t.Errorf("a single agent must run as a one-member team, got %+v", p.Members)
	}
	if p.SearcherForAgent == nil {
		t.Error("SearcherForAgent must be set, otherwise the per-agent retrieval overlay does not apply")
	}
}

func TestBuildTeamParamsPrefersTeamOverAgent(t *testing.T) {
	team := &agentteams.TeamForChat{
		Team:    agentteams.TeamRecord{ID: "t1"},
		Members: []agentteams.AgentRecord{{ID: "m1"}, {ID: "m2"}},
	}
	p := BuildTeamParams(context.Background(), TeamParamsInput{
		KbID: "kb1", Team: team, Agent: &agentteams.AgentRecord{ID: "a1"}, SiteCfg: &fakeSiteConfigReader{},
	})
	if p.Team.ID != "t1" || len(p.Members) != 2 {
		t.Errorf("team must beat the single agent, got Team=%q Members=%d", p.Team.ID, len(p.Members))
	}
}

// Fix round 1 (#1a): TestBuildTeamParamsMirrorsInlineConstruction set
// CurrentDateLine/KbSystemPrompt/FileIDs but never asserted them in the
// output, and never exercised GraphChunkIDs/BridgeChunks at all — a dropped
// assignment for any of those went unnoticed. This test gives every
// pass-through input field a distinct, recognisable value and asserts each
// one round-trips into the resulting TeamParams unchanged.
func TestBuildTeamParamsRoundTripsAllInputFields(t *testing.T) {
	in := TeamParamsInput{
		KbID: "kb1", ChatID: "c1", Query: "Frage", Language: "de",
		CurrentDateLine: "Heute ist der 18.08.2026.", KbSystemPrompt: "KB-Prompt",
		FileIDs:       []string{"f1", "f2"},
		GraphChunkIDs: []string{"g1"},
		BridgeChunks:  map[string]int{"b1": 3},
		Agent:         &agentteams.AgentRecord{ID: "a1"},
		SiteCfg:       &fakeSiteConfigReader{},
	}
	p := BuildTeamParams(context.Background(), in)

	if p.KbID != "kb1" {
		t.Errorf("KbID = %q, want kb1", p.KbID)
	}
	if p.ChatID != "c1" {
		t.Errorf("ChatID = %q, want c1", p.ChatID)
	}
	if p.Query != "Frage" {
		t.Errorf("Query = %q, want Frage", p.Query)
	}
	if p.Language != "de" {
		t.Errorf("Language = %q, want de", p.Language)
	}
	if p.CurrentDateLine != "Heute ist der 18.08.2026." {
		t.Errorf("CurrentDateLine = %q, want the input date line", p.CurrentDateLine)
	}
	if p.KbSystemPrompt != "KB-Prompt" {
		t.Errorf("KbSystemPrompt = %q, want KB-Prompt", p.KbSystemPrompt)
	}
	if !reflect.DeepEqual(p.FileIDs, []string{"f1", "f2"}) {
		t.Errorf("FileIDs = %+v, want [f1 f2]", p.FileIDs)
	}
	if !reflect.DeepEqual(p.GraphChunkIDs, []string{"g1"}) {
		t.Errorf("GraphChunkIDs = %+v, want [g1]", p.GraphChunkIDs)
	}
	if !reflect.DeepEqual(p.BridgeChunks, map[string]int{"b1": 3}) {
		t.Errorf("BridgeChunks = %+v, want map[b1:3]", p.BridgeChunks)
	}
}

// Fix round 1 (#1b): the all-defaults fakeSiteConfigReader in the tests
// above returns nil for every key, so HyPESearchEnabled/
// AgentsAllowPrivilegedTools/AgentTeamRouterModel/EnrichmentModel all fall
// through to their zero-ish defaults regardless of whether BuildTeamParams
// actually calls the reader — a dropped call is invisible. This test
// configures a reader that answers "true"/distinct model names for those
// four gate keys and asserts the resolved TeamParams reflect them, proving
// BuildTeamParams genuinely reads SiteCfg rather than hardcoding results.
func TestBuildTeamParamsReadsSiteConfigDerivedFlags(t *testing.T) {
	cfg := &fakeSiteConfigReader{values: map[string]*string{
		"hype_search_enabled":           strPtr("true"),
		"agents_allow_privileged_tools": strPtr("true"),
		"agent_team_router_model":       strPtr("router-model-x"),
		"contextual_enrichment_model":   strPtr("planning-model-y"),
	}}
	p := BuildTeamParams(context.Background(), TeamParamsInput{
		KbID: "kb1", Agent: &agentteams.AgentRecord{ID: "a1"}, SiteCfg: cfg,
	})
	if !p.HyPESearch {
		t.Error("HyPESearch = false, want true — hype_search_enabled=true in SiteCfg was not read")
	}
	if !p.AllowPrivilegedTools {
		t.Error("AllowPrivilegedTools = false, want true — agents_allow_privileged_tools=true in SiteCfg was not read")
	}
	if p.RouterModel != "router-model-x" {
		t.Errorf("RouterModel = %q, want router-model-x", p.RouterModel)
	}
	if p.PlanningModel != "planning-model-y" {
		t.Errorf("PlanningModel = %q, want planning-model-y", p.PlanningModel)
	}
}

// Fix round 1 (#1c): the prior test only asserted SearcherForAgent != nil,
// so replacing its whole body with "return in.SearchService" (dropping the
// per-agent config overlay entirely) went unnoticed. This exercises both
// branches: an agent with no Config must get the base searcher back
// unchanged; an agent WITH Config must get back a distinct overlaid
// searcher (proving CloneWithSiteConfigReader/NewAgentOverlay actually ran,
// not merely that the closure exists).
func TestBuildTeamParamsSearcherForAgentAppliesOverlay(t *testing.T) {
	base := &vector.SearchService{}
	p := BuildTeamParams(context.Background(), TeamParamsInput{
		KbID: "kb1", Agent: &agentteams.AgentRecord{ID: "a1"},
		SiteCfg: &fakeSiteConfigReader{}, SearchService: base,
	})
	if p.SearcherForAgent == nil {
		t.Fatal("SearcherForAgent must be set")
	}

	if got := p.SearcherForAgent(agentteams.AgentRecord{ID: "a1"}); got != base {
		t.Fatalf("empty Config: expected the base searcher unchanged, got %v", got)
	}

	got := p.SearcherForAgent(agentteams.AgentRecord{
		ID: "a1", Config: map[string]string{"rerank_blend_alpha": "0.5"},
	})
	if got == base {
		t.Fatal("non-empty Config: expected a distinct overlaid searcher, got the same base instance back — the per-agent config overlay was not applied")
	}
	if _, ok := got.(*vector.SearchService); !ok {
		t.Fatalf("expected the overlay result to still be a *vector.SearchService, got %T", got)
	}
}
