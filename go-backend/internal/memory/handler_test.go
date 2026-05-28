package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/longmem"
)

type fakeStore struct {
	rows      []longmem.Memory
	deletedID int64
	bulkUser  string
}

func (f *fakeStore) ListByUser(_ context.Context, _ string) ([]longmem.Memory, error) {
	return f.rows, nil
}
func (f *fakeStore) DeleteByID(_ context.Context, _ string, id int64) error {
	f.deletedID = id
	return nil
}
func (f *fakeStore) DeleteByUser(_ context.Context, userID string) error {
	f.bulkUser = userID
	return nil
}

func withUser(r *http.Request, id string) *http.Request {
	return r.WithContext(auth.WithUser(r.Context(), &auth.Claims{ID: id}))
}

func TestList_RequiresAuth(t *testing.T) {
	h := NewHandler(&fakeStore{})
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/user/memory", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestList_ReturnsRows(t *testing.T) {
	h := NewHandler(&fakeStore{rows: []longmem.Memory{{ID: 1, Kind: "semantic", Content: "x"}}})
	rec := httptest.NewRecorder()
	h.List(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/user/memory", nil), "u1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []memoryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Content != "x" {
		t.Errorf("unexpected body: %+v", got)
	}
}

func TestDeleteOne_ScopedToUser(t *testing.T) {
	store := &fakeStore{}
	h := NewHandler(store)
	req := withUser(httptest.NewRequest(http.MethodDelete, "/api/user/memory/42", nil), "u1")
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()
	h.DeleteOne(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if store.deletedID != 42 {
		t.Errorf("deletedID = %d, want 42", store.deletedID)
	}
}

func TestDeleteAll(t *testing.T) {
	store := &fakeStore{}
	h := NewHandler(store)
	rec := httptest.NewRecorder()
	h.DeleteAll(rec, withUser(httptest.NewRequest(http.MethodDelete, "/api/user/memory", nil), "u1"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if store.bulkUser != "u1" {
		t.Errorf("bulkUser = %q, want u1", store.bulkUser)
	}
}

func TestExport_SetsAttachmentHeader(t *testing.T) {
	h := NewHandler(&fakeStore{rows: []longmem.Memory{{ID: 1, Content: "x"}}})
	rec := httptest.NewRecorder()
	h.Export(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/user/memory/export", nil), "u1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition header")
	}
}
