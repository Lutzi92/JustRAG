package adminusers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/adminusers"
	"github.com/justrag/go-backend/internal/auth"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var (
	_ adminusers.Store          = (*mockStore)(nil)
	_ adminusers.CascadeDeleter = (*mockCascadeDeleter)(nil)
	_ auth.BlacklistStore       = (*mockBlacklistStore)(nil)
)

type mockStore struct {
	users       []adminusers.UserListRow
	updatedUser *adminusers.UserListRow
	listErr     error
	updateErr   error
	auditErr    error

	// Recorded calls
	lastUpdateID   string
	lastUpdateRole string
	auditCalls     []auditCall
}

type auditCall struct {
	operatorID string
	action     string
	targetType string
	targetID   string
}

func (m *mockStore) ListAllUsers(_ context.Context) ([]adminusers.UserListRow, error) {
	return m.users, m.listErr
}

func (m *mockStore) GetUserByID(_ context.Context, id string) (*adminusers.UserListRow, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	for i := range m.users {
		if m.users[i].ID == id {
			return &m.users[i], nil
		}
	}
	return nil, nil
}

func (m *mockStore) UpdateUserRole(_ context.Context, id, role string) (*adminusers.UserListRow, error) {
	m.lastUpdateID = id
	m.lastUpdateRole = role
	return m.updatedUser, m.updateErr
}

func (m *mockStore) LogAuditAction(_ context.Context, operatorID, action, targetType, targetID string, _ any) error {
	m.auditCalls = append(m.auditCalls, auditCall{
		operatorID: operatorID,
		action:     action,
		targetType: targetType,
		targetID:   targetID,
	})
	return m.auditErr
}

// ---------------------------------------------------------------------------
// Mock cascade deleter
// ---------------------------------------------------------------------------

type mockCascadeDeleter struct {
	deletedUserID string
	err           error
}

func (m *mockCascadeDeleter) DeleteUser(_ context.Context, userID string) error {
	m.deletedUserID = userID
	return m.err
}

// ---------------------------------------------------------------------------
// Mock blacklist
// ---------------------------------------------------------------------------

type mockBlacklistStore struct {
	store map[string]string
}

func newMockBlacklistStore() *mockBlacklistStore {
	return &mockBlacklistStore{store: make(map[string]string)}
}

func (m *mockBlacklistStore) Get(_ context.Context, key string) (string, error) {
	v, ok := m.store[key]
	if !ok {
		return "", auth.ErrKeyNotFound
	}
	return v, nil
}

func (m *mockBlacklistStore) Set(_ context.Context, key string, value any, _ time.Duration) error {
	m.store[key] = fmt.Sprintf("%v", value)
	return nil
}

func (m *mockBlacklistStore) Exists(_ context.Context, keys ...string) (int64, error) {
	count := int64(0)
	for _, k := range keys {
		if _, ok := m.store[k]; ok {
			count++
		}
	}
	return count, nil
}

func (m *mockBlacklistStore) Pipeline(_ context.Context, _ func(pipe auth.PipelineExecer) error) error {
	return fmt.Errorf("pipeline not supported in mock")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }

func makeUser(id, username, role string) adminusers.UserListRow {
	return adminusers.UserListRow{
		ID:        id,
		Username:  username,
		FirstName: strPtr("First"),
		LastName:  strPtr("Last"),
		Role:      role,
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// injectUser adds a fake JWT claims into the request context, simulating auth middleware.
func injectUser(r *http.Request, id, role string) *http.Request {
	claims := &auth.Claims{ID: id, Username: "testuser", Role: role}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// newHandler creates a Handler with a no-op cascade deleter for tests that don't exercise deletion.
func newHandler(store *mockStore) *adminusers.Handler {
	bl := auth.NewBlacklist(newMockBlacklistStore(), false)
	return adminusers.NewHandler(store, bl, &mockCascadeDeleter{})
}

// newHandlerWithDeleter creates a Handler with the given cascade deleter.
func newHandlerWithDeleter(store *mockStore, deleter *mockCascadeDeleter) *adminusers.Handler {
	bl := auth.NewBlacklist(newMockBlacklistStore(), false)
	return adminusers.NewHandler(store, bl, deleter)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// 1. List users → 200 + array
func TestListUsers_OK(t *testing.T) {
	users := []adminusers.UserListRow{
		makeUser("user-1", "alice", "admin"),
		makeUser("user-2", "bob", "user"),
	}
	store := &mockStore{users: users}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	rr := httptest.NewRecorder()

	h.ListUsers(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result []adminusers.UserListRow
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 users, got %d", len(result))
	}
	if result[0].ID != "user-1" {
		t.Errorf("expected user-1, got %s", result[0].ID)
	}
}

// 2. Update role valid → 200
func TestUpdateUserRole_Valid(t *testing.T) {
	targetUser := makeUser("user-2", "bob", "user")
	updatedUser := makeUser("user-2", "bob", "admin")

	store := &mockStore{
		users:       []adminusers.UserListRow{targetUser},
		updatedUser: &updatedUser,
	}
	h := newHandler(store)

	body := bytes.NewBufferString(`{"role":"admin"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/users/user-2/role", body)
	req.SetPathValue("id", "user-2")
	req = injectUser(req, "caller-1", "superadmin")
	rr := httptest.NewRecorder()

	h.UpdateUserRole(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var result adminusers.UserListRow
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result.Role != "admin" {
		t.Errorf("expected role=admin, got %s", result.Role)
	}

	// Audit log should have been called.
	if len(store.auditCalls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(store.auditCalls))
	}
	if store.auditCalls[0].action != "user.role_change" {
		t.Errorf("expected action=user.role_change, got %s", store.auditCalls[0].action)
	}
}

// 3. Update role invalid value → 400
func TestUpdateUserRole_InvalidRole(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	body := bytes.NewBufferString(`{"role":"owner"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/users/user-2/role", body)
	req.SetPathValue("id", "user-2")
	req = injectUser(req, "caller-1", "admin")
	rr := httptest.NewRecorder()

	h.UpdateUserRole(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// 4. Update own role → 403
func TestUpdateUserRole_OwnRole(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	body := bytes.NewBufferString(`{"role":"user"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/users/caller-1/role", body)
	req.SetPathValue("id", "caller-1")
	req = injectUser(req, "caller-1", "admin")
	rr := httptest.NewRecorder()

	h.UpdateUserRole(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

// Non-superadmin caller cannot change a superadmin's role → 403
func TestUpdateUserRole_SuperadminProtection(t *testing.T) {
	targetUser := makeUser("user-super", "superuser", "superadmin")
	store := &mockStore{
		users: []adminusers.UserListRow{targetUser},
	}
	h := newHandler(store)

	body := bytes.NewBufferString(`{"role":"user"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/users/user-super/role", body)
	req.SetPathValue("id", "user-super")
	req = injectUser(req, "caller-1", "admin")
	rr := httptest.NewRecorder()

	h.UpdateUserRole(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

// 5. Delete user → 204, cascade deleter invoked, audit logged.
func TestDeleteUser_OK(t *testing.T) {
	store := &mockStore{}
	deleter := &mockCascadeDeleter{}
	h := newHandlerWithDeleter(store, deleter)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/users/user-2", nil)
	req.SetPathValue("id", "user-2")
	req = injectUser(req, "caller-1", "superadmin")
	rr := httptest.NewRecorder()

	h.DeleteUser(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if deleter.deletedUserID != "user-2" {
		t.Errorf("expected cascade deleter called with user-2, got %q", deleter.deletedUserID)
	}

	// Audit log should have been called.
	if len(store.auditCalls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(store.auditCalls))
	}
	if store.auditCalls[0].action != "user.delete" {
		t.Errorf("expected action=user.delete, got %s", store.auditCalls[0].action)
	}
}

// 6. Delete self → 403
func TestDeleteUser_Self(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/users/caller-1", nil)
	req.SetPathValue("id", "caller-1")
	req = injectUser(req, "caller-1", "superadmin")
	rr := httptest.NewRecorder()

	h.DeleteUser(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

// 7. Delete user — cascade deleter error → 500
func TestDeleteUser_CascadeError(t *testing.T) {
	store := &mockStore{}
	deleter := &mockCascadeDeleter{err: errors.New("db failure")}
	h := newHandlerWithDeleter(store, deleter)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/users/user-3", nil)
	req.SetPathValue("id", "user-3")
	req = injectUser(req, "caller-1", "superadmin")
	rr := httptest.NewRecorder()

	h.DeleteUser(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	// No audit log on failure.
	if len(store.auditCalls) != 0 {
		t.Errorf("expected no audit calls on failure, got %d", len(store.auditCalls))
	}
}
