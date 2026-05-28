package chat

import (
	"context"
	"strconv"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// fakeReader is a tiny SiteConfigReader that returns canned values for
// individual keys. Used to drive the Resolve* helpers without a DB.
type fakeReader struct {
	values map[string]string
}

func (f *fakeReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := f.values[key]; ok {
		return &v, nil
	}
	return nil, nil
}

func TestResolvePlateauConfig_DefaultsOff(t *testing.T) {
	got := ResolvePlateauConfig(context.Background(), &fakeReader{values: map[string]string{}})
	if got.Enabled {
		t.Errorf("plateau should default to disabled, got %#v", got)
	}
	if got.Chunks != 1 {
		t.Errorf("plateau chunks default = %d, want 1", got.Chunks)
	}
	if got.ScoreDelta != 0.02 {
		t.Errorf("plateau score_delta default = %v, want 0.02", got.ScoreDelta)
	}
}

func TestResolvePlateauConfig_ParsesValues(t *testing.T) {
	got := ResolvePlateauConfig(context.Background(), &fakeReader{values: map[string]string{
		"chat_agentic_plateau_stop":             "true",
		"chat_agentic_plateau_chunks_threshold": "2",
		"chat_agentic_plateau_score_delta":      "0.05",
	}})
	if !got.Enabled {
		t.Errorf("plateau should be enabled when chat_agentic_plateau_stop=true")
	}
	if got.Chunks != 2 {
		t.Errorf("plateau chunks = %d, want 2", got.Chunks)
	}
	if got.ScoreDelta != 0.05 {
		t.Errorf("plateau score_delta = %v, want 0.05", got.ScoreDelta)
	}
}

func TestResolvePlateauConfig_RejectsOutOfRange(t *testing.T) {
	got := ResolvePlateauConfig(context.Background(), &fakeReader{values: map[string]string{
		"chat_agentic_plateau_stop":             "true",
		"chat_agentic_plateau_chunks_threshold": "999",
		"chat_agentic_plateau_score_delta":      "5.0",
	}})
	// Out-of-range values fall back to defaults; the master switch stays on.
	if got.Chunks != 1 {
		t.Errorf("out-of-range chunks should fall back to default 1, got %d", got.Chunks)
	}
	if got.ScoreDelta != 0.02 {
		t.Errorf("out-of-range score_delta should fall back to default 0.02, got %v", got.ScoreDelta)
	}
}

// chunkScored builds a chunk with a configurable score, so we can drive
// the topChunkScore-based plateau check.
func chunkScored(id, file string, score float64) vector.SearchChunk {
	return vector.SearchChunk{ID: id, FileID: file, FileName: file, Content: id + " body", Score: score}
}

// TestRunAgenticChat_PlateauEarlyStop drives a declining-yield search and
// asserts the loop bails with outcome=early_stop_plateau after two flat
// rounds. The first hop returns a strong chunk; subsequent hops return one
// new (deduplicated) chunk each but with marginal score gains, so:
//
//	hop 1: top=0.90, accumulated=1
//	hop 2: top=0.91 → delta 0.01 < 0.02 AND added=1 ≤ 1 → streak=1
//	hop 3: top=0.91 → delta 0.00 < 0.02 AND added=1 ≤ 1 → streak=2 → BREAK
func TestRunAgenticChat_PlateauEarlyStop(t *testing.T) {
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{
			"original": {Chunks: []vector.SearchChunk{chunkScored("c1", "f1", 0.90)}},
			"f1":       {Chunks: []vector.SearchChunk{chunkScored("c2", "f2", 0.91)}},
			"f2":       {Chunks: []vector.SearchChunk{chunkScored("c3", "f3", 0.91)}},
		},
	}
	critique := &stubCritiqueQueue{responses: []critiqueDecision{
		{sufficient: false, followUp: "f1"},
		{sufficient: false, followUp: "f2"},
		{sufficient: false, followUp: "f3"},
	}}

	beforeOutcome := agenticOutcomeCount("early_stop_plateau")

	params := AgenticChatParams{
		KbID:     "kb1",
		Query:    "original",
		Language: "en",
		MaxHops:  5,
		Plateau:  PlateauConfig{Enabled: true, Chunks: 1, ScoreDelta: 0.02},
	}
	if _, err := runAgenticChatTestable(context.Background(), nil, searcher, critique.next, params, func(map[string]any) {}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := agenticOutcomeCount("early_stop_plateau") - beforeOutcome; got != 1 {
		t.Errorf("early_stop_plateau counter delta: got %v, want 1", got)
	}
	// Sanity: the loop ran at most 3 hops (hop 2 + hop 3 = streak limit
	// reached). A 4th hop would mean the plateau check didn't fire.
	if len(searcher.calls) > 3 {
		t.Errorf("plateau should bail by hop 3; saw %d searches: %#v", len(searcher.calls), searcher.calls)
	}
}

// TestRunAgenticChat_PlateauDisabledKeepsLegacyBehavior is the negative
// control: with the same declining-yield trace, the loop should run to
// max_hops_reached when the plateau flag is off.
func TestRunAgenticChat_PlateauDisabledKeepsLegacyBehavior(t *testing.T) {
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{
			"original": {Chunks: []vector.SearchChunk{chunkScored("c1", "f1", 0.90)}},
			"f1":       {Chunks: []vector.SearchChunk{chunkScored("c2", "f2", 0.91)}},
			"f2":       {Chunks: []vector.SearchChunk{chunkScored("c3", "f3", 0.91)}},
		},
	}
	critique := &stubCritiqueQueue{responses: []critiqueDecision{
		{sufficient: false, followUp: "f1"},
		{sufficient: false, followUp: "f2"},
	}}

	beforeOutcome := agenticOutcomeCount("max_hops_reached")

	params := AgenticChatParams{
		KbID:     "kb1",
		Query:    "original",
		Language: "en",
		MaxHops:  3,
		// Plateau zero-value → disabled.
	}
	if _, err := runAgenticChatTestable(context.Background(), nil, searcher, critique.next, params, func(map[string]any) {}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := agenticOutcomeCount("max_hops_reached") - beforeOutcome; got != 1 {
		t.Errorf("max_hops_reached counter delta: got %v, want 1 (plateau disabled should not short-circuit)", got)
	}
}

// TestRunPlanExecuteChat_PlateauEarlyStop mirrors the agentic test on the
// plan-execute orchestrator. Each iterate round adds one new chunk with a
// negligible score gain; the plateau streak hits 2 and the outcome is
// early_stop_plateau.
func TestRunPlanExecuteChat_PlateauEarlyStop(t *testing.T) {
	plan := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _ string) ([]string, string, error) {
		return []string{"sub1"}, "rationale", nil
	}
	round := 0
	iter := func(_ context.Context, _ *ai.ConfigResolver, _, _, _ string, _ int, _, _ string) (string, []string, string, error) {
		round++
		return "search", []string{"q" + strconv.Itoa(round)}, "more", nil
	}
	searcher := &fakeSearcher{
		// Plan returns a strong chunk; each follow-up adds exactly one new
		// chunk at near-identical score so dedup never kicks in but the
		// plateau check does.
		resp: func(query string) []vector.SearchChunk {
			switch query {
			case "Q?":
				return []vector.SearchChunk{chunkScored("c0", "f0", 0.90)}
			default:
				// Use the query string as a unique chunk ID so dedup keeps it.
				return []vector.SearchChunk{chunkScored("c-"+query, "f-"+query, 0.91)}
			}
		},
	}

	_, err := runPlanExecuteChatTestable(
		context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{
			KbID: "kb", Query: "Q?", Language: "en",
			MaxSubQueries: 3, MaxIterations: 5, TokenBudget: 8000,
			Plateau: PlateauConfig{Enabled: true, Chunks: 1, ScoreDelta: 0.02},
		},
		func(map[string]any) {},
	)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Plan + at most 2 iterate rounds (streak limit) before bailing → 3 searches.
	if len(searcher.calls) > 3 {
		t.Errorf("plateau should bail by round 2; saw %d searches", len(searcher.calls))
	}
}
