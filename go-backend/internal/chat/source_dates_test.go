package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeFileDates is a FileDateLookup that returns canned rows (and optionally
// an error) without touching Postgres.
type fakeFileDates struct {
	rows   map[string]FileDates
	err    error
	calls  int
	gotIDs []string
}

func (f *fakeFileDates) FileDatesByIDs(_ context.Context, ids []string) (map[string]FileDates, error) {
	f.calls++
	f.gotIDs = append(f.gotIDs, ids...)
	return f.rows, f.err
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestEnrichSourceDates_FillsKnownIDsAndLeavesUnknownNil(t *testing.T) {
	created := mustTime(t, "2026-01-02T03:04:05Z")
	published := mustTime(t, "2025-12-24T00:00:00Z")
	lookup := &fakeFileDates{rows: map[string]FileDates{
		"file-a": {CreatedAt: created, PublishedAt: &published},
		"file-b": {CreatedAt: created},
	}}

	sources := []ChatSource{
		{Index: 1, FileID: "file-a"},
		{Index: 2, FileID: "file-b"},
		{Index: 3, FileID: "file-unknown"},
		{Index: 4, FileID: ""},
	}
	enrichSourceDates(context.Background(), lookup, sources)

	if sources[0].CreatedAt == nil || !sources[0].CreatedAt.Equal(created) {
		t.Errorf("file-a createdAt = %v, want %v", sources[0].CreatedAt, created)
	}
	if sources[0].PublishedAt == nil || !sources[0].PublishedAt.Equal(published) {
		t.Errorf("file-a publishedAt = %v, want %v", sources[0].PublishedAt, published)
	}
	if sources[1].CreatedAt == nil || !sources[1].CreatedAt.Equal(created) {
		t.Errorf("file-b createdAt = %v, want %v", sources[1].CreatedAt, created)
	}
	if sources[1].PublishedAt != nil {
		t.Errorf("file-b publishedAt = %v, want nil (NULL column)", sources[1].PublishedAt)
	}
	if sources[2].CreatedAt != nil || sources[2].PublishedAt != nil {
		t.Errorf("unknown id must stay nil, got %+v", sources[2])
	}
	if sources[3].CreatedAt != nil || sources[3].PublishedAt != nil {
		t.Errorf("empty file id must stay nil, got %+v", sources[3])
	}

	// The batch must be one query per turn (W3-R11), with the empty id and
	// the duplicate-free id set only.
	if lookup.calls != 1 {
		t.Errorf("lookup calls = %d, want 1", lookup.calls)
	}
	for _, id := range lookup.gotIDs {
		if id == "" {
			t.Error("empty file id was passed to the lookup")
		}
	}
}

func TestEnrichSourceDates_DeduplicatesFileIDs(t *testing.T) {
	lookup := &fakeFileDates{rows: map[string]FileDates{}}
	sources := []ChatSource{
		{FileID: "dup"}, {FileID: "dup"}, {FileID: "other"}, {FileID: "dup"},
	}
	enrichSourceDates(context.Background(), lookup, sources)
	if len(lookup.gotIDs) != 2 {
		t.Errorf("lookup got %v, want 2 distinct ids", lookup.gotIDs)
	}
}

// Mutation: delete the `if err != nil { return }` guard in enrichSourceDates.
// The fake below returns BOTH an error and a (bogus) partial map — exactly
// what a half-failed batch query looks like — so dropping the guard writes
// those wrong dates onto the sources and this test fails.
func TestEnrichSourceDates_LookupErrorLeavesSourcesUnchanged(t *testing.T) {
	bogus := mustTime(t, "1999-01-01T00:00:00Z")
	lookup := &fakeFileDates{
		rows: map[string]FileDates{"file-a": {CreatedAt: bogus, PublishedAt: &bogus}},
		err:  errors.New("connection reset"),
	}
	sources := []ChatSource{{Index: 1, FileID: "file-a", FileName: "a.pdf"}}

	enrichSourceDates(context.Background(), lookup, sources)

	if sources[0].CreatedAt != nil || sources[0].PublishedAt != nil {
		t.Errorf("a failed lookup must leave sources untouched, got %+v", sources[0])
	}
	if sources[0].FileName != "a.pdf" {
		t.Errorf("unrelated fields must not change: %+v", sources[0])
	}
}

// Mutation: remove the `l == nil` guard → nil-interface call panics.
func TestEnrichSourceDates_NilLookupIsANoop(t *testing.T) {
	sources := []ChatSource{{FileID: "file-a"}}
	enrichSourceDates(context.Background(), nil, sources)
	if sources[0].CreatedAt != nil {
		t.Errorf("nil lookup must not fill dates, got %+v", sources[0])
	}
}

func TestEnrichSourceDates_NoFileIDsSkipsTheQuery(t *testing.T) {
	lookup := &fakeFileDates{rows: map[string]FileDates{}}
	enrichSourceDates(context.Background(), lookup, []ChatSource{{FileID: ""}})
	if lookup.calls != 0 {
		t.Errorf("lookup called %d times for a source set with no file ids, want 0", lookup.calls)
	}
	enrichSourceDates(context.Background(), lookup, nil)
	if lookup.calls != 0 {
		t.Errorf("lookup called %d times for an empty source slice, want 0", lookup.calls)
	}
}

// The dates are enriched BEFORE AddMessage, so they live in the
// messages.sources JSONB and must survive the round trip back out of it —
// otherwise reopening an old chat would show freshness on new answers only.
// This also pins the exact wire names Task 6 (frontend) reads.
//
// Mutation: rename either json tag on ChatSource → this fails.
func TestChatSourceDates_SurviveTheSourcesJSONB(t *testing.T) {
	created := mustTime(t, "2026-01-02T03:04:05Z")
	published := mustTime(t, "2025-12-24T00:00:00Z")
	raw, err := json.Marshal([]ChatSource{
		{Index: 1, FileID: "f1", FileName: "a.pdf", CreatedAt: &created, PublishedAt: &published},
		{Index: 2, FileID: "f2", FileName: "b.pdf"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"createdAt"`) || !strings.Contains(string(raw), `"publishedAt"`) {
		t.Fatalf("wire names changed: %s", raw)
	}
	// A source without dates must omit both keys entirely rather than
	// serialising nulls — the frontend branches on presence.
	if strings.Count(string(raw), `"createdAt"`) != 1 {
		t.Errorf("unset dates must be omitted: %s", raw)
	}

	row, err := toMessageRow(messageDBRow{
		ID: "m1", ChatID: "c1", Role: "ai", Content: "hi",
		Sources: raw, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("toMessageRow: %v", err)
	}
	decoded := row.DecodedSources()
	if len(decoded) != 2 {
		t.Fatalf("decoded %d sources, want 2", len(decoded))
	}
	if decoded[0].CreatedAt == nil || !decoded[0].CreatedAt.Equal(created) {
		t.Errorf("createdAt did not survive: %v", decoded[0].CreatedAt)
	}
	if decoded[0].PublishedAt == nil || !decoded[0].PublishedAt.Equal(published) {
		t.Errorf("publishedAt did not survive: %v", decoded[0].PublishedAt)
	}
	if decoded[1].CreatedAt != nil || decoded[1].PublishedAt != nil {
		t.Errorf("dateless source came back with dates: %+v", decoded[1])
	}
}

func TestWithFileDates_SetsHandlerDependency(t *testing.T) {
	h := &Handler{}
	lookup := &fakeFileDates{}
	WithFileDates(lookup)(h)
	if h.fileDates != lookup {
		t.Error("WithFileDates did not attach the lookup")
	}
}
