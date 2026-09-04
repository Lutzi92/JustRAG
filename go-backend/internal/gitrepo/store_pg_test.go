package gitrepo

import (
	"context"
	"testing"
	"time"
)

// Compile-time guard: *PGStore must satisfy Store.
var _ Store = NewStore(nil)

func TestNewStoreNonNil(t *testing.T) {
	s := NewStore(nil)
	if s == nil {
		t.Fatal("NewStore(nil) returned nil")
	}
}

// fakeTableDropper records each DropTablesForFile call, optionally into a
// shared event-order log (nil = untracked) so http_test.go's DeleteSource
// tests can additionally assert ordering against another mock's own calls.
type fakeTableDropper struct {
	called []string
	events *[]string
}

func (d *fakeTableDropper) DropTablesForFile(_ context.Context, fileID string) error {
	if d.events != nil {
		*d.events = append(*d.events, "drop:"+fileID)
	}
	d.called = append(d.called, fileID)
	return nil
}

// TestPGStoreDropTablesForNilDropperIsNoop pins the nil-safety half of the
// Phase-3 carry directly against dropTablesFor (rather than indirectly via
// DeleteGitRepoFileByID and a nil-pool panic, which a prior version of this
// test relied on -- that made a removed nil check and a removed dropper
// call both look identical from the outside, since either way the next
// line's nil-pool Exec panics regardless). With no dropper wired,
// dropTablesFor must return immediately without ever touching the pool, so
// this must not panic even though s.pool is nil.
func TestPGStoreDropTablesForNilDropperIsNoop(t *testing.T) {
	s := NewStore(nil)
	s.dropTablesFor(context.Background(), []string{"file-1"})
}

// TestPGStoreDropTablesForCallsOncePerID pins the positive half: with a
// dropper wired, every id passed in gets exactly one DropTablesForFile
// call, in order.
func TestPGStoreDropTablesForCallsOncePerID(t *testing.T) {
	s := NewStore(nil)
	d := &fakeTableDropper{}
	s.SetTableDropper(d)

	s.dropTablesFor(context.Background(), []string{"file-1", "file-2"})

	want := []string{"file-1", "file-2"}
	if len(d.called) != len(want) {
		t.Fatalf("called = %v, want %v", d.called, want)
	}
	for i, id := range want {
		if d.called[i] != id {
			t.Errorf("called[%d] = %q, want %q", i, d.called[i], id)
		}
	}
}

func TestToGitRepoSourceRow(t *testing.T) {
	tok := "enc-tok"
	branch := "main"
	errMsg := "some error"
	sha := "abc123def456"
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	created := time.Date(2025, 6, 1, 8, 0, 0, 0, time.UTC)

	db := gitRepoSourceDBRow{
		ID:                   "id-1",
		KbID:                 "kb-2",
		RepoURL:              "https://github.com/org/repo.git",
		IsPrivate:            true,
		AccessTokenEncrypted: &tok,
		Branch:               &branch,
		Status:               "active",
		ErrorMessage:         &errMsg,
		ConsecutiveFailures:  3,
		LastSyncedAt:         &ts,
		LastCommitSHA:        &sha,
		FileCount:            42,
		SyncProgress:         10,
		SyncTotal:            20,
		CreatedAt:            created,
	}

	got := toGitRepoSourceRow(db)

	if got.ID != db.ID {
		t.Errorf("ID: got %q want %q", got.ID, db.ID)
	}
	if got.KbID != db.KbID {
		t.Errorf("KbID: got %q want %q", got.KbID, db.KbID)
	}
	if got.RepoURL != db.RepoURL {
		t.Errorf("RepoURL: got %q want %q", got.RepoURL, db.RepoURL)
	}
	if got.IsPrivate != db.IsPrivate {
		t.Errorf("IsPrivate: got %v want %v", got.IsPrivate, db.IsPrivate)
	}
	if got.AccessTokenEncrypted == nil || *got.AccessTokenEncrypted != tok {
		t.Errorf("AccessTokenEncrypted: got %v want %q", got.AccessTokenEncrypted, tok)
	}
	if got.Branch == nil || *got.Branch != branch {
		t.Errorf("Branch: got %v want %q", got.Branch, branch)
	}
	if got.Status != db.Status {
		t.Errorf("Status: got %q want %q", got.Status, db.Status)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != errMsg {
		t.Errorf("ErrorMessage: got %v want %q", got.ErrorMessage, errMsg)
	}
	if got.ConsecutiveFailures != db.ConsecutiveFailures {
		t.Errorf("ConsecutiveFailures: got %d want %d", got.ConsecutiveFailures, db.ConsecutiveFailures)
	}
	if got.LastSyncedAt == nil || !got.LastSyncedAt.Equal(ts) {
		t.Errorf("LastSyncedAt: got %v want %v", got.LastSyncedAt, ts)
	}
	if got.LastCommitSHA == nil || *got.LastCommitSHA != sha {
		t.Errorf("LastCommitSHA: got %v want %q", got.LastCommitSHA, sha)
	}
	if got.FileCount != db.FileCount {
		t.Errorf("FileCount: got %d want %d", got.FileCount, db.FileCount)
	}
	if got.SyncProgress != db.SyncProgress {
		t.Errorf("SyncProgress: got %d want %d", got.SyncProgress, db.SyncProgress)
	}
	if got.SyncTotal != db.SyncTotal {
		t.Errorf("SyncTotal: got %d want %d", got.SyncTotal, db.SyncTotal)
	}
	if !got.CreatedAt.Equal(db.CreatedAt) {
		t.Errorf("CreatedAt: got %v want %v", got.CreatedAt, db.CreatedAt)
	}
}

func TestToGitRepoSourceRowNilPointers(t *testing.T) {
	// Ensure nil pointer fields are preserved (not accidentally dereferenced).
	db := gitRepoSourceDBRow{
		ID:      "id-2",
		KbID:    "kb-3",
		RepoURL: "https://github.com/org/pub.git",
		Status:  "active",
		// All pointer fields left nil.
	}

	got := toGitRepoSourceRow(db)

	if got.AccessTokenEncrypted != nil {
		t.Errorf("AccessTokenEncrypted: expected nil, got %v", got.AccessTokenEncrypted)
	}
	if got.Branch != nil {
		t.Errorf("Branch: expected nil, got %v", got.Branch)
	}
	if got.ErrorMessage != nil {
		t.Errorf("ErrorMessage: expected nil, got %v", got.ErrorMessage)
	}
	if got.LastSyncedAt != nil {
		t.Errorf("LastSyncedAt: expected nil, got %v", got.LastSyncedAt)
	}
	if got.LastCommitSHA != nil {
		t.Errorf("LastCommitSHA: expected nil, got %v", got.LastCommitSHA)
	}
}

func TestGitRepoStoreImplementsSweeperContract(t *testing.T) {
	if k := NewStore(nil).Kind(); k != "git_repo" {
		t.Fatalf("expected kind git_repo, got %q", k)
	}
}

func TestGitRepoSourceRowCarriesSchedule(t *testing.T) {
	next := time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC)
	got := toGitRepoSourceRow(gitRepoSourceDBRow{ID: "g1", SyncSchedule: "daily", NextSyncAt: &next})
	if got.SyncSchedule != "daily" || got.NextSyncAt == nil {
		t.Fatalf("schedule fields not carried through: %+v", got)
	}
}
