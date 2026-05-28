package longmem

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestFormatEmbedding_HappyPath: round-trip a small float64 slice
// to its pgvector text-literal shape. Pinned so a future change
// (e.g. switching to %g instead of FormatFloat) doesn't silently
// alter the on-disk format pgvector parses.
func TestFormatEmbedding_HappyPath(t *testing.T) {
	t.Parallel()
	got := formatEmbedding([]float64{1.0, -0.5, 0.0})
	want := "[1,-0.5,0]"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// TestFormatEmbedding_EmptyReturnsEmptyString: callers can detect
// "no embedding to write" by an empty return without checking the
// input slice themselves.
func TestFormatEmbedding_EmptyReturnsEmptyString(t *testing.T) {
	t.Parallel()
	if got := formatEmbedding(nil); got != "" {
		t.Errorf("nil → expected empty, got %q", got)
	}
	if got := formatEmbedding([]float64{}); got != "" {
		t.Errorf("empty → expected empty, got %q", got)
	}
}

// TestInsert_NilPoolErrors: pool=nil returns a clean error rather
// than panicking, even with the new embedding parameter — same v1
// contract.
func TestInsert_NilPoolErrors(t *testing.T) {
	t.Parallel()
	s := NewPgStore(nil)
	if err := s.Insert(context.Background(), "user-1", "", "semantic", "fact", 0.5, nil); err == nil {
		t.Errorf("nil pool, nil embedding: expected error, got nil")
	}
	if err := s.Insert(context.Background(), "user-1", "", "semantic", "fact", 0.5, []float64{1, 2, 3}); err == nil {
		t.Errorf("nil pool, with embedding: expected error, got nil")
	}
}

// TestRecallSemantic_RequiredFields: the early-return contracts —
// missing pool and missing user_id both error. The empty-embedding
// short-circuit is documented to return (nil, nil) but can only be
// verified against a real DB (a nil pool short-circuits sooner);
// the integration test on a live Postgres covers that branch.
func TestRecallSemantic_RequiredFields(t *testing.T) {
	t.Parallel()
	s := NewPgStore(nil)
	if _, err := s.RecallSemantic(context.Background(), "user-1", "", []float64{1, 2, 3}, 5, 30); err == nil {
		t.Errorf("nil pool: expected error, got nil")
	}
	s2 := &PgStore{}
	if _, err := s2.RecallSemantic(context.Background(), "", "", []float64{1, 2, 3}, 5, 30); err == nil {
		t.Errorf("empty user_id: expected error, got nil")
	}
}

// TestFormatMemoriesForPrompt_Empty: nil and empty slice both
// yield the empty string. The chat handler concatenates the
// result with the KB system prompt; an empty string lets that
// concatenation work without nil-checks.
func TestFormatMemoriesForPrompt_Empty(t *testing.T) {
	t.Parallel()
	if got := FormatMemoriesForPrompt(nil); got != "" {
		t.Errorf("nil → expected empty, got %q", got)
	}
	if got := FormatMemoriesForPrompt([]Memory{}); got != "" {
		t.Errorf("empty slice → expected empty, got %q", got)
	}
}

// TestFormatMemoriesForPrompt_GroupingOrder: facts must appear in
// (semantic, procedural, episodic) order regardless of input
// order. The LLM weights the first-mentioned items more strongly
// in its attention; identity facts deserve that position.
func TestFormatMemoriesForPrompt_GroupingOrder(t *testing.T) {
	t.Parallel()
	memories := []Memory{
		{Kind: "episodic", Content: "uploaded contract X today"},
		{Kind: "semantic", Content: "user is a privacy officer"},
		{Kind: "procedural", Content: "checks retention period before review"},
		{Kind: "episodic", Content: "asked about §13 yesterday"},
		{Kind: "semantic", Content: "prefers German answers"},
	}
	got := FormatMemoriesForPrompt(memories)
	idents := []struct {
		s    string
		want int // expected index (lower = earlier)
	}{
		{"[identity] user is a privacy officer", 0},
		{"[identity] prefers German answers", 1},
		{"[workflow] checks retention", 2},
		{"[recent] uploaded contract X today", 3},
		{"[recent] asked about §13 yesterday", 4},
	}
	for i := 0; i < len(idents)-1; i++ {
		if !strings.Contains(got, idents[i].s) || !strings.Contains(got, idents[i+1].s) {
			t.Errorf("missing entries: %v", got)
			continue
		}
		if strings.Index(got, idents[i].s) > strings.Index(got, idents[i+1].s) {
			t.Errorf("order broken: %q should come before %q", idents[i].s, idents[i+1].s)
		}
	}
}

// TestFormatMemoriesForPrompt_LabelsByKind: each kind maps to
// a stable label the LLM has learned to weight. Drift here would
// silently change how the model interprets memory rows.
func TestFormatMemoriesForPrompt_LabelsByKind(t *testing.T) {
	t.Parallel()
	got := FormatMemoriesForPrompt([]Memory{
		{Kind: "semantic", Content: "x"},
		{Kind: "procedural", Content: "y"},
		{Kind: "episodic", Content: "z"},
	})
	for _, want := range []string{"[identity]", "[workflow]", "[recent]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing label %q in %q", want, got)
		}
	}
	// Header is the LLM's reading anchor — pin it.
	if !strings.HasPrefix(got, "Conversation memory") {
		t.Errorf("expected 'Conversation memory' header, got start: %q", got[:30])
	}
}

// TestScore_RecencyDominatesAtShortDecay: at a 1-day decay, a
// fresh fact (last_accessed=now) outscores a 7-day-old fact even
// when the older one has higher salience. This is the design
// intent of the formula.
func TestScore_RecencyDominatesAtShortDecay(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fresh := score(0.5, now, 0, 1, now)                     // mid-salience, just-accessed
	old := score(0.95, now.Add(-7*24*time.Hour), 0, 1, now) // high-salience, 7 days old
	if fresh <= old {
		t.Errorf("at decay=1d, fresh-mid (%f) should beat 7d-old-high (%f)", fresh, old)
	}
}

// TestScore_AccessCountTilts: two equal-recency, equal-salience
// memories where one has been recalled 5 times rank higher.
// Mem0's design: recall itself signals durability.
func TestScore_AccessCountTilts(t *testing.T) {
	t.Parallel()
	now := time.Now()
	never := score(0.5, now, 0, 30, now)
	often := score(0.5, now, 5, 30, now)
	if often <= never {
		t.Errorf("often-accessed (%f) should outrank never-accessed (%f) at equal salience", often, never)
	}
}

// TestScore_NeverAccessedFloor: the (1 + log1p) shape gives
// never-accessed memories a weight of exactly 1.0 (not 0). Without
// that floor a fresh memory would rank zero. Pinned because the
// formula is easy to break by switching log1p for ln directly.
func TestScore_NeverAccessedFloor(t *testing.T) {
	t.Parallel()
	now := time.Now()
	got := score(1.0, now, 0, 30, now)
	// salience=1, decay=0d/30d → exp(0)=1, weight=1+log1p(0)=1+0=1
	// → score = 1*1*1 = 1
	if got < 0.999 || got > 1.001 {
		t.Errorf("expected score≈1.0 for fresh, max-salience, never-accessed; got %f", got)
	}
}

// TestScore_DecayDaysClamp: decayDays < 1 must behave as 1, not
// trigger a divide-by-zero or NaN. The Insert/Recall callers
// clamp at the boundary too, but the score formula is the
// last-mile defence.
func TestScore_DecayDaysClamp(t *testing.T) {
	t.Parallel()
	now := time.Now()
	got := score(0.5, now, 0, 0, now) // decayDays=0 → should treat as 1
	if got != 0.5 {
		t.Errorf("decayDays=0 should clamp to 1 (score=0.5), got %f", got)
	}
}

func TestNewMethods_NilPoolErrors(t *testing.T) {
	t.Parallel()
	s := NewPgStore(nil)
	if _, err := s.ListByUser(context.Background(), "u1"); err == nil {
		t.Error("ListByUser nil pool: expected error")
	}
	if err := s.DeleteByID(context.Background(), "u1", 1); err == nil {
		t.Error("DeleteByID nil pool: expected error")
	}
	if err := s.UpdateEmbedding(context.Background(), "u1", 1, []float64{1, 2}); err == nil {
		t.Error("UpdateEmbedding nil pool: expected error")
	}
	if _, err := s.ListUserIDsWithMemory(context.Background()); err == nil {
		t.Error("ListUserIDsWithMemory nil pool: expected error")
	}
}

func TestDeleteByID_RequiresFields(t *testing.T) {
	t.Parallel()
	s := &PgStore{} // nil pool short-circuits, but empty user must also guard
	if err := s.DeleteByID(context.Background(), "", 1); err == nil {
		t.Error("empty userID: expected error")
	}
}

// TestZeroVectorLiteral builds the pgvector zero literal at an arbitrary dim.
func TestZeroVectorLiteral(t *testing.T) {
	t.Parallel()
	if got := zeroVectorLiteral(3); got != "[0,0,0]" {
		t.Errorf("dim 3: want [0,0,0], got %q", got)
	}
	if got := zeroVectorLiteral(1); got != "[0]" {
		t.Errorf("dim 1: want [0], got %q", got)
	}
	// Exact-string compare so the assertion can't be satisfied by an
	// unintended format change (e.g. float "0.0" elements).
	want4096 := "[" + strings.Repeat("0,", 4095) + "0]"
	if got := zeroVectorLiteral(4096); got != want4096 {
		t.Errorf("dim 4096: literal mismatch (len got=%d want=%d)", len(got), len(want4096))
	}
}
