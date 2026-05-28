package chat

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// TestDecideCRAGAction_FirstRound exercises the branch table on the first
// pass (alreadyRewritten=false). The ambiguous branch is load-bearing: the
// grader maps LLM/parse errors to ambiguous, so an all-ambiguous result is
// indistinguishable from a degraded grader and must not suppress the answer.
// The single-doc shortcut (relevant>=1 AND relevantFiles==1) prevents
// CRAG's rewrite from destroying queries that correctly hit one document.
// Only a bucket of explicitly-irrelevant chunks (no relevant, no ambiguous)
// triggers an abstain — that is the one case where the grader actually said
// the KB doesn't cover the query.
func TestDecideCRAGAction_FirstRound(t *testing.T) {
	t.Parallel()
	const min = 3
	cases := []struct {
		name          string
		relevant      int
		ambiguous     int
		relevantFiles int
		want          cragAction
	}{
		{"hits min → proceed", 3, 0, 3, cragProceed},
		{"above min → proceed", 5, 0, 4, cragProceed},
		{"below min, relevant spread across 2+ files → rewrite", 2, 0, 2, cragRewrite},
		{"below min, 1 relevant across 1 file → rewrite (indistinguishable)", 1, 0, 1, cragProceed},
		{"below min, 2 relevant both from same file → proceed (single-doc)", 2, 0, 1, cragProceed},
		{"zero relevant, zero ambiguous → abstain", 0, 0, 0, cragAbstain},
		{"zero relevant but ambiguous present → proceed (fail-open)", 0, 5, 0, cragProceed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := decideCRAGAction(c.relevant, c.ambiguous, min, c.relevantFiles, false)
			if got != c.want {
				t.Errorf("relevant=%d ambiguous=%d relevantFiles=%d: got %q, want %q",
					c.relevant, c.ambiguous, c.relevantFiles, got, c.want)
			}
		})
	}
}

// TestDecideCRAGAction_SecondRound: after the rewrite we proceed with anything
// non-irrelevant (>=1 relevant OR >=1 ambiguous) — only a complete miss
// abstains. The ambiguous bucket is counted here because the grader is
// fail-open (parse/LLM errors yield all-ambiguous) and the retry round is
// already the fallback path; dropping usable chunks because the grader flaked
// would violate the fail-open contract. relevantFiles is irrelevant in the
// second round — the decision is purely fail-open.
func TestDecideCRAGAction_SecondRound(t *testing.T) {
	t.Parallel()
	const min = 3
	cases := []struct {
		name      string
		relevant  int
		ambiguous int
		want      cragAction
	}{
		{"any relevant after rewrite → proceed", 1, 0, cragProceed},
		{"under min after rewrite → still proceed", 2, 0, cragProceed},
		{"hits min after rewrite → proceed", 3, 0, cragProceed},
		{"zero relevant but ambiguous present → proceed (fail-open)", 0, 2, cragProceed},
		{"zero relevant zero ambiguous → abstain", 0, 0, cragAbstain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// relevantFiles is unused in the second-round branch, pass anything.
			t.Parallel()

			got := decideCRAGAction(c.relevant, c.ambiguous, min, 0, true)
			if got != c.want {
				t.Errorf("relevant=%d ambiguous=%d: got %q, want %q", c.relevant, c.ambiguous, got, c.want)
			}
		})
	}
}

// TestCountRelevantFiles verifies that the file-count helper only looks at
// chunks graded relevant, and deduplicates by FileID.
func TestCountRelevantFiles(t *testing.T) {
	t.Parallel()
	chunks := []vector.SearchChunk{
		{Grade: ai.GradeRelevant, FileID: "F1"},
		{Grade: ai.GradeRelevant, FileID: "F1"}, // same file, counts once
		{Grade: ai.GradeRelevant, FileID: "F2"},
		{Grade: ai.GradeAmbiguous, FileID: "F3"}, // ambiguous doesn't count
		{Grade: ai.GradeIrrelevant, FileID: "F4"},
		{Grade: "", FileID: "F5"},             // ungraded doesn't count
		{Grade: ai.GradeRelevant, FileID: ""}, // empty FileID ignored
	}
	got := countRelevantFiles(chunks)
	if got != 2 {
		t.Errorf("countRelevantFiles: got %d, want 2 (F1 and F2)", got)
	}
}

// stubReader is a SiteConfigReader that returns canned values.
type stubReader struct{ vals map[string]string }

func (s stubReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := s.vals[key]; ok {
		return &v, nil
	}
	return nil, nil
}

// TestLoadCRAGConfig_Defaults: with no reader (or no overrides), CRAG is off
// and the threshold is 3.
func TestLoadCRAGConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := loadCRAGConfig(context.Background(), nil)
	if cfg.enabled {
		t.Error("expected enabled=false without reader")
	}
	if cfg.minRelevant != 3 {
		t.Errorf("expected minRelevant=3, got %d", cfg.minRelevant)
	}
	if cfg.graderModel != "" {
		t.Errorf("expected empty graderModel, got %q", cfg.graderModel)
	}
}

// TestLoadCRAGConfig_Overrides exercises every key the loader parses.
func TestLoadCRAGConfig_Overrides(t *testing.T) {
	t.Parallel()
	r := stubReader{vals: map[string]string{
		"crag_enabled":             "true",
		"crag_min_relevant_chunks": "5",
		"crag_grader_model":        "  gpt-4o-mini  ", // intentional whitespace
	}}
	cfg := loadCRAGConfig(context.Background(), r)
	if !cfg.enabled {
		t.Error("expected enabled=true")
	}
	if cfg.minRelevant != 5 {
		t.Errorf("expected minRelevant=5, got %d", cfg.minRelevant)
	}
	if cfg.graderModel != "gpt-4o-mini" {
		t.Errorf("expected trimmed graderModel, got %q", cfg.graderModel)
	}
}

// TestLoadCRAGConfig_GraderModelTierFallback: when crag_grader_model
// is unset but model_tier_fast is, the loader uses the tier value
// for the grader. When crag_grader_model is set, it wins
// (per-task override beats tier).
func TestLoadCRAGConfig_GraderModelTierFallback(t *testing.T) {
	t.Parallel()

	// tier only → tier wins
	r := stubReader{vals: map[string]string{
		"crag_enabled":    "true",
		"model_tier_fast": "tier-fast-model",
	}}
	cfg := loadCRAGConfig(context.Background(), r)
	if cfg.graderModel != "tier-fast-model" {
		t.Errorf("tier fallback failed: got graderModel=%q, want tier-fast-model", cfg.graderModel)
	}

	// per-task + tier → per-task wins
	r = stubReader{vals: map[string]string{
		"crag_enabled":      "true",
		"crag_grader_model": "explicit-grader",
		"model_tier_fast":   "tier-fast-model",
	}}
	cfg = loadCRAGConfig(context.Background(), r)
	if cfg.graderModel != "explicit-grader" {
		t.Errorf("per-task should win: got graderModel=%q, want explicit-grader", cfg.graderModel)
	}

	// neither → empty (caller falls back to KB chat model)
	r = stubReader{vals: map[string]string{"crag_enabled": "true"}}
	cfg = loadCRAGConfig(context.Background(), r)
	if cfg.graderModel != "" {
		t.Errorf("neither set: got graderModel=%q, want empty", cfg.graderModel)
	}
}

// TestLoadCRAGConfig_InvalidValues: bad inputs fall back to defaults so a
// fat-fingered admin form can never break ingestion or chat. Covers both
// parse-error ("abc") and out-of-range (0, -1, 99) numeric failures.
func TestLoadCRAGConfig_InvalidValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, enabled, minChunks string
	}{
		{"garbage enabled + zero chunks", "maybe", "0"},
		{"non-numeric chunks", "true", "abc"},
		{"negative chunks", "true", "-1"},
		{"above-range chunks", "true", "99"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := stubReader{vals: map[string]string{
				"crag_enabled":             c.enabled,
				"crag_min_relevant_chunks": c.minChunks,
			}}
			cfg := loadCRAGConfig(context.Background(), r)
			if c.enabled == "maybe" && cfg.enabled {
				t.Error("expected enabled=false on garbage value")
			}
			if cfg.minRelevant != 3 {
				t.Errorf("expected minRelevant=3 on invalid %q, got %d", c.minChunks, cfg.minRelevant)
			}
		})
	}
}

// TestShouldBypassCRAGForRouting exercises the per-request eligibility rule
// that adaptive routing uses to decide whether to disable CRAG. The rule is
// "lookup or enumeration AND empty Enhance" — every other combination must
// keep CRAG running. Phase 3 §A added enumeration to the bypass set because
// BM25-seeded extraction already enforces verified completeness, making
// per-chunk grading redundant for that route.
func TestShouldBypassCRAGForRouting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		queryType string
		enhance   string
		want      bool
	}{
		{"lookup + empty enhance → bypass", vector.QueryTypeLookup, "", true},
		{"enumeration + empty enhance → bypass", vector.QueryTypeEnumeration, "", true},
		{"complex_reasoning + empty enhance → keep", vector.QueryTypeComplexReasoning, "", false},
		{"unknown query type + empty enhance → keep", "", "", false},
		{"lookup + rewrite enhance → keep (explicit quality opt-in)", vector.QueryTypeLookup, "rewrite", false},
		{"enumeration + expand enhance → keep (explicit quality opt-in)", vector.QueryTypeEnumeration, "expand", false},
		{"enumeration + spell enhance → keep", vector.QueryTypeEnumeration, "spell", false},
		{"complex_reasoning + rewrite enhance → keep", vector.QueryTypeComplexReasoning, "rewrite", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := shouldBypassCRAGForRouting(c.queryType, c.enhance)
			if got != c.want {
				t.Errorf("queryType=%q enhance=%q: got %v, want %v",
					c.queryType, c.enhance, got, c.want)
			}
		})
	}
}

// TestCountGrades verifies the chat-layer wrapper that ignores empty Grade
// fields (chunks the grader didn't reach).
func TestCountGrades(t *testing.T) {
	t.Parallel()
	chunks := []vector.SearchChunk{
		{Grade: ai.GradeRelevant},
		{Grade: ai.GradeRelevant},
		{Grade: ai.GradeAmbiguous},
		{Grade: ai.GradeIrrelevant},
		{Grade: ""}, // ungraded — must not count anywhere
	}
	rel, amb, irr := countGrades(chunks)
	if rel != 2 || amb != 1 || irr != 1 {
		t.Errorf("got rel=%d amb=%d irr=%d, want 2/1/1", rel, amb, irr)
	}
}
