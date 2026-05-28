package ai

import (
	"context"
	"errors"
	"testing"
)

func TestDecideNextAction_AnswerVerdict(t *testing.T) {
	r := stubCompletion(t, `{"action":"answer","queries":[],"reason":"chunks suffice"}`)
	action, queries, reason, err := DecideNextAction(context.Background(), r, "", "Q?", "[1] c.", 1, "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if action != "answer" {
		t.Errorf("action: want answer, got %q", action)
	}
	if len(queries) != 0 {
		t.Errorf("queries: want empty for answer, got %#v", queries)
	}
	if reason != "chunks suffice" {
		t.Errorf("reason: got %q", reason)
	}
}

func TestDecideNextAction_SearchVerdictWithQueries(t *testing.T) {
	r := stubCompletion(t, `{"action":"search","queries":["q1","q2"],"reason":"missing X"}`)
	action, queries, _, err := DecideNextAction(context.Background(), r, "", "Q?", "[1] c.", 2, "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if action != "search" {
		t.Errorf("action: want search, got %q", action)
	}
	if len(queries) != 2 || queries[0] != "q1" || queries[1] != "q2" {
		t.Errorf("queries: want [q1 q2], got %#v", queries)
	}
}

func TestDecideNextAction_SearchWithEmptyQueriesNormalizesToAnswer(t *testing.T) {
	// Defensive: if the LLM emits action=search with empty queries, the
	// orchestrator can't make progress. Treat it as "answer" so the loop
	// terminates cleanly.
	r := stubCompletion(t, `{"action":"search","queries":[],"reason":"unclear"}`)
	action, queries, _, err := DecideNextAction(context.Background(), r, "", "Q?", "[1] c.", 2, "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if action != "answer" {
		t.Errorf("action: want normalized answer, got %q", action)
	}
	if len(queries) != 0 {
		t.Errorf("queries: should be empty after normalization, got %#v", queries)
	}
}

func TestDecideNextAction_UnknownActionReturnsError(t *testing.T) {
	r := stubCompletion(t, `{"action":"reflect","queries":[],"reason":"hmm"}`)
	_, _, _, err := DecideNextAction(context.Background(), r, "", "Q?", "[1] c.", 1, "en", "")
	if err == nil {
		t.Fatalf("want error for unknown action, got nil")
	}
}

func TestDecideNextAction_LLMErrorBubbles(t *testing.T) {
	r := stubCompletionError(t, errors.New("boom"))
	_, _, _, err := DecideNextAction(context.Background(), r, "", "Q?", "[1] c.", 1, "en", "")
	if err == nil {
		t.Fatalf("want error, got nil")
	}
}

func TestDecideNextAction_GarbageJSONReturnsError(t *testing.T) {
	r := stubCompletion(t, "not json")
	_, _, _, err := DecideNextAction(context.Background(), r, "", "Q?", "[1] c.", 1, "en", "")
	if err == nil {
		t.Fatalf("want parse error, got nil")
	}
}

func TestDecideNextAction_TrimsAndCapsQueries(t *testing.T) {
	// Cap is IterateActionMaxQueries (=3). Helper trims excess and strips
	// whitespace before returning.
	r := stubCompletion(t, `{"action":"search","queries":[" q1 "," q2 "," q3 "," q4 "],"reason":"r"}`)
	_, queries, _, err := DecideNextAction(context.Background(), r, "", "Q?", "[1] c.", 1, "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(queries) != 3 {
		t.Errorf("queries: want 3 (capped), got %d (%#v)", len(queries), queries)
	}
	if queries[0] != "q1" {
		t.Errorf("queries[0]: want q1 (trimmed), got %q", queries[0])
	}
}
