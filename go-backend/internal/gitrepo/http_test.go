package gitrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeStore struct {
	created      *CreateGitRepoSourceInput
	getByID      *GitRepoSourceRow
	updateCalled bool
	deleteCalled bool
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
func (f *fakeStore) UpdateGitRepoSource(_ context.Context, _ string, _ GitRepoSourceUpdate) error {
	f.updateCalled = true
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

func TestCreateSourceRejectsNonHTTPS(t *testing.T) {
	h := NewHandler(&fakeStore{}, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "ftp://x/y", "isPrivate": false})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateSourceEncryptsToken(t *testing.T) {
	fs := &fakeStore{}
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
	h := NewHandler(&fakeStore{}, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "http://x/y", "isPrivate": false})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateSourceRejectsEmptyHost(t *testing.T) {
	h := NewHandler(&fakeStore{}, "test-jwt-secret-at-least-32-bytes-long!!", nil)
	body, _ := json.Marshal(map[string]any{"repoUrl": "https://", "isPrivate": false})
	req := httptest.NewRequest("POST", "/api/kb/kb1/git-repos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSource(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateSourcePrivateRequiresToken(t *testing.T) {
	h := NewHandler(&fakeStore{}, "test-jwt-secret-at-least-32-bytes-long!!", nil)
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
	fs := &fakeStore{getByID: &GitRepoSourceRow{ID: "SRC1", KbID: "KB-B"}}
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
