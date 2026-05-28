package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// Completer is the minimal LLM interface the answer generator and judge need.
// ai.GenerateCompletion is adapted to this in cmd/eval/main.go.
type Completer interface {
	Complete(ctx context.Context, prompt, systemPrompt string) (string, error)
}

// GenerateAnswer produces an AI answer to question using the retrieved chunk
// contents as context. It uses the production ChatSystemPrompt so the answer
// reflects what the chat handler would produce (minus sandwich ordering,
// which is acceptable for eval purposes).
//
// chunks, contents, and fileNames must have matching lengths and indices.
func GenerateAnswer(ctx context.Context, completer Completer, question string, chunks []RetrievedChunk, contents, fileNames []string, lang string) (string, error) {
	if len(chunks) != len(contents) || len(chunks) != len(fileNames) {
		return "", fmt.Errorf("GenerateAnswer: chunks (%d), contents (%d), fileNames (%d) length mismatch", len(chunks), len(contents), len(fileNames))
	}

	var ctxBuilder strings.Builder
	for i := range chunks {
		fmt.Fprintf(&ctxBuilder, "[%d] Source: %s\n%s\n\n---\n\n", i+1, fileNames[i], contents[i])
	}

	systemPrompt := prompts.ChatSystemPrompt(lang)
	userPrompt := fmt.Sprintf("%s\n\nQUESTION:\n%s", ctxBuilder.String(), question)

	return completer.Complete(ctx, userPrompt, systemPrompt)
}
