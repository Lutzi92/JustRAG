package chat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// parseSSEFrames splits a recorded SSE body into its decoded JSON frames.
func parseSSEFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(body, "\n\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		payload, ok := strings.CutPrefix(raw, "data: ")
		if !ok {
			t.Fatalf("frame is not an SSE data line: %q", raw)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("frame is not valid JSON (%v): %q", err, payload)
		}
		out = append(out, m)
	}
	return out
}

// The conflict entries reference sources by their [N] index, so the client
// must already hold the source list when the badge arrives. Both streaming
// paths go through writeOpeningFrames, so locking the order here locks it
// for the standard path and the orchestrator tail alike.
func TestWriteOpeningFrames_ConflictsFollowSources(t *testing.T) {
	w := httptest.NewRecorder()
	report := &ConflictReport{Conflicts: []MessageConflict{{
		Claim: "Beitragshöhe", SourceA: 1, SourceB: 2,
		Kind: "superseded", Newer: "b", FileA: "alt.md", FileB: "neu.md",
	}}}

	writeOpeningFrames(context.Background(), w,
		[]ChatSource{{Index: 1, FileID: "f1", FileName: "alt.md"}, {Index: 2, FileID: "f2", FileName: "neu.md"}},
		"", "chat-1", "msg-1", report)

	frames := parseSSEFrames(t, w.Body.String())
	if len(frames) != 2 {
		t.Fatalf("frames: got %d, want 2 (sources, conflicts):\n%s", len(frames), w.Body.String())
	}
	if _, ok := frames[0]["sources"]; !ok {
		t.Fatalf("frame 0 is not the sources frame: %v", frames[0])
	}
	if _, ok := frames[0]["conflicts"]; ok {
		t.Error("the sources frame must not also carry conflicts — they are separate frames")
	}
	list, ok := frames[1]["conflicts"].([]any)
	if !ok {
		t.Fatalf("frame 1 is not the conflicts frame: %v", frames[1])
	}
	// ONE wire shape: a BARE ARRAY of entries, never a nested object.
	if len(list) != 1 {
		t.Fatalf("conflicts: got %d entries, want 1", len(list))
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("conflict entry is not an object: %v", list[0])
	}
	for _, key := range []string{"claim", "sourceA", "sourceB", "kind", "newer", "fileA", "fileB"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("conflict entry is missing %q: %v", key, entry)
		}
	}
}

func TestWriteOpeningFrames_NoConflictsFrameWhenEmpty(t *testing.T) {
	for name, report := range map[string]*ConflictReport{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeOpeningFrames(context.Background(), w,
				[]ChatSource{{Index: 1, FileID: "f1", FileName: "a.md"}},
				"", "chat-1", "msg-1", report)

			frames := parseSSEFrames(t, w.Body.String())
			if len(frames) != 1 {
				t.Fatalf("frames: got %d, want only the sources frame:\n%s", len(frames), w.Body.String())
			}
			if _, ok := frames[0]["conflicts"]; ok {
				t.Error("the conflicts key must be absent, not an empty array")
			}
		})
	}
}

// The persisted message must carry the SAME bare array, so a reloaded chat
// reads message.conflicts[0].claim exactly like the live SSE frame does.
func TestMessageRow_ConflictsSerialiseAsBareArray(t *testing.T) {
	row := MessageRow{
		ID:        "m1",
		Conflicts: []MessageConflict{{Claim: "Beitragshöhe", SourceA: 1, SourceB: 2, Kind: "superseded", Newer: "b", FileA: "alt.md", FileB: "neu.md"}},
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Conflicts []map[string]any `json:"conflicts"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("conflicts is not a bare array on the message: %v\n%s", err, b)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0]["claim"] != "Beitragshöhe" {
		t.Fatalf("message.conflicts[0].claim is not readable: %s", b)
	}

	empty, err := json.Marshal(MessageRow{ID: "m2"})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(empty), `"conflicts"`) {
		t.Errorf("a message with no conflicts must omit the key: %s", empty)
	}
}
