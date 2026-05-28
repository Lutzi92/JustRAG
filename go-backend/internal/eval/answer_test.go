package eval

import (
	"context"
	"strings"
	"testing"
)

type fakeCompleter struct {
	capturedPrompt string
	capturedSystem string
	returnAnswer   string
	returnErr      error
}

func (f *fakeCompleter) Complete(_ context.Context, prompt, systemPrompt string) (string, error) {
	f.capturedPrompt = prompt
	f.capturedSystem = systemPrompt
	if f.returnErr != nil {
		return "", f.returnErr
	}
	return f.returnAnswer, nil
}

func TestGenerateAnswer_StitchesNumberedChunks(t *testing.T) {
	completer := &fakeCompleter{returnAnswer: "stub answer"}
	chunks := []RetrievedChunk{
		{FileID: "f1", Score: 0.9},
		{FileID: "f2", Score: 0.5},
	}
	contents := []string{"First chunk content.", "Second chunk content."}
	fileNames := []string{"alpha.pdf", "beta.docx"}

	ans, err := GenerateAnswer(context.Background(), completer, "What is alpha?", chunks, contents, fileNames, "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans != "stub answer" {
		t.Fatalf("expected stub answer, got %q", ans)
	}
	for _, needle := range []string{"[1]", "[2]", "alpha.pdf", "beta.docx", "First chunk content", "Second chunk content", "What is alpha?"} {
		if !strings.Contains(completer.capturedPrompt, needle) {
			t.Errorf("prompt missing %q\ngot: %s", needle, completer.capturedPrompt)
		}
	}
	if completer.capturedSystem == "" {
		t.Error("expected a non-empty system prompt")
	}
}

func TestGenerateAnswer_PropagatesError(t *testing.T) {
	completer := &fakeCompleter{returnErr: errorsNew("boom")}
	_, err := GenerateAnswer(context.Background(), completer, "q", nil, nil, nil, "en")
	if err == nil {
		t.Fatal("expected error")
	}
}

// errorsNew is a tiny local helper so tests don't depend on errors package.
func errorsNew(s string) error { return &staticErr{msg: s} }

type staticErr struct{ msg string }

func (e *staticErr) Error() string { return e.msg }
