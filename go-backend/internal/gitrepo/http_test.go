package gitrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeStore struct {
	created        *CreateGitRepoSourceInput
	getByID        *GitRepoSourceRow
	updateCalled   bool
	lastUpdate     GitRepoSourceUpdate
	deleteCalled   bool
	gitRepoEnabled bool // controls GetSiteConfigValue("git_repo_enabled")
}

func (f *fakeStore) CreateGitRepoSource(_ context.Context, in CreateGitRepoSourceInput) (*GitRepoSourceRow, error) {
	f.created = &in
	tok := ""
	if in.AccessTokenEncrypted != nil {
		tok = *in.AccessTokenEncrypted
	}
	return &GitRepoSourceRow{ID: "s1", KbID: in.KbID, RepoURL: in.RepoURL, IsPrivate: in.IsPrivate, AccessTokenEncrypted: &tok}, nil
}
func (f *fakeStore) ListGitRepoSources(context.Context, string) ([]GitRepoSourceRow, error) {
	return nil, nil
}
func (f *fakeStore) GetGitRepoSourceByID(context.Context, string) (*GitRepoSourceRow, error) {
	return f.getByID, nil
}
func (f *fakeStore) UpdateGitRepoSource(_ context.Context, _ string, upd GitRepoSourceUpdate) error {
	f.updateCalled = true
	f.lastUpdate = upd
	return nil
}
func (f *fakeStore) DeleteGitRepoSource(_ context.Context, _ string) error {
	f.deleteCalled = true
	return nil
}
func (f *fakeStore) SetGitRepoSyncState(context.Context, string, SyncState) error { return nil }
func (f *fakeStore) ListGitRepoFiles(context.Context, string) ([]GitRepoFileRow, error) {
	return nil, nil
}
func (f *fakeStore) CreateGitRepoFile(context.Context, CreateGitRepoFileInput) (string, error) {
	return "f1", nil
}
func (f *fakeStore) DeleteGitRepoFileByID(context.Context, string) error { return nil }
func (f *fakeStore) GetGitRepoSourceFileProgress(context.Context, string) (int, int, error) {
	return 0, 0, nil
}
func (f *fakeStore) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if key == "git_repo_enabled" && f.gitRepoEnabled {
		v := "true"
		return &v, nil
	}
	return nil, nil
}

func TestCreateSourceRejectsNonHTTPS(t *testing.T) {
	h := NewHandler(&fakeStore{gitRepoEnabled: true}, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "ftp://x/y", "isPrivate": false})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateSourceEncryptsToken(t *testing.T) {
	fs := &fakeStore{gitRepoEnabled: true}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "https://github.com/x/y", "isPrivate": true, "accessToken": "secret-pat"})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body)
	}
	if fs.created == nil || fs.created.AccessTokenEncrypted == nil {
		t.Fatal("token not stored")
	}
	if *fs.created.AccessTokenEncrypted == "secret-pat" {
		t.Fatal("token stored in cleartext")
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("secret-pat")) {
		t.Fatal("cleartext token leaked in response")
	}
}

func TestCreateSourceRejectsPlainHTTP(t *testing.T) {
	h := NewHandler(&fakeStore{gitRepoEnabled: true}, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "http://x/y", "isPrivate": false})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateSourceRejectsEmptyHost(t *testing.T) {
	h := NewHandler(&fakeStore{gitRepoEnabled: true}, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "https://", "isPrivate": false})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateSourcePrivateRequiresToken(t *testing.T) {
	h := NewHandler(&fakeStore{gitRepoEnabled: true}, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "https://github.com/x/y", "isPrivate": true, "accessToken": ""})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUpdateSourceRejectsBadStatus(t *testing.T) {
	// getByID returns a source matching the KB so the ownership check passes,
	// but the status value "frozen" must be rejected before the store call.
	fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-A"}}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"status": "frozen"})
	req := httptest.NewRequest("PATCH", "/api/kb/KB-A/git-repos/SRC1", bytes.NewReader(body))
	req.SetPathValue("id", "KB-A")
	req.SetPathValue("sourceId", "SRC1")
	rec := httptest.NewRecorder()
	h.UpdateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUpdateSourceCrossKBReturns404(t *testing.T) {
	// Source belongs to KB-B but the request targets KB-A → must be 404, no update.
	fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-B"}}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"status": "active"})
	req := httptest.NewRequest("PATCH", "/api/kb/KB-A/git-repos/SRC1", bytes.NewReader(body))
	req.SetPathValue("id", "KB-A")
	req.SetPathValue("sourceId", "SRC1")
	rec := httptest.NewRecorder()
	h.UpdateSource(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if fs.updateCalled {
		t.Fatal("UpdateGitRepoSource must not be called on cross-KB source")
	}
}

func TestDeleteSourceCrossKBReturns404(t *testing.T) {
	// Source belongs to KB-B but the request targets KB-A → must be 404, no delete.
	fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-B"}}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	req := httptest.NewRequest("DELETE", "/api/kb/KB-A/git-repos/SRC1", nil)
	req.SetPathValue("id", "KB-A")
	req.SetPathValue("sourceId", "SRC1")
	rec := httptest.NewRecorder()
	h.DeleteSource(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if fs.deleteCalled {
		t.Fatal("DeleteGitRepoSource must not be called on cross-KB source")
	}
}

func TestTriggerSyncCrossKBReturns404(t *testing.T) {
	// Source belongs to KB-B but request targets KB-A → must be 404, no enqueue.
	fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-B"}, gitRepoEnabled: true}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil) // asynqClient nil
	req := httptest.NewRequest("POST", "/api/kb/KB-A/git-repos/SRC1/sync", nil)
	req.SetPathValue("id", "KB-A")
	req.SetPathValue("sourceId", "SRC1")
	rec := httptest.NewRecorder()
	h.TriggerSync(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCreateSourceDisabledReturns403(t *testing.T) {
	// git_repo_enabled is not set → CreateSource must return 403 and NOT create any source.
	fs := &fakeStore{} // gitRepoEnabled defaults to false
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "https://github.com/x/y", "isPrivate": false})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if fs.created != nil {
		t.Fatal("CreateGitRepoSource must not be called when feature is disabled")
	}
}

// ---------------------------------------------------------------------------
// Helpers for syncSchedule tests
// ---------------------------------------------------------------------------

// newTestHandler returns a Handler whose store reports git_repo_enabled=true,
// plus the fakeStore so tests can inspect captured mock state.
func newTestHandler(t *testing.T) (*Handler, *fakeStore) {
	t.Helper()
	fs := &fakeStore{gitRepoEnabled: true}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	return h, fs
}

// doRequest builds a request with the given method/path/body and invokes handler directly.
func doRequest(t *testing.T, handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Tests: syncSchedule
// ---------------------------------------------------------------------------

func TestCreateSource_RejectsUnknownSchedule(t *testing.T) {
	h, _ := newTestHandler(t) // must return a handler whose store reports git_repo_enabled=true
	body := `{"repoUrl":"https://github.com/o/r.git","syncSchedule":"nightly"}`
	rec := doRequest(t, h.CreateSource, http.MethodPost, "/api/kb/kb-1/git-repos", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateSource_DefaultsToManual(t *testing.T) {
	h, fs := newTestHandler(t)
	body := `{"repoUrl":"https://github.com/o/r.git"}`
	rec := doRequest(t, h.CreateSource, http.MethodPost, "/api/kb/kb-1/git-repos", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if fs.created == nil || fs.created.SyncSchedule != "manual" {
		t.Fatalf("expected manual, got %+v", fs.created)
	}
}

func TestUpdateSource_InvalidSyncSchedule(t *testing.T) {
	fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-A"}}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body := `{"syncSchedule":"hourly"}`
	req := httptest.NewRequest("PATCH", "/api/kb/KB-A/git-repos/SRC1", strings.NewReader(body))
	req.SetPathValue("id", "KB-A")
	req.SetPathValue("sourceId", "SRC1")
	rec := httptest.NewRecorder()
	h.UpdateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if fs.updateCalled {
		t.Fatal("UpdateGitRepoSource must not be called for an invalid syncSchedule")
	}
}

// TestUpdateSource_ClearsNextSyncAt verifies that changing syncSchedule
// clears next_sync_at so the sweeper re-stamps the slot on its next tick
// instead of leaving a stale slot from the previous schedule in place. This
// must hold for every target schedule, INCLUDING a change back to "manual" —
// that is the case most likely to get special-cased away by a future edit,
// since "manual" reads like "nothing to schedule" rather than "a schedule
// change that must clear the stamp".
func TestUpdateSource_ClearsNextSyncAt(t *testing.T) {
	for _, schedule := range []string{"daily", "weekly", "manual"} {
		t.Run(schedule, func(t *testing.T) {
			fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-A"}}
			h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
			body := `{"syncSchedule":"` + schedule + `"}`
			req := httptest.NewRequest("PATCH", "/api/kb/KB-A/git-repos/SRC1", strings.NewReader(body))
			req.SetPathValue("id", "KB-A")
			req.SetPathValue("sourceId", "SRC1")
			rec := httptest.NewRecorder()
			h.UpdateSource(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if fs.lastUpdate.NextSyncAt == nil {
				t.Fatal("expected NextSyncAt to be set (to clear it) when syncSchedule changes")
			}
			if *fs.lastUpdate.NextSyncAt != nil {
				t.Fatalf("expected NextSyncAt to be cleared to NULL, got %v", **fs.lastUpdate.NextSyncAt)
			}
		})
	}
}

// TestUpdateSource_NoScheduleChangeLeavesNextSyncAt verifies that a PATCH
// not touching syncSchedule and not re-activating does not touch
// next_sync_at. Pausing a source is exactly this case: it must not disturb
// the stamped slot, since the row falls out of ListDue/ListUnscheduled by
// status alone while paused.
func TestUpdateSource_NoScheduleChangeLeavesNextSyncAt(t *testing.T) {
	fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-A"}}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body := `{"status":"paused"}`
	req := httptest.NewRequest("PATCH", "/api/kb/KB-A/git-repos/SRC1", strings.NewReader(body))
	req.SetPathValue("id", "KB-A")
	req.SetPathValue("sourceId", "SRC1")
	rec := httptest.NewRecorder()
	h.UpdateSource(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fs.lastUpdate.NextSyncAt != nil {
		t.Fatalf("expected NextSyncAt to stay untouched, got %v", fs.lastUpdate.NextSyncAt)
	}
}

// TestUpdateSource_ReactivatingClearsNextSyncAt verifies that resuming a
// paused source (status -> "active", with no syncSchedule in the request
// body) clears next_sync_at. Without this, resuming a source that was
// paused for a week fires an immediate daytime sync on the next sweep,
// because the week-old next_sync_at is still in the past — exactly what the
// stamp-without-enqueue design in ListUnscheduled exists to prevent.
func TestUpdateSource_ReactivatingClearsNextSyncAt(t *testing.T) {
	fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-A", Status: "paused"}}
	h := NewHandler(fs, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body := `{"status":"active"}`
	req := httptest.NewRequest("PATCH", "/api/kb/KB-A/git-repos/SRC1", strings.NewReader(body))
	req.SetPathValue("id", "KB-A")
	req.SetPathValue("sourceId", "SRC1")
	rec := httptest.NewRecorder()
	h.UpdateSource(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fs.lastUpdate.NextSyncAt == nil {
		t.Fatal("expected NextSyncAt to be set (to clear it) when re-activating")
	}
	if *fs.lastUpdate.NextSyncAt != nil {
		t.Fatalf("expected NextSyncAt to be cleared to NULL, got %v", **fs.lastUpdate.NextSyncAt)
	}
}
