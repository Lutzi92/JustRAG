package jobs

import (
	"encoding/json"
	"reflect"
	"testing"
)

// These tests pin the wire format of the Asynq job payloads. Both enqueuer
// (feature packages) and worker (internal/worker) marshal/unmarshal these
// structs by Go type, but the JSON field names are the actual contract — if
// a field were silently renamed the worker would receive a zero value and
// the failure would be hard to debug in production.

func TestFileProcessingPayload_RoundTrip(t *testing.T) {
	p := FileProcessingPayload{
		FileID:       "file-1",
		KbID:         "kb-1",
		FilePath:     "/tmp/foo.pdf",
		OriginalName: "foo.pdf",
		MimeType:     "application/pdf",
	}
	assertRoundTrip(t, p, &FileProcessingPayload{})
	assertWireKeys(t, p, []string{"fileId", "kbId", "filePath", "originalName", "mimetype"})
}

func TestTextProcessingPayload_RoundTrip(t *testing.T) {
	p := TextProcessingPayload{
		FileID:       "file-2",
		KbID:         "kb-2",
		Content:      "hello world",
		OriginalName: "note.txt",
		MimeType:     "text/plain",
	}
	assertRoundTrip(t, p, &TextProcessingPayload{})
	assertWireKeys(t, p, []string{"fileId", "kbId", "content", "originalName", "mimetype"})
}

func TestURLProcessingPayload_RoundTrip(t *testing.T) {
	p := URLProcessingPayload{
		FileID:       "file-3",
		KbID:         "kb-3",
		FilePath:     "fetched/path",
		OriginalName: "page.html",
		MimeType:     "text/html",
	}
	assertRoundTrip(t, p, &URLProcessingPayload{})
	assertWireKeys(t, p, []string{"fileId", "kbId", "filePath", "originalName", "mimetype"})
}

func TestRSSPollPayload_RoundTrip(t *testing.T) {
	p := RSSPollPayload{FeedID: "feed-1"}
	assertRoundTrip(t, p, &RSSPollPayload{})
	assertWireKeys(t, p, []string{"feedId"})
}

func TestConfluenceSyncPayload_RoundTrip(t *testing.T) {
	p := ConfluenceSyncPayload{SourceID: "src-1"}
	assertRoundTrip(t, p, &ConfluenceSyncPayload{})
	assertWireKeys(t, p, []string{"sourceId"})
}

func TestQueueAndTaskTypeConstants(t *testing.T) {
	// Pin the literal values: changing them breaks any Asynq queue or task
	// type already enqueued before a deploy. The test exists so a rename
	// shows up as a deliberate change in review.
	cases := []struct{ got, want string }{
		{QueueQuick, "rag-quick"},
		{QueueHeavy, "rag-heavy"},
		{QueueBatch, "rag-batch"},
		{TypeFileProcessing, "file-processing"},
		{TypeTextProcessing, "text-processing"},
		{TypeURLProcessing, "url-processing"},
		{TypeRSSPoll, "rss-poll"},
		{TypeReEmbedding, "re-embedding"},
		{TypeResearchExecution, "research-execution"},
		{TypeAcademicResearchExecution, "academic-research-execution"},
		{TypeContentGeneration, "content-generation"},
		{TypeConfluenceSync, "confluence-sync"},
		{TypeCrawl, "crawl"},
		{TypeEvalRun, "eval-run"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("constant changed: got %q, want %q", c.got, c.want)
		}
	}
}

// assertRoundTrip marshals payload, unmarshals into dst, and verifies the
// decoded value equals the original. Catches silent field-name drift.
func assertRoundTrip(t *testing.T, payload, dst any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	gotV := reflect.ValueOf(dst).Elem().Interface()
	if !reflect.DeepEqual(gotV, payload) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", gotV, payload)
	}
}

// assertWireKeys decodes the payload into a map and asserts the JSON keys
// match exactly. This catches a renamed json tag that DeepEqual on the typed
// struct would miss because the new field would just be empty.
func assertWireKeys(t *testing.T, payload any, want []string) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if len(m) != len(want) {
		t.Errorf("key count mismatch: got %v, want %v", keys(m), want)
		return
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing wire key %q in %v", k, keys(m))
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
