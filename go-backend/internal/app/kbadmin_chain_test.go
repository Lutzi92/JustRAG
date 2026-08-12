package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// kbStoreStub lets us script the KB row and the caller's kb_members role
// per test. Implements kbaccess.KBStore.
type kbStoreStub struct {
	kb   *kbaccess.KnowledgeBase
	role string
}

func (s kbStoreStub) GetKBByID(_ context.Context, _ string) (*kbaccess.KnowledgeBase, error) {
	return s.kb, nil
}

func (s kbStoreStub) GetKBRole(_ context.Context, _, _ string) (string, error) {
	return s.role, nil
}

// buildAdminChain mirrors routes.go's kbAdminChain composition without the
// outer Authenticate wrapper. The test injects the user into the request
// context directly via auth.WithUser, so Authenticate (which only sets a
// user from a valid JWT) is not needed and would reject every request with
// 401. Unlike the old kbTuningChain, this does NOT wrap RequireRole around
// the KB check — the KB role 'admin' is the sole gate now.
func buildAdminChain(kbMw *kbaccess.Middleware) func(http.HandlerFunc) http.Handler {
	return func(h http.HandlerFunc) http.Handler {
		return kbMw.RequireKBRole(kbaccess.RoleAdmin)(http.HandlerFunc(h))
	}
}

// TestKbAdminChain_PlainUserWithKbAdminRolePasses covers the intended
// behaviour change: kbTuningChain used to additionally require the system
// role api-user/admin/superadmin, so a plain user could never reach the
// settings surface of their own KB even with an admin-level kb_members row.
// kbAdminChain drops that hurdle — the KB role alone decides.
func TestKbAdminChain_PlainUserWithKbAdminRolePasses(t *testing.T) {
	kbMw := kbaccess.NewMiddleware(kbStoreStub{
		kb:   &kbaccess.KnowledgeBase{ID: "kb1", IsGlobal: false},
		role: kbaccess.RoleAdmin,
	})
	chain := buildAdminChain(kbMw)

	handlerCalled := false
	h := chain(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/settings", nil)
	req.SetPathValue("id", "kb1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: "user-1", Role: auth.RoleUser}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200 (handlerCalled=%v)", w.Code, handlerCalled)
	}
	if !handlerCalled {
		t.Fatal("expected handler to be called")
	}
}

// TestKbAdminChain_EditRoleRejected ensures the edit/admin separation line
// holds at the settings endpoint: a kb_members row of 'edit' is not enough.
func TestKbAdminChain_EditRoleRejected(t *testing.T) {
	kbMw := kbaccess.NewMiddleware(kbStoreStub{
		kb:   &kbaccess.KnowledgeBase{ID: "kb1", IsGlobal: false},
		role: kbaccess.RoleEdit,
	})
	chain := buildAdminChain(kbMw)

	handlerCalled := false
	h := chain(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/settings", nil)
	req.SetPathValue("id", "kb1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: "user-1", Role: auth.RoleUser}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d want 403 (handlerCalled=%v)", w.Code, handlerCalled)
	}
	if handlerCalled {
		t.Fatal("expected handler NOT to be called")
	}
}

// TestKbAdminChain table-drives the rest of the kbaccess.EffectiveRole
// ladder as observed through kbAdminChain (required role: admin).
func TestKbAdminChain(t *testing.T) {
	ownerID := "user-1"
	cases := []struct {
		name     string
		sysRole  string
		userID   string
		kbOwner  *string
		kbRole   string // the caller's kb_members role, "" for none
		wantCode int
	}{
		{"owner-role+plain-user", auth.RoleUser, ownerID, &ownerID, kbaccess.RoleOwner, http.StatusOK},
		{"superadmin", auth.RoleSuperAdmin, "other", &ownerID, "", http.StatusOK},
		{"admin-role+plain-user", auth.RoleUser, "other", &ownerID, kbaccess.RoleAdmin, http.StatusOK},
		{"edit-role+plain-user", auth.RoleUser, "other", &ownerID, kbaccess.RoleEdit, http.StatusForbidden},
		{"view-role+plain-user", auth.RoleUser, "other", &ownerID, kbaccess.RoleView, http.StatusForbidden},
		{"non-member+plain-user", auth.RoleUser, "other", &ownerID, "", http.StatusForbidden},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kbMw := kbaccess.NewMiddleware(kbStoreStub{
				kb:   &kbaccess.KnowledgeBase{ID: "kb1", UserID: c.kbOwner, IsGlobal: false},
				role: c.kbRole,
			})
			chain := buildAdminChain(kbMw)

			handlerCalled := false
			h := chain(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/settings", nil)
			req.SetPathValue("id", "kb1")
			req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: c.userID, Role: c.sysRole}))

			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != c.wantCode {
				t.Fatalf("got %d want %d (handlerCalled=%v)", w.Code, c.wantCode, handlerCalled)
			}
		})
	}
}
