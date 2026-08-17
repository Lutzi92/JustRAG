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
// 401. kbAdminChain is the plain four-role gate — no system-role term — and
// carries the per-KB decisions any KB admin may make (rename, membership,
// categories, canonicalize, community build).
func buildAdminChain(kbMw *kbaccess.Middleware) func(http.HandlerFunc) http.Handler {
	return func(h http.HandlerFunc) http.Handler {
		return kbMw.RequireKBRole(kbaccess.RoleAdmin)(http.HandlerFunc(h))
	}
}

// buildAdvancedChain calls THE production composition (kbAdvancedInner in
// routes.go), not a copy of it, minus only the outer Authenticate wrapper the
// tests supply themselves (see buildAdminChain).
//
// It used to rebuild the same nesting inline, and that made every test below
// vacuous with respect to the one thing they exist to protect: adding
// auth.RoleUser to routes.go's role list left the whole suite green, because
// these tests were asserting against their own mirror rather than the chain the
// server installs. Do not reintroduce a local copy — if the composition is
// worth pinning, the tests have to touch the real one.
func buildAdvancedChain(authMw *auth.Middleware, kbMw *kbaccess.Middleware) func(http.HandlerFunc) http.Handler {
	return kbAdvancedInner(authMw, kbMw)
}

// TestKbAdminChain_PlainUserWithKbAdminRolePasses pins the boundary between
// the two chains, and it is deliberately a pair with
// TestKbAdvancedChain_PlainUserRefused below.
//
// History, because this test has changed meaning twice and the next reader
// deserves the decision rather than the accident: kbTuningChain once required
// a system role in {api-user, admin, superadmin} on top of the KB role.
// Commit 9a4f83a folded that chain into kbAdminChain and dropped the system
// role term, and this test was written to pin the drop. That went too far —
// it made every KB owner, i.e. every ordinary user who ever created a KB, an
// operator of the 52-key per-KB site_config registry, of eval runs, and of
// the workflow presets. The system-role requirement is now restored on that
// advanced surface as kbAdvancedChain.
//
// What survives the restore, and what this test pins, is that kbAdminChain
// itself stays free of any system-role term: a plain user who is a KB admin
// still reaches the ordinary per-KB decisions. The gate came back on the
// advanced surface only, not on the four-role model as a whole.
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

	req := httptest.NewRequest(http.MethodPatch, "/api/kb/kb1", nil)
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

// TestKbAdvancedChain_PlainUserRefused is the restored gate, stated as an
// intent: the KB "advanced settings" surface (RAG settings, Agenten & Teams,
// Evals, Workflow, and the per-KB re-embed sweep the settings Save flow
// offers) requires a system role in {api-user, admin, superadmin} IN ADDITION
// to KB role admin.
//
// The caller here is the strongest possible plain user — 'owner' on the KB,
// which is what every user who creates a KB gets — and is still refused. That
// is the whole point: the KB role is not the permission for this surface. If
// this test ever goes green after someone puts an advanced route back on
// kbAdminChain, the surface is open to every KB creator again.
func TestKbAdvancedChain_PlainUserRefused(t *testing.T) {
	authMw := auth.NewMiddleware("test-secret-at-least-32-characters-long", nil)
	kbMw := kbaccess.NewMiddleware(kbStoreStub{
		kb:   &kbaccess.KnowledgeBase{ID: "kb1", IsGlobal: false},
		role: kbaccess.RoleOwner,
	})
	chain := buildAdvancedChain(authMw, kbMw)

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

// TestKbAdvancedChain table-drives the two-term gate: both the system role and
// the KB role must clear, and neither one alone is enough.
func TestKbAdvancedChain(t *testing.T) {
	ownerID := "user-1"
	cases := []struct {
		name     string
		sysRole  string
		userID   string
		kbRole   string // the caller's kb_members role, "" for none
		wantCode int
	}{
		// System role clears, KB role clears.
		{"api-user+kb-owner", auth.RoleAPIUser, ownerID, kbaccess.RoleOwner, http.StatusOK},
		{"api-user+kb-admin", auth.RoleAPIUser, "other", kbaccess.RoleAdmin, http.StatusOK},
		{"admin+kb-owner", auth.RoleAdmin, ownerID, kbaccess.RoleOwner, http.StatusOK},
		{"admin+kb-admin", auth.RoleAdmin, "other", kbaccess.RoleAdmin, http.StatusOK},
		// Superadmin passes RequireRole through its hoisted bypass and
		// resolves to KB role 'owner', so it needs no kb_members row.
		{"superadmin+no-kb-row", auth.RoleSuperAdmin, "other", "", http.StatusOK},

		// KB role clears, system role does not. This is the restored gate.
		{"plain-user+kb-owner", auth.RoleUser, ownerID, kbaccess.RoleOwner, http.StatusForbidden},
		{"plain-user+kb-admin", auth.RoleUser, "other", kbaccess.RoleAdmin, http.StatusForbidden},

		// System role clears, KB role does not. The KB-role requirement is
		// still load-bearing: an api-user or admin gets nothing on a private
		// KB they have no admin-level row on.
		{"api-user+kb-edit", auth.RoleAPIUser, "other", kbaccess.RoleEdit, http.StatusForbidden},
		{"api-user+kb-view", auth.RoleAPIUser, "other", kbaccess.RoleView, http.StatusForbidden},
		{"api-user+no-kb-row", auth.RoleAPIUser, "other", "", http.StatusForbidden},
		{"admin+no-kb-row-private-kb", auth.RoleAdmin, "other", "", http.StatusForbidden},

		// Neither clears.
		{"plain-user+kb-edit", auth.RoleUser, "other", kbaccess.RoleEdit, http.StatusForbidden},
		{"plain-user+no-kb-row", auth.RoleUser, "other", "", http.StatusForbidden},
	}

	authMw := auth.NewMiddleware("test-secret-at-least-32-characters-long", nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kbMw := kbaccess.NewMiddleware(kbStoreStub{
				kb:   &kbaccess.KnowledgeBase{ID: "kb1", UserID: &ownerID, IsGlobal: false},
				role: c.kbRole,
			})
			chain := buildAdvancedChain(authMw, kbMw)

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

// TestKbAdvancedChain_GlobalKBAdminRule covers rule 3 of the effective-role
// ladder through this chain: a system admin with no kb_members row resolves to
// KB admin on a PUBLIC KB (and to nothing on a private one, which the table
// above pins). Included because it is the one combination where the two terms
// are not independent — the same system role satisfies both.
func TestKbAdvancedChain_GlobalKBAdminRule(t *testing.T) {
	authMw := auth.NewMiddleware("test-secret-at-least-32-characters-long", nil)
	kbMw := kbaccess.NewMiddleware(kbStoreStub{
		kb:   &kbaccess.KnowledgeBase{ID: "kb1", IsGlobal: true, IsPublished: true},
		role: "",
	})
	chain := buildAdvancedChain(authMw, kbMw)

	handlerCalled := false
	h := chain(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/settings", nil)
	req.SetPathValue("id", "kb1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: "u", Role: auth.RoleAdmin}))

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
// holds: a kb_members row of 'edit' is not enough for the KB-admin tier.
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

	req := httptest.NewRequest(http.MethodPatch, "/api/kb/kb1", nil)
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

			req := httptest.NewRequest(http.MethodPatch, "/api/kb/kb1", nil)
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
