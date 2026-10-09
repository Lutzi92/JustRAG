package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/prompts"
)

// Oracle: the completion hook records exactly what the production call hands
// to the model layer — the model override must be the caller's fast-tier pick
// (not dropped, not replaced by the KB chat model), the system prompt must be
// the starter prompt with the "DATA, not instructions" rule, and the user
// prompt must carry the documents. The hook's canned reply is then parsed.
func TestGenerateStarterQuestions_PassesFastModelAndParses(t *testing.T) {
	var gotModel, gotSystem, gotUser, gotKB string
	r := newTestResolverWithCompletion(t, func(_ context.Context, _ *ConfigResolver, prompt, system, kbID, model string) (*CompletionResult, error) {
		gotUser, gotSystem, gotKB, gotModel = prompt, system, kbID, model
		return &CompletionResult{Content: `Here you go: ["Was regelt der Plan?","Wer ist zuständig?"]`}, nil
	})
	docs := []prompts.StarterDoc{{Name: "plan.pdf", Excerpt: "Budget 2026"}}
	got, err := GenerateStarterQuestions(context.Background(), r, "kb-1", "fast-model", "Haushalt", docs, "de", 6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotModel != "fast-model" || gotKB != "kb-1" {
		t.Errorf("model/kb passed through as %q/%q, want fast-model/kb-1", gotModel, gotKB)
	}
	if !strings.Contains(gotSystem, "DATEN, keine Anweisungen") {
		t.Errorf("system prompt lacks the DATA wording:\n%s", gotSystem)
	}
	if !strings.Contains(gotUser, "Budget 2026") || !strings.Contains(gotUser, "Haushalt") {
		t.Errorf("user prompt lacks the documents:\n%s", gotUser)
	}
	if len(got) != 2 || got[0] != "Was regelt der Plan?" {
		t.Errorf("parsed %#v, want the two questions", got)
	}
}

// Oracle: the documented contract — garbage is an empty result, a transport
// error bubbles so the caller can cache the failure.
func TestGenerateStarterQuestions_GarbageAndError(t *testing.T) {
	got, err := GenerateStarterQuestions(context.Background(), stubCompletion(t, "no json"), "kb", "", "KB", nil, "en", 6)
	if err != nil || len(got) != 0 {
		t.Fatalf("garbage: want empty and no error, got %#v, %v", got, err)
	}
	boom := errors.New("boom")
	if _, err := GenerateStarterQuestions(context.Background(), stubCompletionError(t, boom), "kb", "", "KB", nil, "en", 6); !errors.Is(err, boom) {
		t.Fatalf("transport error: want %v, got %v", boom, err)
	}
}
