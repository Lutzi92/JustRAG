package openaicompat

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

// stubDateLookup is a minimal chat.FileDateLookup for identity checks —
// contextParams must thread the handler's lookup through unchanged, not a
// wrapper or a copy.
type stubDateLookup struct{}

func (stubDateLookup) FileDatesByIDs(_ context.Context, _ []string) (map[string]chat.FileDates, error) {
	return nil, nil
}

// contextParams is the extracted builder behind the inline
// chat.ChatContextParams{} literal at the chat-completions call site. It
// must carry the handler's fileDates lookup (W6-R2) — this surface also
// passes a nil site-config reader to PrepareChatContext by design (OpenAI
// clients don't read site_configs), so FileDates costs nothing here today,
// but it is read only by the conflict detector and keeps this handler
// uniform with the other answering surfaces.
//
// Mutation: drop the `FileDates: h.fileDates` line from contextParams →
// this fails.
func TestContextParamsCarriesFileDates(t *testing.T) {
	lookup := stubDateLookup{}
	h := &Handler{fileDates: lookup}

	params := h.contextParams("kb-1", "search query", "kb system prompt")

	if params.FileDates == nil {
		t.Fatal("params.FileDates is nil; contextParams did not thread h.fileDates")
	}
	if params.FileDates != lookup {
		t.Errorf("params.FileDates = %#v, want the exact stub instance %#v", params.FileDates, lookup)
	}
}

// Every pre-existing field on the literal must still round-trip through the
// extracted method, including the hardcoded Language: "en" (OpenAI-compat
// clients carry no language signal).
func TestContextParamsPreservesExistingFields(t *testing.T) {
	h := &Handler{}

	params := h.contextParams("kb-1", "search query", "kb system prompt")

	if params.KbID != "kb-1" {
		t.Errorf("KbID = %q, want %q", params.KbID, "kb-1")
	}
	if params.SearchQuery != "search query" {
		t.Errorf("SearchQuery = %q, want %q", params.SearchQuery, "search query")
	}
	if params.Language != "en" {
		t.Errorf("Language = %q, want %q", params.Language, "en")
	}
	if params.KbSystemPrompt != "kb system prompt" {
		t.Errorf("KbSystemPrompt = %q, want %q", params.KbSystemPrompt, "kb system prompt")
	}
}
