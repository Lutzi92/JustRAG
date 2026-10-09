package files_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/kbaccess"
)

type fakeFileDeleter struct {
	got [][]string
	err error
}

func (f *fakeFileDeleter) DeleteFiles(_ context.Context, ids []string) error {
	f.got = append(f.got, ids)
	return f.err
}

func deleteWithDeleter(t *testing.T, fd *fakeFileDeleter) *httptest.ResponseRecorder {
	t.Helper()
	ownerID := "user-1"
	fileInfo := &files.FileInfo{ID: "file-abc", KbID: "kb-1", Name: "r.pdf", StoragePath: strPtr("user-1/kb-1/r.pdf")}
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID}
	store := &mockStore{file: fileInfo, kb: kb, role: kbaccess.RoleOwner}
	h := files.NewHandler(store, &mockStorage{}, noopChunks())
	h.SetFileDeleter(fd)

	req := newRequest(http.MethodDelete, "/api/files/file-abc")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, ownerUser())
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	return rr
}

func TestDelete_DelegatesToFileDeleter(t *testing.T) {
	fd := &fakeFileDeleter{}
	rr := deleteWithDeleter(t, fd)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if len(fd.got) != 1 || len(fd.got[0]) != 1 || fd.got[0][0] != "file-abc" {
		t.Fatalf("DeleteFiles calls = %v, want [[file-abc]]", fd.got)
	}
}

func TestDelete_FileDeleterErrorIs500(t *testing.T) {
	fd := &fakeFileDeleter{err: errors.New("boom")}
	rr := deleteWithDeleter(t, fd)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}
