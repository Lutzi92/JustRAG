package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// kbStoreStub lets us script ownership / share results per test.
type kbStoreStub struct {
	kb    *kbaccess.KnowledgeBase
	share *kbaccess.KBShare
}

func (s kbStoreStub) GetKBByID(_ context.Context, _ string) (*kbaccess.KnowledgeBase, error) {
	return s.kb, nil
}
func (s kbStoreStub) GetKBShare(_ context.Context, _, _ string) (*kbaccess.KBShare, error) {
	return s.share, nil
}
func (s kbStoreStub) IsGlobalKBEditor(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

// newTestAuthMiddleware returns an auth.Middleware suitable for unit tests that
// compose RequireRole without Authenticate. The middleware is constructed with
// an empty secret and a nil blacklist — safe here because RequireRole only reads
// the context user and does not touch the blacklist or JWT parsing path.
func newTestAuthMiddleware(_ *testing.T) *auth.Middleware {
	return auth.NewMiddleware("", nil)
}

// buildTuningChain mirrors routes.go's kbTuningChain composition without the
// outer Authenticate wrapper. The test injects the user into the request context
// directly via auth.WithUser, so Authenticate (which only sets a user from a
// valid JWT) is not needed and would reject every request with 401.
func buildTuningChain(mw *auth.Middleware, kbMw *kbaccess.Middleware) func(http.HandlerFunc) http.Handler {
	return func(h http.HandlerFunc) http.Handler {
		// Authenticate is exercised separately; here we test the role + KB-permission gate
		// against a pre-populated context user.
		return mw.RequireRole(auth.RoleAPIUser, auth.RoleAdmin)(
			kbMw.RequireKBPermission("edit")(http.HandlerFunc(h)))
	}
}

func TestKBTuningChain(t *testing.T) {
	ownerID := "user-1"
	cases := []struct {
		name     string
		role     string
		userID   string
		kbOwner  *string
		share    *kbaccess.KBShare
		wantCode int
	}{
		{"owner+api-user", auth.RoleAPIUser, ownerID, &ownerID, nil, http.StatusOK},
		{"owner+plain-user", auth.RoleUser, ownerID, &ownerID, nil, http.StatusForbidden},
		{"superadmin", auth.RoleSuperAdmin, "other", &ownerID, nil, http.StatusOK},
		{"edit-share+api-user", auth.RoleAPIUser, "other", &ownerID, &kbaccess.KBShare{Permission: "edit"}, http.StatusOK},
		{"view-share+api-user", auth.RoleAPIUser, "other", &ownerID, &kbaccess.KBShare{Permission: "view"}, http.StatusForbidden},
		{"non-member+api-user", auth.RoleAPIUser, "other", &ownerID, nil, http.StatusForbidden},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			authMw := newTestAuthMiddleware(t)
			kbMw := kbaccess.NewMiddleware(kbStoreStub{
				kb:    &kbaccess.KnowledgeBase{ID: "kb1", UserID: c.kbOwner},
				share: c.share,
			})
			chain := buildTuningChain(authMw, kbMw)

			handlerCalled := false
			h := chain(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/settings", nil)
			req.SetPathValue("id", "kb1")
			req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: c.userID, Role: c.role}))

			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != c.wantCode {
				t.Fatalf("got %d want %d (handlerCalled=%v)", w.Code, c.wantCode, handlerCalled)
			}
		})
	}
}
