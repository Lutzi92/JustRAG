package kbmembers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbmembers"
)

// mockStore is a test double for kbmembers.Store.
type mockStore struct {
	members []kbmembers.Member

	// role/roleErr back GetRole when roles is nil — the single-user tests
	// (TransferOwner target lookup, MembershipImpact) only ever look up one
	// userID per test and don't need per-user differentiation.
	role    string
	roleErr error
	// roles, when non-nil, backs GetRole per userID instead: present ->
	// (role, nil), absent -> ("", ErrNotFound). Needed by the bulk-invite
	// tests, which look up several different users' existing membership in
	// one request.
	roles map[string]string

	setRoleErr   error
	setRoleCall  bool
	setRoleCalls []string // userIDs SetRole was called with, in order

	removeErr  error
	removeCall bool

	transferErr  error
	transferCall bool

	leaveDeleted int
	leaveErr     error

	countChats int
	countErr   error
}

func (m *mockStore) ListMembers(context.Context, string) ([]kbmembers.Member, error) {
	return m.members, nil
}

func (m *mockStore) GetRole(_ context.Context, _ string, userID string) (string, error) {
	if m.roles != nil {
		if r, ok := m.roles[userID]; ok {
			return r, nil
		}
		return "", kbmembers.ErrNotFound
	}
	return m.role, m.roleErr
}

func (m *mockStore) SetRole(_ context.Context, _ string, userID string, _ string, _ string) error {
	m.setRoleCall = true
	m.setRoleCalls = append(m.setRoleCalls, userID)
	return m.setRoleErr
}

func (m *mockStore) RemoveMember(context.Context, string, string) error {
	m.removeCall = true
	return m.removeErr
}

func (m *mockStore) TransferOwner(context.Context, string, string) error {
	m.transferCall = true
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
type mockPending struct {
	knownUsers map[string]string // lower(username) -> userID
	upserted   []string          // usernames sent to UpsertPendingInvite, in order
}

func (m *mockPending) GetUserIDByUsername(_ context.Context, username string) (string, bool, error) {
	id, ok := m.knownUsers[strings.ToLower(username)]
	return id, ok, nil
}
func (m *mockPending) UpsertPendingInvite(_ context.Context, _, username, _, _ string) error {
	m.upserted = append(m.upserted, strings.ToLower(username))
	return nil
}
func (m *mockPending) ListPendingInvites(context.Context, string) ([]kbmembers.PendingInvite, error) {
	return nil, nil
}

var _ kbmembers.PendingInviteStore = (*mockPending)(nil)

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
	handler := kbmembers.NewHandler(store, &mockPending{})

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
	handler := kbmembers.NewHandler(store, &mockPending{})

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
	handler := kbmembers.NewHandler(store, &mockPending{})

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
	handler := kbmembers.NewHandler(store, &mockPending{})

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
	handler := kbmembers.NewHandler(store, &mockPending{})

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
	handler := kbmembers.NewHandler(store, &mockPending{})

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
	handler := kbmembers.NewHandler(store, &mockPending{})

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

// TestMembershipImpact_NonMember: a caller with no kb_members row (e.g. a
// plain viewer on a published global KB, resolved to 'view' by kbViewChain
// without ever having a membership row) must get 404, not a chat count for a
// membership that doesn't exist — DELETE /membership would 404 them too, and
// Task 8's frontend subscriber branch is designed around this 404.
func TestMembershipImpact_NonMember(t *testing.T) {
	store := &mockStore{roleErr: kbmembers.ErrNotFound, countChats: 7}
	handler := kbmembers.NewHandler(store, &mockPending{})

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/membership/impact", nil)
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleView, "viewer-1"))

	rr := httptest.NewRecorder()
	handler.MembershipImpact(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMembershipImpact_Member(t *testing.T) {
	store := &mockStore{role: kbaccess.RoleEdit, countChats: 5}
	handler := kbmembers.NewHandler(store, &mockPending{})

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/membership/impact", nil)
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleEdit, "user-2"))

	rr := httptest.NewRecorder()
	handler.MembershipImpact(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var got struct {
		ChatCount int `json:"chatCount"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ChatCount != 5 {
		t.Errorf("chatCount = %d, want 5", got.ChatCount)
	}
}

// TestTransferOwner_TargetNotMember: the target user id has no kb_members
// row. Without this check, Store.TransferOwner's unconditional
// INSERT ... ON CONFLICT DO UPDATE would silently hand ownership to an
// arbitrary id — and since the ex-owner is demoted to admin and only an
// owner may transfer, that would be a one-way trapdoor.
func TestTransferOwner_TargetNotMember(t *testing.T) {
	store := &mockStore{roleErr: kbmembers.ErrNotFound}
	handler := kbmembers.NewHandler(store, &mockPending{})

	body := `{"userId":"stranger-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/transfer-owner", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleOwner, "owner-1"))

	rr := httptest.NewRecorder()
	handler.TransferOwner(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.transferCall {
		t.Errorf("TransferOwner should not have been called for a non-member target")
	}
}

// TestTransferOwner_TargetAlreadyOwner: GetRole reports the target already
// holds 'owner' (shouldn't happen given the single-owner invariant, but the
// handler must not blindly re-run the transfer if it does).
func TestTransferOwner_TargetAlreadyOwner(t *testing.T) {
	store := &mockStore{role: kbaccess.RoleOwner}
	handler := kbmembers.NewHandler(store, &mockPending{})

	body := `{"userId":"owner-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/transfer-owner", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleOwner, "owner-1"))

	rr := httptest.NewRecorder()
	handler.TransferOwner(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.transferCall {
		t.Errorf("TransferOwner should not have been called when the target is already owner")
	}
}

func TestTransferOwner_Success(t *testing.T) {
	store := &mockStore{role: kbaccess.RoleEdit}
	handler := kbmembers.NewHandler(store, &mockPending{})

	body := `{"userId":"user-2"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/transfer-owner", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleOwner, "owner-1"))

	rr := httptest.NewRecorder()
	handler.TransferOwner(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if !store.transferCall {
		t.Errorf("expected TransferOwner to be called for an existing member")
	}
}

// The following BulkInvite tests were ported (not moved — see the doc
// comment on Handler.BulkInvite) from the now-deleted kb/http_sharing_test.go's
// TestBulkInvite_Categorizes / _SkipsInviter / _TooMany / _BadPermission,
// before Task 9 of the four-role KB permission model removed that deprecated
// surface (kb.SharingHandler.BulkInvite) entirely. Without this copy, this
// loop's coverage would have gone with it.

func TestBulkInvite_Categorizes(t *testing.T) {
	store := &mockStore{roles: map[string]string{"user-bob": kbaccess.RoleView}} // bob already a member
	pending := &mockPending{knownUsers: map[string]string{"alice": "user-alice", "bob": "user-bob"}}
	handler := kbmembers.NewHandler(store, pending)

	// alice -> shared, bob -> alreadyHadAccess, carol -> pending, ALICE dup ignored
	body := `{"usernames":["alice","bob","carol","ALICE"],"role":"view"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/members/bulk", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleOwner, "owner-1"))

	rr := httptest.NewRecorder()
	handler.BulkInvite(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var got kbmembers.BulkInviteResult
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
	if len(pending.upserted) != 1 || pending.upserted[0] != "carol" {
		t.Errorf("upserted pending = %v, want [carol]", pending.upserted)
	}
	if len(store.setRoleCalls) != 1 || store.setRoleCalls[0] != "user-alice" {
		t.Errorf("SetRole calls = %v, want [user-alice]", store.setRoleCalls)
	}
}

func TestBulkInvite_SkipsInviter(t *testing.T) {
	// adminContext sets the current user to id "owner-1". A pasted username
	// that resolves to owner-1 must be skipped (folded into alreadyHadAccess),
	// never re-granted a role.
	store := &mockStore{roles: map[string]string{}}
	pending := &mockPending{knownUsers: map[string]string{"me": "owner-1"}}
	handler := kbmembers.NewHandler(store, pending)

	body := `{"usernames":["me"],"role":"view"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/members/bulk", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleOwner, "owner-1"))

	rr := httptest.NewRecorder()
	handler.BulkInvite(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if len(store.setRoleCalls) != 0 {
		t.Errorf("inviter should not be re-granted a role, got SetRole for %v", store.setRoleCalls)
	}
	var got kbmembers.BulkInviteResult
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.AlreadyHadAccess) != 1 || got.AlreadyHadAccess[0] != "me" {
		t.Errorf("alreadyHadAccess = %v, want [me]", got.AlreadyHadAccess)
	}
}

func TestBulkInvite_TooMany(t *testing.T) {
	names := make([]string, kbmembers.MaxBulkUsernames+1)
	for i := range names {
		names[i] = "u" + strconv.Itoa(i)
	}
	payload, _ := json.Marshal(map[string]any{"usernames": names, "role": "view"})
	store := &mockStore{roles: map[string]string{}}
	handler := kbmembers.NewHandler(store, &mockPending{knownUsers: map[string]string{}})

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/members/bulk", bytes.NewReader(payload))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleOwner, "owner-1"))

	rr := httptest.NewRecorder()
	handler.BulkInvite(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for >%d usernames, got %d", kbmembers.MaxBulkUsernames, rr.Code)
	}
}

func TestBulkInvite_RejectsOwnerRole(t *testing.T) {
	store := &mockStore{roles: map[string]string{}}
	handler := kbmembers.NewHandler(store, &mockPending{knownUsers: map[string]string{}})

	body := `{"usernames":["alice"],"role":"owner"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/members/bulk", bytes.NewBufferString(body))
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(adminContext(req.Context(), "kb-1", kbaccess.RoleOwner, "owner-1"))

	rr := httptest.NewRecorder()
	handler.BulkInvite(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(store.setRoleCalls) != 0 {
		t.Errorf("SetRole should not have been called")
	}
}
