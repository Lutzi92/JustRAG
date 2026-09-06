package gitrepo

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/storage"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// recordingFileStore embeds the package's existing fakeStore and records every
// CreateGitRepoFile input, so a sync test can assert what published_at each
// file was created with.
type recordingFileStore struct {
	fakeStore
	created []CreateGitRepoFileInput
}

func (f *recordingFileStore) CreateGitRepoFile(_ context.Context, in CreateGitRepoFileInput) (string, error) {
	f.created = append(f.created, in)
	return "file-1", nil
}

type nopStorage struct{}

var _ storage.Storage = nopStorage{}

func (nopStorage) StoreFile(context.Context, string, []byte, string) error { return nil }
func (nopStorage) StoreFileFromReader(context.Context, string, io.Reader, string) error {
	return nil
}
func (nopStorage) ReadFile(context.Context, string) ([]byte, error)              { return nil, nil }
func (nopStorage) ReadFileStream(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (nopStorage) DeleteFile(context.Context, string) error                      { return nil }
func (nopStorage) DeleteFiles(context.Context, []string) error                   { return nil }
func (nopStorage) DeleteDirectory(context.Context, string) error                 { return nil }
func (nopStorage) FileExists(context.Context, string) (bool, error)              { return false, nil }
func (nopStorage) IsS3() bool                                                    { return false }

// unreachableAsynqClient points at a closed port: every Enqueue fails fast and
// the sync path only logs enqueue failures, so these tests need no Redis.
func unreachableAsynqClient(t *testing.T) *asynq.Client {
	t.Helper()
	c := asynq.NewClient(asynq.RedisClientOpt{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// ---------------------------------------------------------------------------
// The clone
// ---------------------------------------------------------------------------

// headCommitDate reads the committer date of the fixture repo's HEAD with the
// git CLI, so the assertion compares against git's own answer rather than
// against the value go-git happened to produce.
func headCommitDate(t *testing.T, dir string) time.Time {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%cI")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, string(trimNewline(out)))
	if err != nil {
		t.Fatalf("parse committer date %q: %v", out, err)
	}
	return parsed
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// The shallow clone carries exactly one date — the HEAD commit's committer
// time — and CloneAndCollect must surface it (W4-R11).
//
// Mutation: drop HeadCommitAt from the CloneResult literal → zero time, fails.
func TestCloneAndCollectReportsHeadCommitTime(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available for fixture creation")
	}
	dir := initFixtureRepo(t)
	res, err := CloneAndCollect(context.Background(), CloneOptions{URL: "file://" + dir})
	if err != nil {
		t.Fatalf("CloneAndCollect: %v", err)
	}
	if res.HeadCommitAt.IsZero() {
		t.Fatal("HeadCommitAt is zero; the HEAD commit's committer time must be reported")
	}
	if want := headCommitDate(t, dir); !res.HeadCommitAt.Equal(want) {
		t.Fatalf("HeadCommitAt = %v, want the HEAD committer date %v", res.HeadCommitAt, want)
	}
}

// ---------------------------------------------------------------------------
// The sync
// ---------------------------------------------------------------------------

// Every file of one sync gets the repository's content date. The clone is
// shallow, so a per-file date does not exist; what must not happen is files
// landing with published_at NULL and therefore reading as "ingested today".
//
// Mutation: drop `PublishedAt: publishedAt` from the CreateGitRepoFileInput
// literal in syncGitRepoSource → every created file has a nil date and this
// fails.
func TestSyncPropagatesHeadCommitDateToEveryFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available for fixture creation")
	}
	dir := initFixtureRepo(t)
	want := headCommitDate(t, dir)

	store := &recordingFileStore{}
	store.getByID = &GitRepoSourceRow{ID: "src-1", KbID: "kb-1", RepoURL: "file://" + dir, Status: "active"}

	err := syncGitRepoSource(context.Background(), SyncDeps{
		Store:       store,
		AsynqClient: unreachableAsynqClient(t),
		Storage:     nopStorage{},
	}, "src-1")
	if err != nil {
		t.Fatalf("syncGitRepoSource: %v", err)
	}

	if len(store.created) < 2 {
		t.Fatalf("expected the fixture's README.md and main.go to be created, got %d files", len(store.created))
	}
	for _, in := range store.created {
		if in.PublishedAt == nil {
			t.Fatalf("%s: published_at is nil, want the HEAD commit date", in.Name)
		}
		if !in.PublishedAt.Equal(want) {
			t.Fatalf("%s: published_at = %v, want %v", in.Name, in.PublishedAt, want)
		}
	}
}

// A repository whose HEAD commit is dated in the future must not become the
// KB's permanently-newest content.
//
// Mutation: drop the files.ClampPublishedAt call in syncGitRepoSource → the
// far-future commit date is stored verbatim and this fails.
func TestSyncClampsAFutureHeadCommitDate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available for fixture creation")
	}
	future := time.Now().Add(365 * 24 * time.Hour).Format(time.RFC3339)
	dir := initFixtureRepoAt(t, future)

	store := &recordingFileStore{}
	store.getByID = &GitRepoSourceRow{ID: "src-1", KbID: "kb-1", RepoURL: "file://" + dir, Status: "active"}

	before := time.Now()
	err := syncGitRepoSource(context.Background(), SyncDeps{
		Store:       store,
		AsynqClient: unreachableAsynqClient(t),
		Storage:     nopStorage{},
	}, "src-1")
	if err != nil {
		t.Fatalf("syncGitRepoSource: %v", err)
	}
	after := time.Now()

	if len(store.created) == 0 {
		t.Fatal("expected files to be created")
	}
	for _, in := range store.created {
		if in.PublishedAt == nil {
			t.Fatalf("%s: a future commit date must be clamped, not dropped", in.Name)
		}
		if in.PublishedAt.Before(before) || in.PublishedAt.After(after) {
			t.Fatalf("%s: published_at = %v, want a timestamp inside [%v, %v]",
				in.Name, in.PublishedAt, before, after)
		}
	}
}

// initFixtureRepoAt builds the same fixture repo as initFixtureRepo but with
// the commit's author AND committer dates forced to `when` (RFC 3339).
func initFixtureRepoAt(t *testing.T, when string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_AUTHOR_DATE="+when,
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_COMMITTER_DATE="+when)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(dir+"/README.md", []byte("# fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir
}

// Fix wave, item 7: the repository content date is computed once per sync,
// so handing the SAME *time.Time to every CreateGitRepoFileInput aliased one
// value into every row — a single in-place normalisation anywhere downstream
// would then rewrite the date of every file of the sync. Each input gets its
// own copy.
//
// Mutation: change `PublishedAt: copyTime(publishedAt)` back to
// `PublishedAt: publishedAt` in syncGitRepoSource → every file shares one
// pointer and both assertions below fail.
func TestSyncGivesEachFileItsOwnPublishedAtPointer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available for fixture creation")
	}
	dir := initFixtureRepo(t)
	want := headCommitDate(t, dir)

	store := &recordingFileStore{}
	store.getByID = &GitRepoSourceRow{ID: "src-1", KbID: "kb-1", RepoURL: "file://" + dir, Status: "active"}

	if err := syncGitRepoSource(context.Background(), SyncDeps{
		Store:       store,
		AsynqClient: unreachableAsynqClient(t),
		Storage:     nopStorage{},
	}, "src-1"); err != nil {
		t.Fatalf("syncGitRepoSource: %v", err)
	}
	if len(store.created) < 2 {
		t.Fatalf("expected at least 2 created files, got %d", len(store.created))
	}

	seen := map[*time.Time]string{}
	for _, in := range store.created {
		if in.PublishedAt == nil {
			t.Fatalf("%s: published_at is nil", in.Name)
		}
		if other, dup := seen[in.PublishedAt]; dup {
			t.Fatalf("%s and %s share one *time.Time (%p)", other, in.Name, in.PublishedAt)
		}
		seen[in.PublishedAt] = in.Name
	}

	// Writing through one file's pointer must not move any other file's date.
	*store.created[0].PublishedAt = want.Add(72 * time.Hour)
	for _, in := range store.created[1:] {
		if !in.PublishedAt.Equal(want) {
			t.Fatalf("%s: published_at moved to %v when another file's copy was written", in.Name, in.PublishedAt)
		}
	}
}
