package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/chat"
)

// A source carrying both dates projects them as RFC 3339 in UTC. The input is
// deliberately in a non-UTC zone: two surfaces rendering the same instant
// differently is a client bug nobody reports twice.
//
// Mutation: drop the CreatedAt/PublishedAt fields from the buildCitations
// literal → both keys are absent and this fails.
func TestBuildCitationsCarriesSourceDatesAsRFC3339UTC(t *testing.T) {
	berlin := time.FixedZone("CEST", 2*3600)
	created := time.Date(2026, 9, 1, 14, 30, 0, 0, berlin)
	published := time.Date(2026, 8, 20, 8, 0, 0, 0, berlin)

	got := buildCitations([]chat.ChatSource{{
		Index: 1, FileID: "f1", FileName: "advisory.md", Content: "x", Score: 0.5,
		CreatedAt: &created, PublishedAt: &published,
	}})
	if len(got) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(got))
	}
	if want := "2026-09-01T12:30:00Z"; got[0].CreatedAt != want {
		t.Errorf("created_at = %q, want %q", got[0].CreatedAt, want)
	}
	if want := "2026-08-20T06:00:00Z"; got[0].PublishedAt != want {
		t.Errorf("published_at = %q, want %q", got[0].PublishedAt, want)
	}

	// The wire shape is what clients parse, so assert on the JSON too.
	blob, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, frag := range []string{`"created_at":"2026-09-01T12:30:00Z"`, `"published_at":"2026-08-20T06:00:00Z"`} {
		if !strings.Contains(string(blob), frag) {
			t.Errorf("citation JSON %s missing %s", blob, frag)
		}
	}
}

// A file with an ingest date but no publication date (the common case: an
// uploaded PDF) emits created_at and omits published_at entirely — the key
// must be absent, not an empty string or a zero timestamp, or a client would
// render "1 Jan 0001" as the document's date.
//
// Mutation: drop `omitempty` from PublishedAt → the key appears empty and
// this fails.
func TestBuildCitationsOmitsAbsentDates(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	got := buildCitations([]chat.ChatSource{
		{Index: 1, FileID: "f1", FileName: "upload.pdf", CreatedAt: &created},
		{Index: 2, FileID: "f2", FileName: "no-dates.md"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 citations, got %d", len(got))
	}

	withCreated, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(withCreated), "published_at") {
		t.Errorf("published_at must be omitted when unset, got %s", withCreated)
	}
	if !strings.Contains(string(withCreated), `"created_at":"2026-09-01T12:00:00Z"`) {
		t.Errorf("created_at missing from %s", withCreated)
	}

	neither, err := json.Marshal(got[1])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(neither), "created_at") || strings.Contains(string(neither), "published_at") {
		t.Errorf("both date keys must be absent when unset, got %s", neither)
	}
}

// The OpenAI file_citation annotation shape has no date slot, and inventing
// one would break clients that parse annotations against the spec (W4-R12).
//
// Mutation: add a date field to fileCitation → this fails.
func TestAnnotationsCarryNoDates(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	anns := buildAnnotations("Antwort [1].", []chat.ChatSource{
		{Index: 1, FileID: "f1", FileName: "advisory.md", CreatedAt: &created},
	})
	if len(anns) != 1 {
		t.Fatalf("expected 1 annotation, got %d", len(anns))
	}
	blob, err := json.Marshal(anns[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "2026-09-01") || strings.Contains(string(blob), "_at\"") {
		t.Errorf("annotation must carry no dates, got %s", blob)
	}
}
