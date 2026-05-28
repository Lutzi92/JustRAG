package gencontent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/gencontent"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ gencontent.Store = (*mockStore)(nil)

type mockStore struct {
	row     *gencontent.GenContentRow
	updated *gencontent.GenContentRow
	err     error
}

func (m *mockStore) GetGenContentByID(_ context.Context, _ string) (*gencontent.GenContentRow, error) {
	return m.row, m.err
}

func (m *mockStore) UpdateGenContent(_ context.Context, _ string, _ any) (*gencontent.GenContentRow, error) {
	return m.updated, m.err
}

func (m *mockStore) DeleteGenContent(_ context.Context, _ string) error {
	return m.err
}

func (m *mockStore) ListGeneratedContent(_ context.Context, _, _ string) ([]gencontent.GeneratedContentItem, error) {
	return nil, m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	ownerID   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	otherID   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	contentID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

func makeRow(userID string) *gencontent.GenContentRow {
	return &gencontent.GenContentRow{
		ID:        contentID,
		KbID:      "dddddddd-dddd-dddd-dddd-dddddddddddd",
		UserID:    userID,
		Type:      "presentation",
		Title:     "Test Presentation",
		Content:   map[string]any{"filePath": "/tmp/test.pptx"},
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func ownerClaims() *auth.Claims {
	return &auth.Claims{ID: ownerID, Role: "user"}
}

func otherClaims() *auth.Claims {
	return &auth.Claims{ID: otherID, Role: "user"}
}

func withClaims(r *http.Request, c *auth.Claims) *http.Request {
	ctx := auth.WithUser(r.Context(), c)
	return r.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Tests: UpdateGenContent
// ---------------------------------------------------------------------------

func TestUpdateGenContent_Success(t *testing.T) {
	originalRow := makeRow(ownerID)
	updatedRow := makeRow(ownerID)
	updatedRow.Content = map[string]any{"filePath": "/tmp/updated.pptx"}

	store := &mockStore{row: originalRow, updated: updatedRow}
	h := gencontent.NewHandler(store)

	body := map[string]any{
		"content": map[string]any{"filePath": "/tmp/updated.pptx"},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/generated-content/"+contentID, bytes.NewReader(bodyBytes))
	req.SetPathValue("id", contentID)
	req = withClaims(req, ownerClaims())

	rec := httptest.NewRecorder()
	h.UpdateGenContent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ID != contentID {
		t.Errorf("expected id=%s, got %s", contentID, resp.ID)
	}
}

func TestUpdateGenContent_NotFound(t *testing.T) {
	store := &mockStore{row: nil}
	h := gencontent.NewHandler(store)

	body := map[string]any{"content": map[string]any{}}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/generated-content/"+contentID, bytes.NewReader(bodyBytes))
	req.SetPathValue("id", contentID)
	req = withClaims(req, ownerClaims())

	rec := httptest.NewRecorder()
	h.UpdateGenContent(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUpdateGenContent_WrongUser_Returns403(t *testing.T) {
	store := &mockStore{row: makeRow(ownerID)}
	h := gencontent.NewHandler(store)

	body := map[string]any{"content": map[string]any{}}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/generated-content/"+contentID, bytes.NewReader(bodyBytes))
	req.SetPathValue("id", contentID)
	req = withClaims(req, otherClaims())

	rec := httptest.NewRecorder()
	h.UpdateGenContent(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: DeleteGenContent
// ---------------------------------------------------------------------------

func TestDeleteGenContent_Success(t *testing.T) {
	store := &mockStore{row: makeRow(ownerID)}
	h := gencontent.NewHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/generated-content/"+contentID, nil)
	req.SetPathValue("id", contentID)
	req = withClaims(req, ownerClaims())

	rec := httptest.NewRecorder()
	h.DeleteGenContent(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteGenContent_WrongUser_Returns403(t *testing.T) {
	store := &mockStore{row: makeRow(ownerID)}
	h := gencontent.NewHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/generated-content/"+contentID, nil)
	req.SetPathValue("id", contentID)
	req = withClaims(req, otherClaims())

	rec := httptest.NewRecorder()
	h.DeleteGenContent(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestDeleteGenContent_NotFound_Returns404(t *testing.T) {
	store := &mockStore{row: nil}
	h := gencontent.NewHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/generated-content/"+contentID, nil)
	req.SetPathValue("id", contentID)
	req = withClaims(req, ownerClaims())

	rec := httptest.NewRecorder()
	h.DeleteGenContent(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: GetGenContentByID not found
// ---------------------------------------------------------------------------

func TestGetGenContentByID_NotFound(t *testing.T) {
	// We exercise the not-found path through the UpdateGenContent handler, which
	// calls GetGenContentByID internally.
	store := &mockStore{row: nil, err: nil}
	h := gencontent.NewHandler(store)

	body := map[string]any{"content": map[string]any{}}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/generated-content/nonexistent", bytes.NewReader(bodyBytes))
	req.SetPathValue("id", "nonexistent")
	req = withClaims(req, ownerClaims())

	rec := httptest.NewRecorder()
	h.UpdateGenContent(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing record, got %d", rec.Code)
	}
}

func TestGetGenContentByID_StoreError(t *testing.T) {
	store := &mockStore{row: nil, err: errors.New("db error")}
	h := gencontent.NewHandler(store)

	body := map[string]any{"content": map[string]any{}}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/generated-content/"+contentID, bytes.NewReader(bodyBytes))
	req.SetPathValue("id", contentID)
	req = withClaims(req, ownerClaims())

	rec := httptest.NewRecorder()
	h.UpdateGenContent(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for store error, got %d", rec.Code)
	}
}
