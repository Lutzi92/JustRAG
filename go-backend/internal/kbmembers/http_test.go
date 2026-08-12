package kbmembers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbmembers"
)

// mockStore is a test double for kbmembers.Store.
type mockStore struct {
	members []kbmembers.Member
	role    string
	roleErr error

	setRoleErr  error
	setRoleCall bool

	removeErr  error
	removeCall bool

	transferErr error

	leaveDeleted int
	leaveErr     error

	countChats int
	countErr   error
}

func (m *mockStore) ListMembers(context.Context, string) ([]kbmembers.Member, error) {
	return m.members, nil
}

func (m *mockStore) GetRole(context.Context, string, string) (string, error) {
	return m.role, m.roleErr
}

func (m *mockStore) SetRole(context.Context, string, string, string, string) error {
	m.setRoleCall = true
	return m.setRoleErr
}

func (m *mockStore) RemoveMember(context.Context, string, string) error {
	m.removeCall = true
	return m.removeErr
}

func (m *mockStore) TransferOwner(context.Context, string, string) error {
	return m.transferErr
}

func (m *mockStore) LeaveKB(context.Context, string, string) (int, error) {
	return m.leaveDeleted, m.leaveErr
}

func (m *mockStore) CountOwnChats(context.Context, string, string) (int, error) {
	return m.countChats, m.countErr
}

var _ kbmembers.Store = (*mockStore)(nil)

// mockPending is a test double for kbmembers.PendingInviteStore.
type mockPending struct{}

func (mockPending) GetUserIDByUsername(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (mockPending) UpsertPendingInvite(context.Context, string, string, string, string) error {
	return nil
}
func (mockPending) ListPendingInvites(context.Context, string) ([]kbmembers.PendingInvite, error) {
	return nil, nil
}

var _ kbmembers.PendingInviteStore = mockPending{}

// adminContext injects a KBAccessResult resolved to role and a Claims user
// with the given id.
func adminContext(ctx context.Context, kbID, role, userID string) context.Context {
	access := &kbaccess.KBAccessResult{
		KB:      &kbaccess.KnowledgeBase{ID: kbID},
		Role:    role,
		IsOwner: role == kbaccess.RoleOwner,
	}
	ctx = kbaccess.WithAccess(ctx, access)
	ctx = auth.WithUser(ctx, &auth.Claims{ID: userID, Role: "user"})
	return ctx
}

func TestSetMemberRole_RejectsOwner(t *testing.T) {
	store := &mockStore{}
	handler := kbmembers.NewHandler(store, mockPending{})

	body := `{"role":"owner"}`
	req := httptest.NewRequest(http.MethodPut, "/api/kb/kb-1/members/user-2", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("userId", "user-2")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleAdmin, "admin-1"))

	rr := httptest.NewRecorder()
	handler.SetMemberRole(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.setRoleCall {
		t.Errorf("SetRole should not have been called")
	}
}

func TestSetMemberRole_RejectsUnknownRole(t *testing.T) {
	store := &mockStore{}
	handler := kbmembers.NewHandler(store, mockPending{})

	body := `{"role":"editor"}`
	req := httptest.NewRequest(http.MethodPut, "/api/kb/kb-1/members/user-2", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("userId", "user-2")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleAdmin, "admin-1"))

	rr := httptest.NewRecorder()
	handler.SetMemberRole(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.setRoleCall {
		t.Errorf("SetRole should not have been called")
	}
}

func TestRemoveMember_OwnerImmutable(t *testing.T) {
	store := &mockStore{removeErr: kbmembers.ErrOwnerImmutable}
	handler := kbmembers.NewHandler(store, mockPending{})

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/members/user-2", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("userId", "user-2")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleAdmin, "admin-1"))

	rr := httptest.NewRecorder()
	handler.RemoveMember(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestRemoveMember_Self_Allowed: an admin removing themselves via
// DELETE /members/{ownId} is allowed — it's a leave. Chats survive because
// the path goes through RemoveMember, not LeaveKB.
func TestRemoveMember_Self_Allowed(t *testing.T) {
	store := &mockStore{}
	handler := kbmembers.NewHandler(store, mockPending{})

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/members/admin-1", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("userId", "admin-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleAdmin, "admin-1"))

	rr := httptest.NewRecorder()
	handler.RemoveMember(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if !store.removeCall {
		t.Errorf("expected RemoveMember to be called")
	}
}

func TestTransferOwner_NonOwnerForbidden(t *testing.T) {
	store := &mockStore{}
	handler := kbmembers.NewHandler(store, mockPending{})

	body := `{"userId":"user-2"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/transfer-owner", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleAdmin, "admin-1"))

	rr := httptest.NewRecorder()
	handler.TransferOwner(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLeaveKB_OwnerForbidden(t *testing.T) {
	store := &mockStore{leaveErr: kbmembers.ErrOwnerImmutable}
	handler := kbmembers.NewHandler(store, mockPending{})

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/membership", nil)
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleOwner, "owner-1"))

	rr := httptest.NewRecorder()
	handler.LeaveKB(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error == "" {
		t.Fatalf("expected a hint about transferring or deleting, got empty error")
	}
}

func TestLeaveKB_ReturnsDeletedChatCount(t *testing.T) {
	store := &mockStore{leaveDeleted: 3}
	handler := kbmembers.NewHandler(store, mockPending{})

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/membership", nil)
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleEdit, "user-2"))

	rr := httptest.NewRecorder()
	handler.LeaveKB(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var got struct {
		DeletedChats int `json:"deletedChats"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DeletedChats != 3 {
		t.Errorf("deletedChats = %d, want 3", got.DeletedChats)
	}
}
