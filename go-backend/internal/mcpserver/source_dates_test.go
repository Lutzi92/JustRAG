package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

// ask_kb's structured output carries the cited files' dates so the calling
// model can tell a current advisory from a superseded one (W4-R12). RFC 3339
// in UTC, matching the OpenAI-compat surface exactly.
//
// Mutation: drop the CreatedAt/PublishedAt fields from the mapSources literal
// → both keys are absent and this fails.
func TestMapSourcesCarriesDatesAsRFC3339UTC(t *testing.T) {
	berlin := time.FixedZone("CEST", 2*3600)
	created := time.Date(2026, 9, 1, 14, 30, 0, 0, berlin)
	published := time.Date(2026, 8, 20, 8, 0, 0, 0, berlin)

	got := mapSources([]chat.ChatSource{{
		Index: 1, FileID: "f1", FileName: "advisory.md", Score: 0.9,
		CreatedAt: &created, PublishedAt: &published,
	}})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if want := "2026-09-01T12:30:00Z"; got[0].CreatedAt != want {
		t.Errorf("createdAt = %q, want %q", got[0].CreatedAt, want)
	}
	if want := "2026-08-20T06:00:00Z"; got[0].PublishedAt != want {
		t.Errorf("publishedAt = %q, want %q", got[0].PublishedAt, want)
	}

	blob, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, frag := range []string{`"createdAt":"2026-09-01T12:30:00Z"`, `"publishedAt":"2026-08-20T06:00:00Z"`} {
		if !strings.Contains(string(blob), frag) {
			t.Errorf("source JSON %s missing %s", blob, frag)
		}
	}
}

// The option plumbing: without it the answerer would project sources whose
// date fields were never filled, and every ask_kb result would silently lose
// its dates while the projection tests above still passed.
//
// Mutation: make WithFileDates a no-op → this fails.
func TestWithFileDatesIsApplied(t *testing.T) {
	var lookup chat.FileDateLookup = stubDateLookup{}
	a := NewPipelineAnswerer(nil, nil, nil, nil, WithFileDates(lookup))
	p, ok := a.(*pipelineAnswerer)
	if !ok {
		t.Fatalf("NewPipelineAnswerer returned %T", a)
	}
	if p.fileDates == nil {
		t.Fatal("fileDates is nil; WithFileDates did not reach the answerer")
	}
}

type stubDateLookup struct{}

func (stubDateLookup) FileDatesByIDs(_ context.Context, _ []string) (map[string]chat.FileDates, error) {
	return nil, nil
}

// An absent date must produce an absent key, never an empty string or a zero
// timestamp — a model reading "0001-01-01" would treat the document as
// two millennia old.
//
// Mutation: drop `omitempty` from either field → this fails.
func TestMapSourcesOmitsAbsentDates(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	got := mapSources([]chat.ChatSource{
		{Index: 1, FileID: "f1", FileName: "upload.pdf", CreatedAt: &created},
		{Index: 2, FileID: "f2", FileName: "no-dates.md"},
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	withCreated, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(withCreated), "publishedAt") {
		t.Errorf("publishedAt must be omitted when unset, got %s", withCreated)
	}
	if !strings.Contains(string(withCreated), `"createdAt":"2026-09-01T12:00:00Z"`) {
		t.Errorf("createdAt missing from %s", withCreated)
	}

	neither, err := json.Marshal(got[1])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(neither), "createdAt") || strings.Contains(string(neither), "publishedAt") {
		t.Errorf("both date keys must be absent when unset, got %s", neither)
	}
}

// contextParams is the extracted builder behind the inline
// chat.ChatContextParams{} literal in Answer. Unlike publicapi/openaicompat,
// this surface passes a real p.cfg site-config reader to PrepareChatContext,
// so FileDates here is what lets the conflict / supersession detector render
// real date lines instead of "unknown" on ask_kb turns (W6-R2).
//
// Mutation: drop the `FileDates: p.fileDates` line from contextParams →
// this fails.
func TestContextParamsCarriesFileDates(t *testing.T) {
	lookup := stubDateLookup{}
	p := &pipelineAnswerer{fileDates: lookup}

	params := p.contextParams("kb-1", "the question", "de", "kb system prompt")

	if params.FileDates == nil {
		t.Fatal("params.FileDates is nil; contextParams did not thread p.fileDates")
	}
	if params.FileDates != lookup {
		t.Errorf("params.FileDates = %#v, want the exact stub instance %#v", params.FileDates, lookup)
	}
}

// Every pre-existing field on the literal — including the hardcoded
// QueryTypeComplexReasoning — must still round-trip through the extracted
// method.
func TestContextParamsPreservesExistingFields(t *testing.T) {
	p := &pipelineAnswerer{}

	params := p.contextParams("kb-1", "the question", "de", "kb system prompt")

	if params.KbID != "kb-1" {
		t.Errorf("KbID = %q, want %q", params.KbID, "kb-1")
	}
	if params.SearchQuery != "the question" {
		t.Errorf("SearchQuery = %q, want %q", params.SearchQuery, "the question")
	}
	if params.Language != "de" {
		t.Errorf("Language = %q, want %q", params.Language, "de")
	}
	if params.KbSystemPrompt != "kb system prompt" {
		t.Errorf("KbSystemPrompt = %q, want %q", params.KbSystemPrompt, "kb system prompt")
	}
	if params.QueryType != vector.QueryTypeComplexReasoning {
		t.Errorf("QueryType = %q, want %q", params.QueryType, vector.QueryTypeComplexReasoning)
	}
}
