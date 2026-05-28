package kb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
)

var _ kb.ShareStore = (*mockShareStore)(nil)

// mockShareStore is a test double for kb.ShareStore.
type mockShareStore struct {
	shares    []kb.ShareRow
	addResult *kb.ShareRow
	addErr    error
	removeErr error
}

func (m *mockShareStore) ListKBShares(_ context.Context, _ string) ([]kb.ShareRow, error) {
	return m.shares, nil
}

func (m *mockShareStore) AddKBShare(_ context.Context, _, _, _ string) (*kb.ShareRow, error) {
	return m.addResult, m.addErr
}

func (m *mockShareStore) RemoveKBShare(_ context.Context, _, _ string) error {
	return m.removeErr
}

// ownerContext injects a KBAccessResult (IsOwner: true) and a Claims user into the context.
func ownerContext(ctx context.Context, kbID string) context.Context {
	access := &kbaccess.KBAccessResult{
		KB:         &kbaccess.KnowledgeBase{ID: kbID},
		IsOwner:    true,
		Permission: "edit",
	}
	ctx = kbaccess.WithAccess(ctx, access)
	ctx = auth.WithUser(ctx, &auth.Claims{ID: "user-1", Role: "user"})
	return ctx
}

func TestListShares(t *testing.T) {
	now := time.Now()
	store := &mockShareStore{
		shares: []kb.ShareRow{
			{ID: "share-1", UserID: "user-2", Username: "alice", Permission: "view", CreatedAt: now},
		},
	}
	handler := kb.NewSharingHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/shares", nil)
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.ListShares(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []kb.ShareRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 share, got %d", len(got))
	}
	if got[0].Username != "alice" {
		t.Errorf("expected username alice, got %q", got[0].Username)
	}
}

func TestAddShare_Valid(t *testing.T) {
	now := time.Now()
	store := &mockShareStore{
		addResult: &kb.ShareRow{
			ID:         "share-1",
			UserID:     "user-2",
			Username:   "bob",
			Permission: "edit",
			CreatedAt:  now,
		},
	}
	handler := kb.NewSharingHandler(store)

	body := `{"userId":"user-2","permission":"edit"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/share", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.AddShare(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var got kb.ShareRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.UserID != "user-2" {
		t.Errorf("expected userId user-2, got %q", got.UserID)
	}
}

func TestAddShare_MissingUserID(t *testing.T) {
	store := &mockShareStore{}
	handler := kb.NewSharingHandler(store)

	body := `{"userId":"","permission":"view"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/share", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.AddShare(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestRemoveShare(t *testing.T) {
	store := &mockShareStore{removeErr: nil}
	handler := kb.NewSharingHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/share/user-2", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("userId", "user-2")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.RemoveShare(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}
