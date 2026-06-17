package chat

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chatattach"
	"github.com/justrag/go-backend/internal/vector"
)

func TestRunComparisonEngineAggregates(t *testing.T) {
	params := ComparisonChatParams{
		KbID:            "kb1",
		Language:        "en",
		Modes:           []string{"contradiction", "formal"},
		Sections:        []string{"Module A — ECTS: 6", "Exam: written"},
		MaxSections:     60,
		Concurrency:     2,
		PeersPerSection: 5,
	}
	search := func(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error) {
		return &vector.SearchResult{Chunks: []vector.SearchChunk{
			{FileID: "F1", Content: "Module A — ECTS: 5", ID: "h1"},
		}}, nil
	}
	structured := func(ctx context.Context, prompt, system, kbID, model string, spec *ai.StructuredSpec) (string, error) {
		low := toLowerASCIITest(system)
		if containsTest(low, "contradict") || containsTest(low, "widerspr") {
			return `{"findings":[{"severity":"high","uploadQuote":"ECTS: 6","issue":"ECTS differs","citedFileIds":["F1"],"citedQuote":"ECTS: 5"}]}`, nil
		}
		return `{"findings":[]}`, nil
	}
	var events []map[string]any
	emit := func(d map[string]any) { events = append(events, d) }

	res, err := runComparisonEngine(context.Background(), params, search, structured, emit)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(res.Findings), res.Findings)
	}
	for _, f := range res.Findings {
		if f.Mode != "contradiction" {
			t.Fatalf("unexpected mode %s", f.Mode)
		}
		if f.SectionIdx < 0 || f.SectionIdx > 1 {
			t.Fatalf("section idx out of range: %d", f.SectionIdx)
		}
	}
	traj := 0
	for _, e := range events {
		if _, ok := e["agentTrajectory"]; ok {
			traj++
		}
	}
	if traj == 0 {
		t.Fatal("expected per-section trajectory events")
	}
}

func TestRunComparisonEngineSectionCap(t *testing.T) {
	params := ComparisonChatParams{
		KbID: "kb1", Language: "en", Modes: []string{"contradiction"},
		Sections:    []string{"a", "b", "c", "d"},
		MaxSections: 2, Concurrency: 1, PeersPerSection: 5,
	}
	search := func(ctx context.Context, kbID, q string, n int, o vector.SearchOptions) (*vector.SearchResult, error) {
		return &vector.SearchResult{}, nil
	}
	structured := func(ctx context.Context, p, s, k, m string, sp *ai.StructuredSpec) (string, error) {
		return `{"findings":[]}`, nil
	}
	res, err := runComparisonEngine(context.Background(), params, search, structured, func(map[string]any) {})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if !res.Truncated || res.SectionsAnalyzed != 2 {
		t.Fatalf("expected truncation at 2, got analyzed=%d truncated=%v", res.SectionsAnalyzed, res.Truncated)
	}
}

func TestRunComparisonEnginePartialFailure(t *testing.T) {
	params := ComparisonChatParams{
		KbID: "kb1", Language: "en", Modes: []string{"contradiction"},
		Sections: []string{"a", "b"}, MaxSections: 60, Concurrency: 1, PeersPerSection: 5,
	}
	search := func(ctx context.Context, kbID, q string, n int, o vector.SearchOptions) (*vector.SearchResult, error) {
		return &vector.SearchResult{}, nil
	}
	calls := 0
	structured := func(ctx context.Context, p, s, k, m string, sp *ai.StructuredSpec) (string, error) {
		calls++
		if calls == 1 {
			return "", context.DeadlineExceeded
		}
		return `{"findings":[{"severity":"low","uploadQuote":"x","issue":"y","citedFileIds":[],"citedQuote":""}]}`, nil
	}
	res, err := runComparisonEngine(context.Background(), params, search, structured, func(map[string]any) {})
	if err != nil {
		t.Fatalf("engine should not hard-fail on one section error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 surviving finding, got %d", len(res.Findings))
	}
}

func TestRunComparisonEngineBackfillsCitedFiles(t *testing.T) {
	params := ComparisonChatParams{
		KbID: "kb1", Language: "en", Modes: []string{"contradiction"},
		Sections: []string{"s0"}, MaxSections: 60, Concurrency: 1, PeersPerSection: 5,
	}
	search := func(ctx context.Context, kbID, q string, n int, o vector.SearchOptions) (*vector.SearchResult, error) {
		return &vector.SearchResult{Chunks: []vector.SearchChunk{{FileID: "F2", Content: "x", ID: "c1"}}}, nil
	}
	// model returns a finding with EMPTY citedFileIds -> engine must back-fill from peers (F2)
	structured := func(ctx context.Context, p, s, k, m string, sp *ai.StructuredSpec) (string, error) {
		return `{"findings":[{"severity":"medium","uploadQuote":"u","issue":"i","citedFileIds":[],"citedQuote":""}]}`, nil
	}
	res, err := runComparisonEngine(context.Background(), params, search, structured, func(map[string]any) {})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if len(res.Findings) != 1 || len(res.Findings[0].CitedFileIDs) != 1 || res.Findings[0].CitedFileIDs[0] != "F2" {
		t.Fatalf("expected back-filled CitedFileIDs=[F2], got %+v", res.Findings)
	}
}

func TestRunComparisonEngineSortsBySeverity(t *testing.T) {
	params := ComparisonChatParams{
		KbID: "kb1", Language: "en", Modes: []string{"contradiction"},
		Sections: []string{"s0", "s1"}, MaxSections: 60, Concurrency: 1, PeersPerSection: 5,
	}
	search := func(ctx context.Context, kbID, q string, n int, o vector.SearchOptions) (*vector.SearchResult, error) {
		return &vector.SearchResult{}, nil
	}
	calls := 0
	structured := func(ctx context.Context, p, s, k, m string, sp *ai.StructuredSpec) (string, error) {
		calls++
		if calls == 1 {
			return `{"findings":[{"severity":"low","uploadQuote":"u","issue":"i","citedFileIds":[],"citedQuote":""}]}`, nil
		}
		return `{"findings":[{"severity":"high","uploadQuote":"u","issue":"i","citedFileIds":[],"citedQuote":""}]}`, nil
	}
	res, err := runComparisonEngine(context.Background(), params, search, structured, func(map[string]any) {})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if len(res.Findings) != 2 || res.Findings[0].Severity != "high" || res.Findings[1].Severity != "low" {
		t.Fatalf("expected high before low, got %+v", res.Findings)
	}
}

func TestRunComparisonEngineDedupsSources(t *testing.T) {
	params := ComparisonChatParams{
		KbID: "kb1", Language: "en", Modes: []string{"contradiction"},
		Sections: []string{"s0", "s1"}, MaxSections: 60, Concurrency: 1, PeersPerSection: 5,
	}
	// both sections retrieve the SAME chunk id -> sources must dedup to 1
	search := func(ctx context.Context, kbID, q string, n int, o vector.SearchOptions) (*vector.SearchResult, error) {
		return &vector.SearchResult{Chunks: []vector.SearchChunk{{FileID: "F1", Content: "x", ID: "same"}}}, nil
	}
	structured := func(ctx context.Context, p, s, k, m string, sp *ai.StructuredSpec) (string, error) {
		return `{"findings":[]}`, nil
	}
	res, err := runComparisonEngine(context.Background(), params, search, structured, func(map[string]any) {})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if len(res.Sources) != 1 {
		t.Fatalf("expected deduped sources len 1, got %d", len(res.Sources))
	}
}

// tiny case-insensitive helpers, test-local (avoid coupling to engine internals)
func toLowerASCIITest(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}
func containsTest(s, sub string) bool {
	return len(sub) == 0 || indexTest(s, sub) >= 0
}
func indexTest(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRunComparisonChatSetsFinalChunks(t *testing.T) {
	att := chatattach.Attachment{
		ID: "att1", UserID: "u1", KbID: "kb1",
		Filename: "m.md", Sections: []string{"ECTS: 6"},
	}
	store := chatattach.NewInMemoryStore(time.Hour)
	id, _ := store.Put(context.Background(), att)

	search := func(ctx context.Context, kbID, q string, n int, o vector.SearchOptions) (*vector.SearchResult, error) {
		return &vector.SearchResult{Chunks: []vector.SearchChunk{{FileID: "F1", Content: "ECTS: 5", ID: "h1"}}}, nil
	}
	structured := func(ctx context.Context, p, s, k, m string, sp *ai.StructuredSpec) (string, error) {
		return `{"findings":[{"severity":"high","uploadQuote":"ECTS: 6","issue":"diff","citedFileIds":["F1"],"citedQuote":"ECTS: 5"}]}`, nil
	}

	deps := ComparisonDeps{Store: store, Search: search, Structured: structured}
	params := ComparisonChatParams{
		KbID: "kb1", ChatID: "c1", Language: "en",
		Modes: []string{"contradiction"}, MaxSections: 60, Concurrency: 1, PeersPerSection: 5,
	}
	cc, findings, err := RunComparisonChat(context.Background(), deps, id, "u1", params, func(map[string]any) {})
	if err != nil {
		t.Fatalf("RunComparisonChat: %v", err)
	}
	if cc == nil || len(cc.FinalChunks) == 0 {
		t.Fatal("cited chunks must be set on ChatContext (eval recall depends on it)")
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	got, _ := store.Get(context.Background(), id)
	if len(got.Findings) != 1 {
		t.Fatalf("findings not persisted: %+v", got.Findings)
	}
}

func TestRunComparisonChatAuthz(t *testing.T) {
	store := chatattach.NewInMemoryStore(time.Hour)
	id, _ := store.Put(context.Background(), chatattach.Attachment{ID: "att1", UserID: "owner", KbID: "kb1", Sections: []string{"x"}})
	called := false
	deps := ComparisonDeps{Store: store,
		Search: func(context.Context, string, string, int, vector.SearchOptions) (*vector.SearchResult, error) {
			called = true
			return &vector.SearchResult{}, nil
		},
		Structured: func(context.Context, string, string, string, string, *ai.StructuredSpec) (string, error) {
			called = true
			return `{"findings":[]}`, nil
		}}
	_, _, err := RunComparisonChat(context.Background(), deps, id, "intruder", ComparisonChatParams{KbID: "kb1", Modes: []string{"formal"}, Concurrency: 1, MaxSections: 60, PeersPerSection: 5}, func(map[string]any) {})
	if err == nil {
		t.Fatal("expected authz error for non-owner")
	}
	if called {
		t.Fatal("engine must not run for a non-owner (authz must precede the engine)")
	}
}
