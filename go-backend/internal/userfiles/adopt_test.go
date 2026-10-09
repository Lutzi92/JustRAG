package userfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/observability"
)

type fakeAdoptStore struct {
	mu        sync.Mutex
	lib       *fakeStore
	files     map[string]*LegacyFile
	users     map[string]bool
	beforeLnk func(fileID string) // simulates a concurrent adopter
	afterLnk  func()              // runs after a successful link
	linkErr   error
	dupUF     string // a link to this library file reports "no row" (unique index)
}

func newFakeAdoptStore(lib *fakeStore) *fakeAdoptStore {
	return &fakeAdoptStore{lib: lib, files: map[string]*LegacyFile{}, users: map[string]bool{}}
}

func (s *fakeAdoptStore) GetLegacy(_ context.Context, id, kb string) (*LegacyFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.files[id]
	if !ok || f.KBID != kb {
		return nil, nil
	}
	c := *f
	return &c, nil
}
func (s *fakeAdoptStore) UserExists(_ context.Context, id string) (bool, error) {
	return s.users[id], nil
}
func (s *fakeAdoptStore) FindBySHA(_ context.Context, owner, sha string) (*UserFile, error) {
	s.lib.mu.Lock()
	defer s.lib.mu.Unlock()
	for _, r := range s.lib.rows {
		if r.OwnerUserID == owner && r.SHA256 == sha {
			c := *r
			return &c, nil
		}
	}
	return nil, nil
}
func (s *fakeAdoptStore) LinkFile(ctx context.Context, id, ufID, path, up string) (bool, error) {
	if s.beforeLnk != nil {
		s.beforeLnk(id)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.linkErr != nil {
		return false, s.linkErr
	}
	f := s.files[id]
	if f.UserFileID != "" || isBusyStatus(f.Status) || (s.dupUF != "" && ufID == s.dupUF) {
		return false, nil
	}
	if s.afterLnk != nil {
		defer s.afterLnk()
	}
	f.UserFileID, f.StoragePath = ufID, path
	if f.UploadedBy == "" {
		f.UploadedBy = up
	}
	return true, nil
}
func (s *fakeAdoptStore) CountByStoragePath(ctx context.Context, p string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, f := range s.files {
		if f.StoragePath == p {
			n++
		}
	}
	return n, nil
}
func (s *fakeAdoptStore) DeleteUnreferenced(ctx context.Context, ufID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	for _, f := range s.files {
		if f.UserFileID == ufID {
			s.mu.Unlock()
			return false, nil
		}
	}
	s.mu.Unlock()
	s.lib.mu.Lock()
	defer s.lib.mu.Unlock()
	_, ok := s.lib.rows[ufID]
	delete(s.lib.rows, ufID)
	return ok, nil
}

type adoptEnv struct {
	as   *fakeAdoptStore
	lib  *fakeStore
	stor *fakeStorage
	a    *Adopter
}

func newAdoptEnv(quota int64) *adoptEnv {
	lib, stor := newFakeStore(), newFakeStorage()
	as := newFakeAdoptStore(lib)
	return &adoptEnv{as, lib, stor, NewAdopter(as, lib, stor, fakeQuota(quota))}
}

const kb1 = "kb-1"

func (e *adoptEnv) addLegacy(id, path, body string) *LegacyFile {
	f := &LegacyFile{ID: id, KBID: kb1, Name: id + ".txt", Type: "text/plain", Size: int64(len(body)),
		Origin: "upload", StoragePath: path}
	e.as.files[id] = f
	if body != "" {
		e.stor.blobs[path] = []byte(body)
	}
	return f
}

func shaOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func (e *adoptEnv) adopt(t *testing.T, caller string, ids ...string) *AdoptResult {
	t.Helper()
	res, err := e.a.Adopt(context.Background(), kb1, caller, ids)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	return res
}

func TestAdoptSkipReasons(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("up", "u/kb/up", "x")
	e.as.files["rss"] = &LegacyFile{ID: "rss", KBID: kb1, Origin: "rss", StoragePath: "p"}
	e.as.files["lib"] = &LegacyFile{ID: "lib", KBID: kb1, Origin: "upload", StoragePath: "q", UserFileID: "uf"}
	e.addLegacy("gone", "u/kb/gone", "")
	e.as.files["other"] = &LegacyFile{ID: "other", KBID: "kb-2", Origin: "upload", StoragePath: "z"}

	res := e.adopt(t, "caller", "rss", "lib", "gone", "other", "missing")
	want := map[string]string{"rss": "not_upload", "lib": "already_library", "gone": "blob_missing",
		"other": "not_found", "missing": "not_found"}
	if len(res.Adopted) != 0 || len(res.Skipped) != len(want) {
		t.Fatalf("res = %+v", res)
	}
	for _, s := range res.Skipped {
		if want[s.FileID] != s.Reason {
			t.Errorf("%s: reason %q, want %q", s.FileID, s.Reason, want[s.FileID])
		}
	}
}

func TestAdoptHappyPathMovesBlob(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("f1", "alice/kb/f1.txt", "hello")
	e.as.users["alice-id"] = true
	e.as.files["f1"].UploadedBy = "alice-id"

	res := e.adopt(t, "admin-id", "f1")
	if len(res.Adopted) != 1 || res.Adopted[0].FileID != "f1" {
		t.Fatalf("res = %+v", res)
	}
	uf := e.lib.rows[res.Adopted[0].UserFileID]
	if uf == nil || uf.OwnerUserID != "alice-id" || uf.SHA256 != shaOf("hello") || uf.Name != "f1.txt" || uf.Mime != "text/plain" {
		t.Fatalf("user file = %+v", uf)
	}
	if uf.StoragePath != "users/alice-id/"+uf.ID {
		t.Errorf("path = %q", uf.StoragePath)
	}
	if string(e.stor.blobs[uf.StoragePath]) != "hello" {
		t.Error("new blob missing")
	}
	if _, ok := e.stor.blobs["alice/kb/f1.txt"]; ok {
		t.Error("old blob should be deleted")
	}
	if got := e.as.files["f1"]; got.UserFileID != uf.ID || got.StoragePath != uf.StoragePath {
		t.Errorf("legacy row = %+v", got)
	}
}

func TestAdoptTargetUser(t *testing.T) {
	cases := []struct {
		name       string
		uploadedBy string
		exists     bool
		wantOwner  string
	}{
		{"uploader exists", "up", true, "up"},
		{"uploader deleted", "up", false, "caller"},
		{"no uploader", "", false, "caller"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAdoptEnv(0)
			e.addLegacy("f", "p/f", "data")
			e.as.files["f"].UploadedBy = c.uploadedBy
			e.as.users[c.uploadedBy] = c.exists
			res := e.adopt(t, "caller", "f")
			if len(res.Adopted) != 1 {
				t.Fatalf("res = %+v", res)
			}
			if got := e.lib.rows[res.Adopted[0].UserFileID].OwnerUserID; got != c.wantOwner {
				t.Errorf("owner = %q, want %q", got, c.wantOwner)
			}
		})
	}
}

func TestAdoptDedupLinksWithoutCopy(t *testing.T) {
	e := newAdoptEnv(0)
	e.lib.rows["ex"] = &UserFile{ID: "ex", OwnerUserID: "caller", SHA256: shaOf("same"), StoragePath: "users/caller/ex", KBs: []KBLink{}}
	e.addLegacy("f", "p/f", "same")
	before := e.stor.stored

	res := e.adopt(t, "caller", "f")
	if len(res.Adopted) != 1 || res.Adopted[0].UserFileID != "ex" {
		t.Fatalf("res = %+v", res)
	}
	if e.stor.stored != before {
		t.Error("blob was copied on dedup")
	}
	if e.as.files["f"].StoragePath != "users/caller/ex" {
		t.Errorf("path = %q", e.as.files["f"].StoragePath)
	}
	if _, ok := e.stor.blobs["p/f"]; ok {
		t.Error("old blob should be deleted after dedup link")
	}
}

func TestAdoptQuotaExceededWritesNothing(t *testing.T) {
	e := newAdoptEnv(3) // 3 bytes
	e.addLegacy("f", "p/f", "four")
	res := e.adopt(t, "caller", "f")
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != "quota_exceeded" {
		t.Fatalf("res = %+v", res)
	}
	if e.stor.stored != 0 || len(e.lib.rows) != 0 {
		t.Errorf("stored=%d rows=%d, want none", e.stor.stored, len(e.lib.rows))
	}
	if _, ok := e.stor.blobs["p/f"]; !ok {
		t.Error("legacy blob must stay")
	}
}

// Review Focus 1: the guarded UPDATE affects 0 rows (a concurrent adoption
// won) -> already_library, and neither the copy nor its library row leaks.
func TestAdoptLostRaceCleansUpCopy(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("f", "p/f", "race")
	e.as.beforeLnk = func(id string) {
		e.as.mu.Lock()
		e.as.files[id].UserFileID = "winner"
		e.as.mu.Unlock()
	}
	res := e.adopt(t, "caller", "f")
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != "already_library" {
		t.Fatalf("res = %+v", res)
	}
	for p := range e.stor.blobs {
		if strings.HasPrefix(p, "users/") {
			t.Errorf("leaked blob %s", p)
		}
	}
	if len(e.lib.rows) != 0 {
		t.Errorf("leaked library row: %+v", e.lib.rows)
	}
	if _, ok := e.stor.blobs["p/f"]; !ok {
		t.Error("old blob must survive a lost race")
	}
}

// Lost race on a dedup hit must not delete the pre-existing library row/blob.
func TestAdoptLostRaceKeepsExistingLibraryRow(t *testing.T) {
	e := newAdoptEnv(0)
	e.lib.rows["ex"] = &UserFile{ID: "ex", OwnerUserID: "caller", SHA256: shaOf("d"), StoragePath: "users/caller/ex", KBs: []KBLink{}}
	e.stor.blobs["users/caller/ex"] = []byte("d")
	e.addLegacy("f", "p/f", "d")
	e.as.linkErr = nil
	e.as.beforeLnk = func(id string) { e.as.mu.Lock(); e.as.files[id].UserFileID = "w"; e.as.mu.Unlock() }
	res := e.adopt(t, "caller", "f")
	if res.Skipped[0].Reason != "already_library" {
		t.Fatalf("res = %+v", res)
	}
	if e.lib.rows["ex"] == nil || e.stor.blobs["users/caller/ex"] == nil {
		t.Error("pre-existing library row/blob was removed")
	}
}

// Review Focus 2: an old blob still referenced by another files row stays.
func TestAdoptOldBlobSharedIsKept(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("f1", "shared/path", "dup")
	e.as.files["f2"] = &LegacyFile{ID: "f2", KBID: kb1, Origin: "upload", StoragePath: "shared/path"}
	res := e.adopt(t, "caller", "f1")
	if len(res.Adopted) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if string(e.stor.blobs["shared/path"]) != "dup" {
		t.Error("shared old blob was deleted")
	}
	// Adopting the second row afterwards removes it: now unshared.
	res = e.adopt(t, "caller", "f2")
	if len(res.Adopted) != 1 {
		t.Fatalf("res2 = %+v", res)
	}
	if _, ok := e.stor.blobs["shared/path"]; ok {
		t.Error("old blob should be gone once unshared")
	}
}

func TestAdoptMetricPerOutcome(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("ok", "p/ok", "m")
	e.as.files["rss"] = &LegacyFile{ID: "rss", KBID: kb1, Origin: "rss"}
	c := observability.UserFileAdoptTotalForTest()
	b1 := testutil.ToFloat64(c.WithLabelValues("adopted"))
	b2 := testutil.ToFloat64(c.WithLabelValues("not_upload"))
	b3 := testutil.ToFloat64(c.WithLabelValues("not_found"))
	e.adopt(t, "caller", "ok", "rss", "nope")
	for label, b := range map[string]float64{"adopted": b1, "not_upload": b2, "not_found": b3} {
		if got := testutil.ToFloat64(c.WithLabelValues(label)); got != b+1 {
			t.Errorf("%s: %v, want %v", label, got, b+1)
		}
	}
}

func TestAdoptEmptyStoragePathIsBlobMissingSkip(t *testing.T) {
	e := newAdoptEnv(0)
	e.as.files["nopath"] = &LegacyFile{ID: "nopath", KBID: kb1, Name: "a.txt", Type: "text/plain", Origin: "upload"}
	e.addLegacy("ok", "u/kb/ok", "hello")

	res := e.adopt(t, "caller", "nopath", "ok")
	if len(res.Skipped) != 1 || res.Skipped[0].FileID != "nopath" || res.Skipped[0].Reason != AdoptSkipBlobMissing {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	if len(res.Adopted) != 1 || res.Adopted[0].FileID != "ok" {
		t.Fatalf("adopted = %+v", res.Adopted)
	}
}

func TestAdoptBusyFilesAreSkipped(t *testing.T) {
	e := newAdoptEnv(0)
	for _, st := range []string{"pending", "processing"} {
		e.addLegacy(st, "u/kb/"+st, "data-"+st).Status = st
	}
	res := e.adopt(t, "caller", "pending", "processing")
	if len(res.Adopted) != 0 || len(res.Skipped) != 2 {
		t.Fatalf("res = %+v", res)
	}
	for _, s := range res.Skipped {
		if s.Reason != AdoptSkipBusy {
			t.Errorf("%s: %q", s.FileID, s.Reason)
		}
	}
	if len(e.lib.rows) != 0 || len(e.stor.blobs) != 2 || len(e.stor.deleted) != 0 {
		t.Errorf("must write and delete nothing: rows=%d blobs=%d deleted=%v", len(e.lib.rows), len(e.stor.blobs), e.stor.deleted)
	}
}

func TestAdoptStatusFlipBetweenCheckAndLinkRollsBack(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("f", "u/kb/f", "hello")
	e.as.beforeLnk = func(id string) {
		e.as.mu.Lock()
		e.as.files[id].Status = "processing"
		e.as.mu.Unlock()
	}
	res := e.adopt(t, "caller", "f")
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != AdoptSkipBusy {
		t.Fatalf("res = %+v", res)
	}
	if len(e.lib.rows) != 0 {
		t.Errorf("library row not rolled back: %v", e.lib.rows)
	}
	if len(e.stor.blobs) != 1 || string(e.stor.blobs["u/kb/f"]) != "hello" {
		t.Errorf("only the legacy blob may remain: %v", e.stor.blobs)
	}
}

func TestAdoptDuplicateInKBIsDistinctReason(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("a", "u/kb/a", "same")
	e.addLegacy("b", "u/kb/b", "same")
	// dupUF emulates the (kb_id, user_file_id) unique index.
	res := e.adopt(t, "caller", "a")
	if len(res.Adopted) != 1 {
		t.Fatalf("res = %+v", res)
	}
	e.as.dupUF = res.Adopted[0].UserFileID
	res = e.adopt(t, "caller", "b")
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != AdoptSkipDuplicateInKB {
		t.Fatalf("res = %+v", res)
	}
	if got := e.as.files["b"]; got.UserFileID != "" || e.stor.blobs["u/kb/b"] == nil {
		t.Errorf("b must stay legacy with its blob: %+v", got)
	}
}

func TestAdoptCancelledContextStillRollsBack(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("f", "u/kb/f", "hello")
	ctx, cancel := context.WithCancel(context.Background())
	e.as.beforeLnk = func(string) { cancel() } // client disconnects after Insert
	_, err := e.a.Adopt(ctx, kb1, "caller", []string{"f"})
	if err == nil {
		t.Fatal("expected link error")
	}
	if len(e.lib.rows) != 0 {
		t.Errorf("phantom user_files row: %v", e.lib.rows)
	}
	if len(e.stor.blobs) != 1 {
		t.Errorf("created blob not removed: %v", e.stor.blobs)
	}
}

func TestAdoptCancelledContextStillDeletesOldBlob(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("f", "u/kb/f", "hello")
	ctx, cancel := context.WithCancel(context.Background())
	e.as.afterLnk = cancel // disconnect right after the link commits
	res, err := e.a.Adopt(ctx, kb1, "caller", []string{"f"})
	if err != nil || len(res.Adopted) != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, ok := e.stor.blobs["u/kb/f"]; ok {
		t.Error("legacy blob leaked")
	}
}

func TestAdoptEmptyNameFallsBackToFile(t *testing.T) {
	e := newAdoptEnv(0)
	e.addLegacy("f", "u/kb/f", "hello").Name = ""
	res := e.adopt(t, "caller", "f")
	if len(res.Adopted) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if got := e.lib.rows[res.Adopted[0].UserFileID].Name; got != "file" {
		t.Errorf("name = %q", got)
	}
}
