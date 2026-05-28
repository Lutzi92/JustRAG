package ai

import (
	"context"
	"errors"
	"testing"
)

// fakeCritiqueResult lets the test control the LLM result without spinning
// up a resolver.
type fakeCritiqueResult struct {
	resp string
	err  error
}

// stubResolver builds a hook-bearing *ConfigResolver and returns it. Callers
// pass it as the resolver argument to the function under test instead of nil,
// so the per-resolver completionHook intercepts the LLM call. See item #2 of
// the 2026-05 external review.
func stubResolver(t *testing.T, result fakeCritiqueResult) *ConfigResolver {
	t.Helper()
	return newTestResolverWithCompletion(t, func(_ context.Context, _ *ConfigResolver, _, _, _, _ string) (*CompletionResult, error) {
		if result.err != nil {
			return nil, result.err
		}
		return &CompletionResult{Content: result.resp}, nil
	})
}

func TestNeedsMoreInfo_SufficientReturnsTrueAndEmptyFollowUp(t *testing.T) {
	r := stubResolver(t, fakeCritiqueResult{resp: `{"sufficient": true}`})

	sufficient, followUp, err := NeedsMoreInfo(context.Background(), r, "", "What is X?", "[1] X is Y.", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sufficient {
		t.Error("sufficient: got false, want true")
	}
	if followUp != "" {
		t.Errorf("followUp: got %q, want empty", followUp)
	}
}

func TestNeedsMoreInfo_InsufficientReturnsFalseAndFollowUp(t *testing.T) {
	r := stubResolver(t, fakeCritiqueResult{resp: `{"sufficient": false, "follow_up_query": "what color is X"}`})

	sufficient, followUp, err := NeedsMoreInfo(context.Background(), r, "", "What is X?", "[1] X exists.", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sufficient {
		t.Error("sufficient: got true, want false")
	}
	if followUp != "what color is X" {
		t.Errorf("followUp: got %q, want \"what color is X\"", followUp)
	}
}

func TestNeedsMoreInfo_LLMErrorBubbles(t *testing.T) {
	r := stubResolver(t, fakeCritiqueResult{err: errors.New("simulated network error")})

	_, _, err := NeedsMoreInfo(context.Background(), r, "", "Q?", "[1] c.", "en", "")
	if err == nil {
		t.Error("expected LLM error to bubble up")
	}
}

func TestNeedsMoreInfo_GarbageJSONReturnsError(t *testing.T) {
	r := stubResolver(t, fakeCritiqueResult{resp: `not JSON at all`})

	_, _, err := NeedsMoreInfo(context.Background(), r, "", "Q?", "[1] c.", "en", "")
	if err == nil {
		t.Error("expected parse error for non-JSON response")
	}
}

func TestNeedsMoreInfo_PartialJSON_InsufficientWithEmptyFollowUpYieldsBail(t *testing.T) {
	// Some LLMs may return {"sufficient": false} without follow_up_query.
	// The function should treat that as a valid "insufficient + no query"
	// signal and return followUp=="", letting the orchestrator bail with
	// outcome="early_stop_no_query".
	r := stubResolver(t, fakeCritiqueResult{resp: `{"sufficient": false}`})

	sufficient, followUp, err := NeedsMoreInfo(context.Background(), r, "", "Q?", "[1] c.", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sufficient {
		t.Error("sufficient: got true, want false")
	}
	if followUp != "" {
		t.Errorf("followUp: got %q, want empty (LLM didn't propose one)", followUp)
	}
}
