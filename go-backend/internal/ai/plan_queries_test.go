package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/aibudget"
)

func TestPlanQueries_HappyPath(t *testing.T) {
	r := stubCompletion(t, `{"queries":["What is A?","What is B?"],"rationale":"two-part question"}`)
	queries, rat, err := PlanQueries(context.Background(), r, "", "What are A and B?", "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(queries) != 2 || queries[0] != "What is A?" || queries[1] != "What is B?" {
		t.Errorf("queries: want [What is A?, What is B?], got %#v", queries)
	}
	if rat != "two-part question" {
		t.Errorf("rationale: got %q", rat)
	}
}

func TestPlanQueries_TolersJSONPreamble(t *testing.T) {
	r := stubCompletion(t, "Here is the JSON:\n{\"queries\":[\"q1\"],\"rationale\":\"single\"}\nDone.")
	queries, _, err := PlanQueries(context.Background(), r, "", "q?", "en", "")
	if err != nil {
		t.Fatalf("unexpected err with preamble: %v", err)
	}
	if len(queries) != 1 || queries[0] != "q1" {
		t.Errorf("queries: want [q1], got %#v", queries)
	}
}

func TestPlanQueries_LLMErrorBubbles(t *testing.T) {
	r := stubCompletionError(t, errors.New("boom"))
	_, _, err := PlanQueries(context.Background(), r, "", "q?", "en", "")
	if err == nil {
		t.Fatalf("want error, got nil")
	}
}

func TestPlanQueries_GarbageJSONReturnsError(t *testing.T) {
	r := stubCompletion(t, "this is not json")
	_, _, err := PlanQueries(context.Background(), r, "", "q?", "en", "")
	if err == nil {
		t.Fatalf("want parse error, got nil")
	}
}

func TestPlanQueries_EmptyQueriesArrayReturnsEmpty(t *testing.T) {
	// Caller (orchestrator) decides to fall back to single-query path on empty.
	r := stubCompletion(t, `{"queries":[],"rationale":"too vague"}`)
	queries, rat, err := PlanQueries(context.Background(), r, "", "q?", "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(queries) != 0 {
		t.Errorf("queries: want empty, got %#v", queries)
	}
	if rat != "too vague" {
		t.Errorf("rationale: got %q", rat)
	}
}

func TestPlanQueries_TrimsWhitespacePerQuery(t *testing.T) {
	r := stubCompletion(t, `{"queries":["  q1  ","\tq2\n"],"rationale":""}`)
	queries, _, err := PlanQueries(context.Background(), r, "", "q?", "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(queries) != 2 || queries[0] != "q1" || queries[1] != "q2" {
		t.Errorf("queries: want trimmed [q1, q2], got %#v", queries)
	}
}

func TestPlanQueries_RecordsTokensToAIBudget(t *testing.T) {
	r := stubCompletionWithTokens(t, `{"queries":["q1"],"rationale":"r"}`, 100, 50)
	ctx := aibudget.New(context.Background(), 10000)
	_, _, err := PlanQueries(ctx, r, "", "q?", "en", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := aibudget.Used(ctx); got != 150 {
		t.Errorf("aibudget.Used: want 150 (100+50), got %d", got)
	}
}

// stubCompletionWithTokens returns a *ConfigResolver whose completion hook
// returns a fixed body + token counts. Callers pass it as the resolver
// argument instead of nil. Replaces the old set-global pattern.
func stubCompletionWithTokens(t *testing.T, body string, promptTokens, completionTokens int) *ConfigResolver {
	t.Helper()
	return newTestResolverWithCompletion(t, func(ctx context.Context, resolver *ConfigResolver, prompt, systemPrompt, kbID, modelOverride string) (*CompletionResult, error) {
		return &CompletionResult{Content: body, PromptTokens: promptTokens, CompletionTokens: completionTokens}, nil
	})
}

// stubCompletion returns a *ConfigResolver whose completion hook returns the
// given response. Callers pass it as the resolver argument instead of nil.
func stubCompletion(t *testing.T, resp string) *ConfigResolver {
	t.Helper()
	return newTestResolverWithCompletion(t, func(_ context.Context, _ *ConfigResolver, _, _, _, _ string) (*CompletionResult, error) {
		return &CompletionResult{Content: resp}, nil
	})
}

// stubCompletionError returns a *ConfigResolver whose completion hook returns
// the given error.
func stubCompletionError(t *testing.T, err error) *ConfigResolver {
	t.Helper()
	return newTestResolverWithCompletion(t, func(_ context.Context, _ *ConfigResolver, _, _, _, _ string) (*CompletionResult, error) {
		return nil, err
	})
}
