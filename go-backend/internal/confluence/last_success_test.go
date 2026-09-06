package confluence

import (
	"context"
	"encoding/json"
	"testing"
)

// recordingSourceStore embeds the existing fake and overrides just what the
// post-file progress wrapper reads, recording the update it writes.
type recordingSourceStore struct {
	fakeConfluenceStore
	source  *ConfluenceSourceRow
	total   int
	done    int
	updates []ConfluenceSourceUpdate
}

func (s *recordingSourceStore) GetConfluenceSourceIDForFile(context.Context, string) (string, error) {
	return "src-1", nil
}
func (s *recordingSourceStore) GetConfluenceSourceByID(context.Context, string) (*ConfluenceSourceRow, error) {
	return s.source, nil
}
func (s *recordingSourceStore) GetConfluenceSourceFileProgress(context.Context, string) (int, int, error) {
	return s.total, s.done, nil
}
func (s *recordingSourceStore) UpdateConfluenceSource(_ context.Context, _ string, u ConfluenceSourceUpdate) (*ConfluenceSourceRow, error) {
	s.updates = append(s.updates, u)
	return s.source, nil
}

func progressPayload(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{"fileId": "file-1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

// A completed sync stamps last_success_at.
//
// Mutation: drop LastSuccessAt from the "all files processed" branch → this
// fails, and the admin overview would fall back to the last ATTEMPT
// timestamp for a source that actually succeeded.
func TestUpdateSourceProgressAfterFile_StampsLastSuccessOnCompletion(t *testing.T) {
	store := &recordingSourceStore{
		source: &ConfluenceSourceRow{ID: "src-1", Status: "syncing", PageCount: 3},
		total:  3, done: 3,
	}
	UpdateSourceProgressAfterFile(context.Background(), store, progressPayload(t))

	if len(store.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(store.updates))
	}
	u := store.updates[0]
	if u.Status == nil || *u.Status != "active" {
		t.Fatalf("expected the completion branch (status=active), got %+v", u.Status)
	}
	if u.LastSuccessAt == nil {
		t.Error("a completed sync must stamp last_success_at")
	}
	if u.LastSyncedAt == nil {
		t.Error("last_synced_at must still be written alongside it")
	}
}

// A sync still in flight writes progress only — no success stamp.
//
// Mutation: move LastSuccessAt into the progress-only branch → this fails.
// Stamping a success before the sync finishes is exactly the failure mode
// the separate column exists to prevent.
func TestUpdateSourceProgressAfterFile_NoSuccessStampWhileStillRunning(t *testing.T) {
	store := &recordingSourceStore{
		source: &ConfluenceSourceRow{ID: "src-1", Status: "syncing", PageCount: 5},
		total:  5, done: 2,
	}
	UpdateSourceProgressAfterFile(context.Background(), store, progressPayload(t))

	if len(store.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(store.updates))
	}
	if store.updates[0].LastSuccessAt != nil {
		t.Error("an in-flight sync must not stamp last_success_at")
	}
}
