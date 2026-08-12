package kbaccess_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
)

var _ kbaccess.KBStore = (*mockStore)(nil)

const (
	kbID   = "kb-1"
	userID = "user-1"
)

// mockStore implements KBStore for tests. It hands back a single fixed KB and
// a single fixed kb_members role regardless of the ids passed in — enough for
// the resolution-ladder matrix below, where each row builds its own store.
type mockStore struct {
	kb   *kbaccess.KnowledgeBase
	role string
}

func (m *mockStore) GetKBByID(context.Context, string) (*kbaccess.KnowledgeBase, error) {
	return m.kb, nil
}

func (m *mockStore) GetKBRole(context.Context, string, string) (string, error) {
	return m.role, nil
}

// claimsCtx injects auth.Claims into a context via auth.WithUser.
func claimsCtx(parent context.Context, c auth.Claims) context.Context {
	return auth.WithUser(parent, &c)
}

// okHandler is a trivial handler that writes 200.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// ---------- TestEffectiveRole: the resolution-ladder matrix ----------

// TestEffectiveRole deckt das Kreuzprodukt der fuenf Aufloesungsregeln ab.
// Diese Tabelle ist die Sicherheitsgrenze des Features — jede neue Regel
// braucht hier Zeilen.
func TestEffectiveRole(t *testing.T) {
	tests := []struct {
		name        string
		sysRole     string // auth-Rolle des Requests
		memberRole  string // Zeile in kb_members, "" = keine
		isGlobal    bool
		isPublished bool
		required    string
		wantStatus  int
		wantRole    string
	}{
		{"superadmin_on_foreign_private", "superadmin", "", false, false, "owner", 200, "owner"},
		{"owner_row", "user", "owner", false, false, "owner", 200, "owner"},
		{"admin_row_denied_owner_gate", "user", "admin", false, false, "owner", 403, ""},
		{"admin_row_passes_admin_gate", "user", "admin", false, false, "admin", 200, "admin"},
		{"edit_row_denied_admin_gate", "user", "edit", false, false, "admin", 403, ""},
		{"edit_row_passes_edit_gate", "user", "edit", false, false, "edit", 200, "edit"},
		{"view_row_denied_edit_gate", "user", "view", false, false, "edit", 403, ""},
		{"stranger_private", "user", "", false, false, "view", 403, ""},
		{"sysadmin_on_public", "admin", "", true, true, "admin", 200, "admin"},
		{"sysadmin_on_unpublished_public", "admin", "", true, false, "admin", 200, "admin"},
		{"user_on_published_public", "user", "", true, true, "view", 200, "view"},
		{"user_on_published_public_denied_edit", "user", "", true, true, "edit", 403, ""},
		{"user_on_unpublished_public", "user", "", true, false, "view", 403, ""},
		{"member_row_beats_public_implicit", "user", "edit", true, true, "edit", 200, "edit"},
		{"apiuser_on_published_public", "api-user", "", true, true, "view", 200, "view"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{
				kb: &kbaccess.KnowledgeBase{
					ID: kbID, IsGlobal: tt.isGlobal, IsPublished: tt.isPublished,
				},
				role: tt.memberRole,
			}
			mw := kbaccess.NewMiddleware(store)

			var gotRole string
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotRole = kbaccess.AccessFromContext(r.Context()).Role
			})

			req := httptest.NewRequest(http.MethodGet, "/api/kb/"+kbID, nil)
			req.SetPathValue("id", kbID)
			req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: userID, Role: tt.sysRole}))

			rr := httptest.NewRecorder()
			mw.RequireKBRole(tt.required)(next).ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tt.wantStatus, rr.Body)
			}
			if gotRole != tt.wantRole {
				t.Errorf("effective role = %q, want %q", gotRole, tt.wantRole)
			}
		})
	}
}

// ---------- RequireKBRole: auth / not-found edge cases not covered by the matrix ----------

func TestRequireKBRole_NoAuth401(t *testing.T) {
	store := &mockStore{kb: &kbaccess.KnowledgeBase{ID: kbID}, role: ""}
	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBRole(kbaccess.RoleView)(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/kb/"+kbID, nil)
	req.SetPathValue("id", kbID)
	// No claims in context — simulates missing/invalid auth.

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestRequireKBRole_KBNotFound404(t *testing.T) {
	store := &mockStore{kb: nil, role: ""}
	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBRole(kbaccess.RoleView)(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/kb/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: userID, Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// ---------- RequireAnalyticsAccess tests ----------

func TestRequireAnalyticsAccess_NonGlobalKB403(t *testing.T) {
	store := &mockStore{kb: &kbaccess.KnowledgeBase{ID: kbID, IsGlobal: false}, role: ""}
	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireAnalyticsAccess(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/kb/"+kbID+"/analytics", nil)
	req.SetPathValue("id", kbID)
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "admin-1", Role: "admin"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-global KB analytics, got %d", rec.Code)
	}
}

func TestRequireAnalyticsAccess_GlobalKBAdminGets200(t *testing.T) {
	store := &mockStore{kb: &kbaccess.KnowledgeBase{ID: kbID, IsGlobal: true}, role: ""}
	mw := kbaccess.NewMiddleware(store)

	var captured *kbaccess.KBAccessResult
	capHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = kbaccess.AccessFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler := mw.RequireAnalyticsAccess(capHandler)

	req := httptest.NewRequest(http.MethodGet, "/kb/"+kbID+"/analytics", nil)
	req.SetPathValue("id", kbID)
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "admin-1", Role: "admin"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for admin accessing global KB analytics, got %d", rec.Code)
	}
	if captured == nil {
		t.Fatal("expected KBAccessResult in context")
	}
	if captured.Role != kbaccess.RoleAdmin {
		t.Errorf("expected Role = %q, got %q", kbaccess.RoleAdmin, captured.Role)
	}
}

func TestRequireAnalyticsAccess_GlobalKBRegularUser403(t *testing.T) {
	store := &mockStore{kb: &kbaccess.KnowledgeBase{ID: kbID, IsGlobal: true}, role: ""}
	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireAnalyticsAccess(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/kb/"+kbID+"/analytics", nil)
	req.SetPathValue("id", kbID)
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: userID, Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for regular user accessing analytics, got %d", rec.Code)
	}
}

func TestRequireAnalyticsAccess_NoAuth401(t *testing.T) {
	store := &mockStore{kb: &kbaccess.KnowledgeBase{ID: kbID, IsGlobal: true}, role: ""}
	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireAnalyticsAccess(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/kb/"+kbID+"/analytics", nil)
	req.SetPathValue("id", kbID)
	// No claims.

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing auth, got %d", rec.Code)
	}
}

// ---------- AccessFromContext test ----------

func TestAccessFromContext_NilWhenMissing(t *testing.T) {
	ctx := context.Background()
	result := kbaccess.AccessFromContext(ctx)
	if result != nil {
		t.Error("expected nil when no access result in context")
	}
}
