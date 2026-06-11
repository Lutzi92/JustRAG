package adminglobalkbs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/adminglobalkbs"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var (
	_ adminglobalkbs.Store          = (*mockStore)(nil)
	_ adminglobalkbs.CascadeDeleter = (*mockCascadeDeleter)(nil)
)

type mockStore struct {
	kbs     []adminglobalkbs.GlobalKBRow
	kb      *adminglobalkbs.GlobalKBRow
	editors []adminglobalkbs.GlobalKBEditorRow
	err     error
}

func (m *mockStore) ListGlobalKBs(_ context.Context) ([]adminglobalkbs.GlobalKBRow, error) {
	return m.kbs, m.err
}

func (m *mockStore) CreateGlobalKB(_ context.Context, _ adminglobalkbs.GlobalKBCreate) (*adminglobalkbs.GlobalKBRow, error) {
	return m.kb, m.err
}

func (m *mockStore) UpdateGlobalKB(_ context.Context, _ string, _ adminglobalkbs.GlobalKBUpdate) (*adminglobalkbs.GlobalKBRow, error) {
	return m.kb, m.err
}

func (m *mockStore) ListGlobalKBEditors(_ context.Context, _ string) ([]adminglobalkbs.GlobalKBEditorRow, error) {
	return m.editors, m.err
}

func (m *mockStore) AddGlobalKBEditor(_ context.Context, _, _ string) error {
	return m.err
}

func (m *mockStore) RemoveGlobalKBEditor(_ context.Context, _, _ string) error {
	return m.err
}

func (m *mockStore) LogAuditAction(_ context.Context, _, _, _, _ string, _ any) error {
	return nil
}

// ---------------------------------------------------------------------------
// Mock cascade deleter
// ---------------------------------------------------------------------------

type mockCascadeDeleter struct {
	deletedKBID string
	err         error
}

func (m *mockCascadeDeleter) DeleteGlobalKB(_ context.Context, kbID string) error {
	m.deletedKBID = kbID
	return m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeKB(id, name string) adminglobalkbs.GlobalKBRow {
	desc := "Test description"
	return adminglobalkbs.GlobalKBRow{
		ID:          id,
		Name:        name,
		Description: &desc,
		Language:    "de",
		IsPublished: true,
		CreatedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func makeEditor(id, username string) adminglobalkbs.GlobalKBEditorRow {
	first := "First"
	last := "Last"
	return adminglobalkbs.GlobalKBEditorRow{
		ID:        id,
		Username:  username,
		FirstName: &first,
		LastName:  &last,
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func newRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	return httptest.NewRequest(method, path, &buf)
}

// newHandler returns a handler with a no-op cascade deleter for tests that
// don't exercise the delete endpoint.
func newHandler(store *mockStore) *adminglobalkbs.Handler {
	return adminglobalkbs.NewHandlerWithDeleter(store, &mockCascadeDeleter{})
}

// ---------------------------------------------------------------------------
// Tests: Global KBs
// ---------------------------------------------------------------------------

func TestListGlobalKBs_OK(t *testing.T) {
	kb := makeKB("kb-1", "Global KB 1")
	store := &mockStore{kbs: []adminglobalkbs.GlobalKBRow{kb}}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/global-kbs", nil)
	rr := httptest.NewRecorder()
	h.ListGlobalKBs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []adminglobalkbs.GlobalKBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 KB, got %d", len(got))
	}
	if got[0].ID != "kb-1" {
		t.Errorf("expected id=kb-1, got %s", got[0].ID)
	}
}

func TestListGlobalKBs_Empty(t *testing.T) {
	store := &mockStore{kbs: []adminglobalkbs.GlobalKBRow{}}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/global-kbs", nil)
	rr := httptest.NewRecorder()
	h.ListGlobalKBs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []adminglobalkbs.GlobalKBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 KBs, got %d", len(got))
	}
}

func TestCreateGlobalKB_OK(t *testing.T) {
	kb := makeKB("kb-new", "New Global KB")
	store := &mockStore{kb: &kb}
	h := newHandler(store)

	body := map[string]any{"name": "New Global KB", "language": "en"}
	req := newRequest(http.MethodPost, "/api/admin/global-kbs", body)
	rr := httptest.NewRecorder()
	h.CreateGlobalKB(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}

	var got adminglobalkbs.GlobalKBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != "kb-new" {
		t.Errorf("expected id=kb-new, got %s", got.ID)
	}
}

func TestCreateGlobalKB_MissingName(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	body := map[string]any{"name": ""}
	req := newRequest(http.MethodPost, "/api/admin/global-kbs", body)
	rr := httptest.NewRecorder()
	h.CreateGlobalKB(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// TestDeleteGlobalKB_OK verifies cascade deletion returns 204 and invokes the deleter.
func TestDeleteGlobalKB_OK(t *testing.T) {
	store := &mockStore{}
	deleter := &mockCascadeDeleter{}
	h := adminglobalkbs.NewHandlerWithDeleter(store, deleter)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/global-kbs/kb-1", nil)
	req.SetPathValue("id", "kb-1")
	rr := httptest.NewRecorder()
	h.DeleteGlobalKB(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if deleter.deletedKBID != "kb-1" {
		t.Errorf("expected cascade deleter called with kb-1, got %q", deleter.deletedKBID)
	}
}

// TestDeleteGlobalKB_DeleterError verifies that a cascade deletion failure returns 500.
func TestDeleteGlobalKB_DeleterError(t *testing.T) {
	store := &mockStore{}
	deleter := &mockCascadeDeleter{err: errors.New("db failure")}
	h := adminglobalkbs.NewHandlerWithDeleter(store, deleter)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/global-kbs/kb-2", nil)
	req.SetPathValue("id", "kb-2")
	rr := httptest.NewRecorder()
	h.DeleteGlobalKB(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: Editors
// ---------------------------------------------------------------------------

func TestListEditors_OK(t *testing.T) {
	editor := makeEditor("user-1", "alice")
	store := &mockStore{editors: []adminglobalkbs.GlobalKBEditorRow{editor}}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/global-kbs/kb-1/editors", nil)
	req.SetPathValue("id", "kb-1")
	rr := httptest.NewRecorder()
	h.ListEditors(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []adminglobalkbs.GlobalKBEditorRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 editor, got %d", len(got))
	}
	if got[0].Username != "alice" {
		t.Errorf("expected username=alice, got %s", got[0].Username)
	}
}

func TestAddEditor_OK(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	body := map[string]string{"userId": "user-1"}
	req := newRequest(http.MethodPost, "/api/admin/global-kbs/kb-1/editors", body)
	req.SetPathValue("id", "kb-1")
	rr := httptest.NewRecorder()
	h.AddEditor(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
}

func TestAddEditor_MissingUserID(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	body := map[string]string{"userId": ""}
	req := newRequest(http.MethodPost, "/api/admin/global-kbs/kb-1/editors", body)
	req.SetPathValue("id", "kb-1")
	rr := httptest.NewRecorder()
	h.AddEditor(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestRemoveEditor_OK(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/global-kbs/kb-1/editors/user-1", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("userId", "user-1")
	rr := httptest.NewRecorder()
	h.RemoveEditor(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}
