package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// conflictSupSearcher returns three chunks from three distinct files, which
// is what clears the conflict pass's ≥ 2-distinct-files gate.
type conflictSupSearcher struct{}

func (conflictSupSearcher) Search(_ context.Context, _, _ string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	return &vector.SearchResult{Chunks: []vector.SearchChunk{
		supChunk("A", 0.9), supChunk("B", 0.8), supChunk("C", 0.7),
	}}, nil
}

// The Supervisor is the production orchestrator: without its own wiring the
// feature would be invisible on every deployment that runs it, which is why
// this mirrors the standard path's test rather than trusting PrepareChatContext.
func TestRunSupervisorChat_ConflictAddendumAndReport(t *testing.T) {
	calls := 0
	detect := func(_ context.Context, _ *ai.ConfigResolver, _, _ string, blocks []ai.ConflictSource, _, _ string) (*ai.ConflictFindings, error) {
		calls++
		if len(blocks) < 2 {
			t.Errorf("detector got %d blocks, want the assembled source set", len(blocks))
		}
		return &ai.ConflictFindings{Conflicts: []ai.Conflict{
			{Claim: "Frist", SourceA: 1, SourceB: 2, Kind: "superseded", Newer: "b"},
		}}, nil
	}

	ctxOut, err := runSupervisorChatTestable(
		context.Background(),
		nil,
		conflictSupSearcher{},
		func(_, _ string) bool { return false }, // retriever arm
		SupervisorChatParams{
			KbID:           "kb1",
			Query:          "Welche Frist gilt?",
			Language:       "de",
			ConflictConfig: ConflictConfig{Enabled: true, MaxChunks: 12, Timeout: time.Second},
			conflictDetect: detect,
		},
		func(map[string]any) {},
	)
	if err != nil {
		t.Fatalf("runSupervisorChatTestable: %v", err)
	}
	if calls != 1 {
		t.Fatalf("conflict detector calls: got %d, want 1 on the Supervisor path", calls)
	}
	if ctxOut.Conflicts == nil || len(ctxOut.Conflicts.Conflicts) != 1 {
		t.Fatalf("ChatContext.Conflicts = %+v, want one conflict", ctxOut.Conflicts)
	}
	// The names must be resolved from the turn's own (sandwich-ordered)
	// source list, so [1]/[2] name whatever the answer prompt numbered 1/2.
	c := ctxOut.Conflicts.Conflicts[0]
	if c.FileA != ctxOut.Sources[0].FileName || c.FileB != ctxOut.Sources[1].FileName {
		t.Errorf("file names = %q/%q, want the source list's %q/%q",
			c.FileA, c.FileB, ctxOut.Sources[0].FileName, ctxOut.Sources[1].FileName)
	}

	addIdx := strings.Index(ctxOut.SystemPrompt, "WIDERSPRÜCHLICHE QUELLEN")
	ctxIdx := strings.Index(ctxOut.SystemPrompt, "\n\nCONTEXT:\n")
	if addIdx < 0 {
		t.Fatalf("supervisor system prompt is missing the conflict addendum:\n%s", ctxOut.SystemPrompt)
	}
	if addIdx > ctxIdx {
		t.Errorf("conflict addendum must precede the CONTEXT block (at %d vs %d)", addIdx, ctxIdx)
	}
}

// An abstaining turn is about to decline; there is nothing to reconcile
// between its sources, so it must not pay for the fast-tier call.
func TestRunSupervisorChat_ConflictSkippedOnAbstain(t *testing.T) {
	calls := 0
	detect := func(_ context.Context, _ *ai.ConfigResolver, _, _ string, _ []ai.ConflictSource, _, _ string) (*ai.ConflictFindings, error) {
		calls++
		return &ai.ConflictFindings{Conflicts: []ai.Conflict{
			{Claim: "Frist", SourceA: 1, SourceB: 2, Kind: "superseded", Newer: "b"},
		}}, nil
	}
	ctxOut, err := runSupervisorChatTestable(
		context.Background(), nil, conflictSupSearcher{},
		func(_, _ string) bool { return false },
		SupervisorChatParams{
			KbID:     "kb1",
			Query:    "Welche Frist gilt?",
			Language: "de",
			// Drive the Q2 gate to "insufficient" through its test seam —
			// the real ai.JudgeContextSufficiency fails OPEN, so it can
			// never produce an abstain without a live model.
			SufficientContextEnabled: true,
			judgeSufficiency: func(context.Context, *ai.ConfigResolver, string, string, string, string, string) bool {
				return false
			},
			ConflictConfig: ConflictConfig{Enabled: true, MaxChunks: 12, Timeout: time.Second},
			conflictDetect: detect,
		},
		func(map[string]any) {},
	)
	if err != nil {
		t.Fatalf("runSupervisorChatTestable: %v", err)
	}
	if !ctxOut.Abstain {
		t.Fatal("the sufficient-context seam did not produce an abstaining turn")
	}
	if calls != 0 {
		t.Errorf("conflict detector calls: got %d, want 0 on an abstaining turn", calls)
	}
	if ctxOut.Conflicts != nil {
		t.Errorf("ChatContext.Conflicts = %+v, want nil on an abstaining turn", ctxOut.Conflicts)
	}
}

// The default (zero) ConflictConfig must leave the Supervisor byte-identical
// to before the feature existed.
func TestRunSupervisorChat_ConflictDisabledByDefault(t *testing.T) {
	calls := 0
	detect := func(_ context.Context, _ *ai.ConfigResolver, _, _ string, _ []ai.ConflictSource, _, _ string) (*ai.ConflictFindings, error) {
		calls++
		return &ai.ConflictFindings{}, nil
	}
	ctxOut, err := runSupervisorChatTestable(
		context.Background(), nil, conflictSupSearcher{},
		func(_, _ string) bool { return false },
		SupervisorChatParams{KbID: "kb1", Query: "Q", Language: "de", conflictDetect: detect},
		func(map[string]any) {},
	)
	if err != nil {
		t.Fatalf("runSupervisorChatTestable: %v", err)
	}
	if calls != 0 {
		t.Errorf("conflict detector calls: got %d, want 0 with the flag off", calls)
	}
	if ctxOut.Conflicts != nil {
		t.Errorf("ChatContext.Conflicts = %+v, want nil with the flag off", ctxOut.Conflicts)
	}
	if strings.Contains(ctxOut.SystemPrompt, "WIDERSPRÜCHLICHE QUELLEN") {
		t.Error("system prompt carries a conflict addendum with the flag off")
	}
}
