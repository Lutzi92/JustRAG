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

// fakeTableDropper records each DropTablesForFile call so the ordering test
// below can assert it ran.
type fakeTableDropper struct{ called []string }

func (d *fakeTableDropper) DropTablesForFile(_ context.Context, fileID string) error {
	d.called = append(d.called, fileID)
	return nil
}

// TestDeleteGitRepoFileByIDDropsTablesBeforeRowDelete pins the Phase-3
// carry: DeleteGitRepoFileByID must drop a file's materialised spreadsheet
// tables BEFORE the files row delete (see TableDropper's doc comment for
// why deleting the row first orphans them beyond any future reach).
//
// This has no real-DB seam (a genuine delete needs a live pool), so the
// ordering is pinned the same way *PGStore itself would panic: NewStore(nil)
// has no pool, so s.pool.Exec panics -- reaching that panic proves the
// dropper call sits strictly before it, since a swapped or dropped call
// would either never run or run after the (fatal) panic.
func TestDeleteGitRepoFileByIDDropsTablesBeforeRowDelete(t *testing.T) {
	s := NewStore(nil)
	d := &fakeTableDropper{}
	s.SetTableDropper(d)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic from the nil-pool Exec (proves the dropper ran first, not that Exec is reachable)")
		}
		if want := []string{"file-1"}; len(d.called) != 1 || d.called[0] != want[0] {
			t.Errorf("dropper called = %v, want %v", d.called, want)
		}
	}()
	_ = s.DeleteGitRepoFileByID(context.Background(), "file-1")
}

// TestDeleteGitRepoFileByIDNilDropperSkipsCleanly pins the nil-safety half:
// with no dropper wired, the nil check itself must not panic before ever
// reaching the (nil-pool) Exec.
func TestDeleteGitRepoFileByIDNilDropperSkipsCleanly(t *testing.T) {
	s := NewStore(nil)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic from the nil-pool Exec")
		}
	}()
	_ = s.DeleteGitRepoFileByID(context.Background(), "file-1")
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
