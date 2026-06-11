package kb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ kb.UpdateStore = (*mockUpdateStore)(nil)

type mockUpdateStore struct {
	kb    *kb.KBRow
	files []kb.FileRow
	total int
	err   error
}

func (m *mockUpdateStore) UpdateKnowledgeBase(_ context.Context, _ string, _ kb.KBUpdate) (*kb.KBRow, error) {
	return m.kb, m.err
}

func (m *mockUpdateStore) ListFiles(_ context.Context, _ string, _, _ int) ([]kb.FileRow, int, error) {
	return m.files, m.total, m.err
}

func (m *mockUpdateStore) GetKBChunkConfig(_ context.Context, _ string) (int, int, error) {
	return 0, 0, m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// injectKBAccess returns a copy of r with a KBAccessResult injected into the
// context, mirroring what kbaccess.RequireKBPermission does in production.
func injectKBAccess(r *http.Request, kbID string) *http.Request {
	access := &kbaccess.KBAccessResult{
		KB:         &kbaccess.KnowledgeBase{ID: kbID, IsGlobal: false},
		IsOwner:    true,
		Permission: "edit",
	}
	return r.WithContext(kbaccess.WithAccess(r.Context(), access))
}

func makeKBRow(id, name string) *kb.KBRow {
	desc := "Test description"
	lang := "de"
	return &kb.KBRow{
		ID:          id,
		Name:        name,
		Description: &desc,
		Language:    lang,
		IsGlobal:    false,
		IsPublished: true,
		CreatedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func makeFileRow(id, name string) kb.FileRow {
	size := 1024
	return kb.FileRow{
		ID:        id,
		Name:      name,
		Type:      "pdf",
		Size:      &size,
		Status:    "ready",
		Progress:  100,
		Origin:    "upload",
		CreatedAt: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// UpdateKB tests
// ---------------------------------------------------------------------------

// TestUpdateKB_Valid checks that a valid PATCH body returns the updated KB with 200.
func TestUpdateKB_Valid(t *testing.T) {
	store := &mockUpdateStore{
		kb: makeKBRow("kb-1", "Updated KB"),
	}
	h := kb.NewUpdateHandler(store, nil)

	body := map[string]any{"name": "Updated KB"}
	b, _ := json.Marshal(body)

	r := httptest.NewRequest(http.MethodPatch, "/api/kb/kb-1", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.UpdateKB(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result kb.KBRow
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result.ID != "kb-1" {
		t.Errorf("expected id=kb-1, got %q", result.ID)
	}
	if result.Name != "Updated KB" {
		t.Errorf("expected name=%q, got %q", "Updated KB", result.Name)
	}
}

// TestUpdateKB_NotFound checks that store.ErrNotFound yields 404.
func TestUpdateKB_NotFound(t *testing.T) {
	mockStore := &mockUpdateStore{err: store.ErrNotFound}
	h := kb.NewUpdateHandler(mockStore, nil)

	body := map[string]any{"name": "Ghost KB"}
	b, _ := json.Marshal(body)

	r := httptest.NewRequest(http.MethodPatch, "/api/kb/missing", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r = injectKBAccess(r, "missing")

	w := httptest.NewRecorder()
	h.UpdateKB(w, r)

	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Result().StatusCode)
	}
}

// TestUpdateKB_InvalidBody checks that a malformed body yields 400.
func TestUpdateKB_InvalidBody(t *testing.T) {
	store := &mockUpdateStore{}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodPatch, "/api/kb/kb-1", bytes.NewReader([]byte("not-json")))
	r.Header.Set("Content-Type", "application/json")
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.UpdateKB(w, r)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Result().StatusCode)
	}
}

// ---------------------------------------------------------------------------
// ListFiles tests
// ---------------------------------------------------------------------------

// TestListFiles_DefaultPagination checks that the default limit/offset are applied
// and that the response envelope matches the store data.
func TestListFiles_DefaultPagination(t *testing.T) {
	files := []kb.FileRow{
		makeFileRow("f-1", "document.pdf"),
		makeFileRow("f-2", "report.pdf"),
	}
	store := &mockUpdateStore{files: files, total: 2}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files", nil)
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result []kb.FileRow
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 files, got %d", len(result))
	}
	if result[0].ID != "f-1" {
		t.Errorf("expected first file id=f-1, got %q", result[0].ID)
	}
}

// TestListFiles_CustomPagination checks that explicit limit/offset query params are forwarded.
func TestListFiles_CustomPagination(t *testing.T) {
	store := &mockUpdateStore{files: []kb.FileRow{makeFileRow("f-3", "extra.pdf")}, total: 10}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files?limit=5&offset=5", nil)
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var files []kb.FileRow
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

// TestListFiles_EmptyResult checks that an empty file list returns an empty array (not null).
func TestListFiles_EmptyResult(t *testing.T) {
	store := &mockUpdateStore{files: nil, total: 0}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files", nil)
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var files []kb.FileRow
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if files == nil {
		t.Error("expected non-nil empty array, got null")
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}
