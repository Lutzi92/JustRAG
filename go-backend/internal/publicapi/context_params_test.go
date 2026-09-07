package publicapi

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
// chat.ChatContextParams{} literal at the SendMessage call site. It must
// carry the handler's fileDates lookup (W6-R2) so the conflict /
// supersession detector can render real date lines on this surface once a
// non-nil site-config reader is threaded through — today PrepareChatContext
// is called with a nil reader here by design (CRAG stays off), but
// FileDates is read only by the conflict detector, so threading it now is
// zero-cost and keeps every existing field exactly as it was.
//
// Mutation: drop the `FileDates: h.fileDates` line from contextParams →
// this fails.
func TestContextParamsCarriesFileDates(t *testing.T) {
	lookup := stubDateLookup{}
	h := &Handler{fileDates: lookup}

	params := h.contextParams("kb-1", "search query", "de", "hyde", []string{"f1", "f2"}, "kb system prompt")

	if params.FileDates == nil {
		t.Fatal("params.FileDates is nil; contextParams did not thread h.fileDates")
	}
	if params.FileDates != lookup {
		t.Errorf("params.FileDates = %#v, want the exact stub instance %#v", params.FileDates, lookup)
	}
}

// Every pre-existing field on the literal must still round-trip through the
// extracted method — a refactor that drops or renames one is a regression
// even though it compiles (the struct has no other required fields).
func TestContextParamsPreservesExistingFields(t *testing.T) {
	h := &Handler{}

	params := h.contextParams("kb-1", "search query", "de", "hyde", []string{"f1", "f2"}, "kb system prompt")

	if params.KbID != "kb-1" {
		t.Errorf("KbID = %q, want %q", params.KbID, "kb-1")
	}
	if params.SearchQuery != "search query" {
		t.Errorf("SearchQuery = %q, want %q", params.SearchQuery, "search query")
	}
	if params.Language != "de" {
		t.Errorf("Language = %q, want %q", params.Language, "de")
	}
	if params.Enhance != "hyde" {
		t.Errorf("Enhance = %q, want %q", params.Enhance, "hyde")
	}
	if len(params.FileIDs) != 2 || params.FileIDs[0] != "f1" || params.FileIDs[1] != "f2" {
		t.Errorf("FileIDs = %v, want [f1 f2]", params.FileIDs)
	}
	if params.KbSystemPrompt != "kb system prompt" {
		t.Errorf("KbSystemPrompt = %q, want %q", params.KbSystemPrompt, "kb system prompt")
	}
}
