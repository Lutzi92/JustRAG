package kb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/store"
)

var _ kb.ShareStore = (*mockShareStore)(nil)

// mockShareStore is a test double for kb.ShareStore.
type mockShareStore struct {
	shares    []kb.ShareRow
	pending   []kb.PendingInviteRow
	addResult *kb.ShareRow
	addErr    error
	removeErr error

	// bulk-invite controls
	knownUsers       map[string]string // lower(username) -> userID
	existingShares   map[string]bool   // userID -> already shared
	upsertedPending  []string          // usernames sent to UpsertPendingInvite
	addedShareUsers  []string          // userIDs sent to AddKBShare
	removePendingErr error
}

func (m *mockShareStore) ListKBShares(_ context.Context, _ string) ([]kb.ShareRow, error) {
	return m.shares, nil
}

func (m *mockShareStore) AddKBShare(_ context.Context, _, userID, _ string) (*kb.ShareRow, error) {
	m.addedShareUsers = append(m.addedShareUsers, userID)
	if m.addResult != nil {
		return m.addResult, m.addErr
	}
	return &kb.ShareRow{UserID: userID, Permission: "view"}, m.addErr
}

func (m *mockShareStore) RemoveKBShare(_ context.Context, _, _ string) error {
	return m.removeErr
}

func (m *mockShareStore) GetUserIDByUsername(_ context.Context, username string) (string, bool, error) {
	id, ok := m.knownUsers[strings.ToLower(username)]
	return id, ok, nil
}

func (m *mockShareStore) ShareExists(_ context.Context, _, userID string) (bool, error) {
	return m.existingShares[userID], nil
}

func (m *mockShareStore) UpsertPendingInvite(_ context.Context, _, username, _, _ string) error {
	m.upsertedPending = append(m.upsertedPending, strings.ToLower(username))
	return nil
}

func (m *mockShareStore) ListPendingInvites(_ context.Context, _ string) ([]kb.PendingInviteRow, error) {
	return m.pending, nil
}

func (m *mockShareStore) RemovePendingInvite(_ context.Context, _, _ string) error {
	return m.removePendingErr
}

// ownerContext injects a KBAccessResult (IsOwner: true) and a Claims user into the context.
func ownerContext(ctx context.Context, kbID string) context.Context {
	access := &kbaccess.KBAccessResult{
		KB:      &kbaccess.KnowledgeBase{ID: kbID},
		IsOwner: true,
		Role:    kbaccess.RoleOwner,
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

	var got struct {
		Shares  []kb.ShareRow         `json:"shares"`
		Pending []kb.PendingInviteRow `json:"pending"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Shares) != 1 {
		t.Fatalf("expected 1 share, got %d", len(got.Shares))
	}
	if got.Shares[0].Username != "alice" {
		t.Errorf("expected username alice, got %q", got.Shares[0].Username)
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

func TestBulkInvite_Categorizes(t *testing.T) {
	store := &mockShareStore{
		knownUsers:     map[string]string{"alice": "user-alice", "bob": "user-bob"},
		existingShares: map[string]bool{"user-bob": true}, // bob already shared
	}
	handler := kb.NewSharingHandler(store)

	// alice -> shared, bob -> alreadyHadAccess, carol -> pending, ALICE dup ignored
	body := `{"usernames":["alice","bob","carol","ALICE"],"permission":"view"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/share/bulk", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.BulkInvite(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Shared           []string `json:"shared"`
		Pending          []string `json:"pending"`
		AlreadyHadAccess []string `json:"alreadyHadAccess"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Shared) != 1 || got.Shared[0] != "alice" {
		t.Errorf("shared = %v, want [alice]", got.Shared)
	}
	if len(got.AlreadyHadAccess) != 1 || got.AlreadyHadAccess[0] != "bob" {
		t.Errorf("alreadyHadAccess = %v, want [bob]", got.AlreadyHadAccess)
	}
	if len(got.Pending) != 1 || got.Pending[0] != "carol" {
		t.Errorf("pending = %v, want [carol]", got.Pending)
	}
	if len(store.upsertedPending) != 1 || store.upsertedPending[0] != "carol" {
		t.Errorf("upsertedPending = %v, want [carol]", store.upsertedPending)
	}
}

func TestBulkInvite_SkipsInviter(t *testing.T) {
	// ownerContext sets the current user to id "user-1". A pasted username that
	// resolves to user-1 must be skipped (folded into alreadyHadAccess), never
	// re-shared.
	ms := &mockShareStore{knownUsers: map[string]string{"me": "user-1"}}
	handler := kb.NewSharingHandler(ms)

	body := `{"usernames":["me"],"permission":"view"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/share/bulk", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.BulkInvite(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if len(ms.addedShareUsers) != 0 {
		t.Errorf("inviter should not be re-shared, got AddKBShare for %v", ms.addedShareUsers)
	}

	var got struct {
		AlreadyHadAccess []string `json:"alreadyHadAccess"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.AlreadyHadAccess) != 1 || got.AlreadyHadAccess[0] != "me" {
		t.Errorf("alreadyHadAccess = %v, want [me]", got.AlreadyHadAccess)
	}
}

func TestBulkInvite_TooMany(t *testing.T) {
	names := make([]string, kb.MaxBulkUsernames+1)
	for i := range names {
		names[i] = "u" + strconv.Itoa(i)
	}
	payload, _ := json.Marshal(map[string]any{"usernames": names, "permission": "view"})
	store := &mockShareStore{knownUsers: map[string]string{}}
	handler := kb.NewSharingHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/share/bulk", bytes.NewReader(payload))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.BulkInvite(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for >%d usernames, got %d", kb.MaxBulkUsernames, rr.Code)
	}
}

func TestBulkInvite_BadPermission(t *testing.T) {
	store := &mockShareStore{knownUsers: map[string]string{}}
	handler := kb.NewSharingHandler(store)
	// "admin" is a legitimate permission value now (kbaccess.Assignable
	// widened the check in Task 6 of the KB-role-model plan), so this fixture
	// uses "owner" instead — still rejected, since ownership only ever moves
	// via the explicit transfer endpoint, never through share/bulk.
	body := `{"usernames":["alice"],"permission":"owner"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/share/bulk", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.BulkInvite(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestRemovePendingShare(t *testing.T) {
	store := &mockShareStore{}
	handler := kb.NewSharingHandler(store)
	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/share/pending/carol", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("username", "carol")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.RemovePendingShare(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

// viewerContext injects a non-owner, non-superadmin KBAccessResult and Claims.
func viewerContext(ctx context.Context, kbID string) context.Context {
	access := &kbaccess.KBAccessResult{
		KB:      &kbaccess.KnowledgeBase{ID: kbID},
		IsOwner: false,
		Role:    kbaccess.RoleView,
	}
	ctx = kbaccess.WithAccess(ctx, access)
	ctx = auth.WithUser(ctx, &auth.Claims{ID: "user-9", Role: "user"})
	return ctx
}

func TestRemovePendingShare_NotFound(t *testing.T) {
	ms := &mockShareStore{removePendingErr: store.ErrNotFound}
	handler := kb.NewSharingHandler(ms)

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/share/pending/carol", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("username", "carol")
	req = req.WithContext(ownerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.RemovePendingShare(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRemovePendingShare_Forbidden(t *testing.T) {
	ms := &mockShareStore{}
	handler := kb.NewSharingHandler(ms)

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/share/pending/carol", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("username", "carol")
	req = req.WithContext(viewerContext(req.Context(), "kb-1"))

	rr := httptest.NewRecorder()
	handler.RemovePendingShare(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}
