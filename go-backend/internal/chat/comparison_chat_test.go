package chat

import (
	"context"
	"strings"
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

// A panic inside one section worker must not take the process down: the
// engine is fail-open by design (see TestRunComparisonEnginePartialFailure),
// and every other fan-out site in the package recovers. Without recovery a
// nil-deref in search or the LLM path crashes the whole server.
func TestRunComparisonEngineSurvivesSectionPanic(t *testing.T) {
	params := ComparisonChatParams{
		KbID: "kb1", Language: "en", Modes: []string{"contradiction"},
		Sections: []string{"boom", "ok"}, MaxSections: 60, Concurrency: 2, PeersPerSection: 5,
	}
	search := func(ctx context.Context, kbID, q string, n int, o vector.SearchOptions) (*vector.SearchResult, error) {
		if q == "boom" {
			panic("simulated nil-deref in peer search")
		}
		return &vector.SearchResult{}, nil
	}
	structured := func(ctx context.Context, p, s, k, m string, sp *ai.StructuredSpec) (string, error) {
		return `{"findings":[{"severity":"low","uploadQuote":"x","issue":"y","citedFileIds":[],"citedQuote":""}]}`, nil
	}

	res, err := runComparisonEngine(context.Background(), params, search, structured, func(map[string]any) {})
	if err != nil {
		t.Fatalf("engine should not hard-fail on a section panic: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected the healthy section's finding to survive, got %d", len(res.Findings))
	}
}

// A panic raised while the shared-state mutex is held must not leave it
// locked: every remaining section would block on Lock and wg.Wait would never
// return. emit is a caller-supplied sink (the SSE relay), so it is the most
// realistic place for that to happen.
func TestRunComparisonEngineSurvivesEmitPanic(t *testing.T) {
	params := ComparisonChatParams{
		KbID: "kb1", Language: "en", Modes: []string{"contradiction"},
		Sections: []string{"s0", "s1", "s2"}, MaxSections: 60, Concurrency: 1, PeersPerSection: 5,
	}
	search := func(ctx context.Context, kbID, q string, n int, o vector.SearchOptions) (*vector.SearchResult, error) {
		return &vector.SearchResult{}, nil
	}
	structured := func(ctx context.Context, p, s, k, m string, sp *ai.StructuredSpec) (string, error) {
		return `{"findings":[{"severity":"low","uploadQuote":"x","issue":"y","citedFileIds":[],"citedQuote":""}]}`, nil
	}
	emitted := 0
	emit := func(map[string]any) {
		emitted++
		if emitted == 1 {
			panic("simulated panic in the SSE sink, under the mutex")
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := runComparisonEngine(context.Background(), params, search, structured, emit); err != nil {
			t.Errorf("engine should not hard-fail: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("engine deadlocked: a panic under the mutex left it locked")
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

// --- Follow-up over a prior upload -----------------------------------------
//
// The whole reason the document comparison stayed a chat turn (rather than
// becoming a workspace artifact) is that the user can keep asking about the
// uploaded document afterwards. That promise rests on two pieces which had no
// test at all: the gate that decides a turn carries a prior upload, and the
// block that renders the document into the system prompt.

func TestShouldInjectFollowUpContext(t *testing.T) {
	cases := []struct {
		name          string
		attachmentID  string
		runComparison bool
		hasStore      bool
		want          bool
	}{
		{"follow-up turn over a prior upload", "att1", false, true, true},
		{"fresh comparison run gets the document from the orchestrator", "att1", true, true, false},
		{"no attachment at all", "", false, true, false},
		{"store not wired", "att1", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldInjectFollowUpContext(tc.attachmentID, tc.runComparison, tc.hasStore); got != tc.want {
				t.Errorf("shouldInjectFollowUpContext(%q, %v, %v) = %v, want %v",
					tc.attachmentID, tc.runComparison, tc.hasStore, got, tc.want)
			}
		})
	}
}

func TestBuildFollowUpContextCarriesDocumentAndFindings(t *testing.T) {
	att := chatattach.Attachment{
		Filename: "entwurf_v3.docx",
		FullText: "Die Frist betraegt vier Wochen.",
		Findings: []chatattach.Finding{{Mode: "contradiction", Severity: "high", Issue: "Frist weicht ab"}},
	}
	got := buildFollowUpContext(att)

	for _, want := range []string{"entwurf_v3.docx", "Die Frist betraegt vier Wochen.", "Frist weicht ab"} {
		if !strings.Contains(got, want) {
			t.Errorf("follow-up context is missing %q:\n%s", want, got)
		}
	}
}

func TestBuildFollowUpContextCapsLongDocuments(t *testing.T) {
	att := chatattach.Attachment{
		Filename: "gross.docx",
		FullText: strings.Repeat("ä", 10000),
	}
	got := buildFollowUpContext(att)

	if len([]rune(got)) > 5000 {
		t.Errorf("uncapped follow-up context (%d runes) would crowd out the KB prompt", len([]rune(got)))
	}
	if !strings.Contains(got, "…") {
		t.Error("a truncated document should say so, otherwise the model treats a cut-off text as complete")
	}
}
