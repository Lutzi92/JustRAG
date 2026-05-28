package kb_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
)

var _ kb.CascadeDeleter = (*mockCascadeDeleter)(nil)

// mockCascadeDeleter is a test double for kb.CascadeDeleter.
type mockCascadeDeleter struct {
	deletedKBID string
	err         error
}

func (m *mockCascadeDeleter) DeleteKB(_ context.Context, kbID string) error {
	m.deletedKBID = kbID
	return m.err
}

// injectKBAccessResult injects a KBAccessResult into the request context so the
// DeleteKB handler can read it (normally set by kbaccess middleware).
func injectKBAccessResult(r *http.Request, result *kbaccess.KBAccessResult) *http.Request {
	return r.WithContext(kbaccess.WithAccess(r.Context(), result))
}

func ownerAccess(kbID string) *kbaccess.KBAccessResult {
	uid := "user-1"
	return &kbaccess.KBAccessResult{
		KB:         &kbaccess.KnowledgeBase{ID: kbID, UserID: &uid, IsGlobal: false},
		IsOwner:    true,
		Permission: "edit",
	}
}

func nonOwnerAccess(kbID string) *kbaccess.KBAccessResult {
	uid := "owner-99"
	return &kbaccess.KBAccessResult{
		KB:         &kbaccess.KnowledgeBase{ID: kbID, UserID: &uid, IsGlobal: false},
		IsOwner:    false,
		Permission: "view",
	}
}

// TestDeleteKB_OwnerGets204 verifies that the KB owner receives 204.
func TestDeleteKB_OwnerGets204(t *testing.T) {
	deleter := &mockCascadeDeleter{}
	h := kb.NewDeleteHandler(deleter)

	r := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1", nil)
	r = withUser(r, &auth.Claims{ID: "user-1", Role: "user"})
	r = injectKBAccessResult(r, ownerAccess("kb-1"))

	rec := httptest.NewRecorder()
	h.DeleteKB(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
	if deleter.deletedKBID != "kb-1" {
		t.Errorf("expected DeleteKB to be called with kb-1, got %q", deleter.deletedKBID)
	}
}

// TestDeleteKB_SuperadminGets204 verifies that a superadmin can delete any KB.
func TestDeleteKB_SuperadminGets204(t *testing.T) {
	deleter := &mockCascadeDeleter{}
	h := kb.NewDeleteHandler(deleter)

	r := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-2", nil)
	r = withUser(r, &auth.Claims{ID: "superadmin-1", Role: "superadmin"})
	r = injectKBAccessResult(r, nonOwnerAccess("kb-2"))

	rec := httptest.NewRecorder()
	h.DeleteKB(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
	if deleter.deletedKBID != "kb-2" {
		t.Errorf("expected DeleteKB called with kb-2, got %q", deleter.deletedKBID)
	}
}

// TestDeleteKB_NonOwnerGets403 verifies that a non-owner without superadmin is rejected.
func TestDeleteKB_NonOwnerGets403(t *testing.T) {
	deleter := &mockCascadeDeleter{}
	h := kb.NewDeleteHandler(deleter)

	r := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-3", nil)
	r = withUser(r, &auth.Claims{ID: "user-99", Role: "user"})
	r = injectKBAccessResult(r, nonOwnerAccess("kb-3"))

	rec := httptest.NewRecorder()
	h.DeleteKB(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
	if deleter.deletedKBID != "" {
		t.Error("expected DeleteKB not to be called")
	}
}

// TestDeleteKB_DeleterError returns 500 when cascade deletion fails.
func TestDeleteKB_DeleterError(t *testing.T) {
	deleter := &mockCascadeDeleter{err: errors.New("db error")}
	h := kb.NewDeleteHandler(deleter)

	r := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-4", nil)
	r = withUser(r, &auth.Claims{ID: "user-1", Role: "user"})
	r = injectKBAccessResult(r, ownerAccess("kb-4"))

	rec := httptest.NewRecorder()
	h.DeleteKB(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// TestDeleteKB_NoAuth returns 401 when there is no authenticated user.
func TestDeleteKB_NoAuth(t *testing.T) {
	deleter := &mockCascadeDeleter{}
	h := kb.NewDeleteHandler(deleter)

	r := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-5", nil)
	// No user injected into context.
	r = injectKBAccessResult(r, ownerAccess("kb-5"))

	rec := httptest.NewRecorder()
	h.DeleteKB(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
