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

// mockStore implements KBStore for tests.
type mockStore struct {
	kbs           map[string]*kbaccess.KnowledgeBase
	shares        map[string]*kbaccess.KBShare // key: kbID+":"+userID
	globalEditors map[string]bool              // key: kbID+":"+userID
}

func newMockStore() *mockStore {
	return &mockStore{
		kbs:           make(map[string]*kbaccess.KnowledgeBase),
		shares:        make(map[string]*kbaccess.KBShare),
		globalEditors: make(map[string]bool),
	}
}

func (s *mockStore) GetKBByID(_ context.Context, id string) (*kbaccess.KnowledgeBase, error) {
	kb, ok := s.kbs[id]
	if !ok {
		return nil, nil // not found = nil, nil (matches dbstore contract)
	}
	return kb, nil
}

func (s *mockStore) GetKBShare(_ context.Context, kbID, userID string) (*kbaccess.KBShare, error) {
	share, ok := s.shares[kbID+":"+userID]
	if !ok {
		return nil, nil // no share = nil, nil (matches dbstore contract)
	}
	return share, nil
}

func (s *mockStore) IsGlobalKBEditor(_ context.Context, kbID, userID string) (bool, error) {
	return s.globalEditors[kbID+":"+userID], nil
}

// claimsCtx injects auth.Claims into a context via auth.WithUser.
func claimsCtx(parent context.Context, c auth.Claims) context.Context {
	return auth.WithUser(parent, &c)
}

// okHandler is a trivial handler that writes 200.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// ptr is a small helper to get a *string.
func ptr(s string) *string { return &s }

// ---------- RequireKBPermission tests ----------

// Test 1: Owner gets 200.
func TestRequireKBPermission_OwnerGets200(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-1"] = &kbaccess.KnowledgeBase{ID: "kb-1", UserID: ptr("user-1"), IsGlobal: false}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBPermission("view")(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-1", nil)
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "user-1", Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Also verify result is stored in context.
	// We need to capture the context inside the handler.
	var capturedResult *kbaccess.KBAccessResult
	capHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedResult = kbaccess.AccessFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	handler2 := mw.RequireKBPermission("view")(capHandler)
	req2 := httptest.NewRequest("GET", "/kb/kb-1", nil)
	req2.SetPathValue("id", "kb-1")
	req2 = req2.WithContext(claimsCtx(req2.Context(), auth.Claims{ID: "user-1", Role: "user"}))
	rec2 := httptest.NewRecorder()
	handler2.ServeHTTP(rec2, req2)

	if capturedResult == nil {
		t.Fatal("expected KBAccessResult in context")
	}
	if !capturedResult.IsOwner {
		t.Error("expected IsOwner to be true")
	}
	if capturedResult.Permission != "edit" {
		t.Errorf("expected edit permission for owner, got %q", capturedResult.Permission)
	}
}

// Test 2: No auth header gets 401.
func TestRequireKBPermission_NoAuth401(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-1"] = &kbaccess.KnowledgeBase{ID: "kb-1", UserID: ptr("user-99"), IsGlobal: false}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBPermission("view")(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-1", nil)
	req.SetPathValue("id", "kb-1")
	// No claims in context — simulates missing/invalid auth.

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// Test 3: KB not found gets 404.
func TestRequireKBPermission_KBNotFound404(t *testing.T) {
	store := newMockStore()
	// No KB added to store.

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBPermission("view")(okHandler)

	req := httptest.NewRequest("GET", "/kb/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "user-1", Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// Test 4: Global KB — any authenticated user can view (200).
func TestRequireKBPermission_GlobalKBAnyUserCanView(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-global"] = &kbaccess.KnowledgeBase{ID: "kb-global", UserID: ptr("owner-99"), IsGlobal: true}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBPermission("view")(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-global", nil)
	req.SetPathValue("id", "kb-global")
	// Regular user, not the owner, no shares.
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "user-regular", Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for regular user viewing global KB, got %d", rec.Code)
	}
}

// Test 5: Superadmin always gets full access.
func TestRequireKBPermission_SuperadminFullAccess(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-private"] = &kbaccess.KnowledgeBase{ID: "kb-private", UserID: ptr("owner-99"), IsGlobal: false}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBPermission("edit")(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-private", nil)
	req.SetPathValue("id", "kb-private")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "superadmin-1", Role: "superadmin"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for superadmin, got %d", rec.Code)
	}
}

// Test 6: Shared KB with "view" permission, requesting "view" → 200.
func TestRequireKBPermission_SharedViewAccess(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-shared"] = &kbaccess.KnowledgeBase{ID: "kb-shared", UserID: ptr("owner-99"), IsGlobal: false}
	store.shares["kb-shared:user-shared"] = &kbaccess.KBShare{Permission: "view"}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBPermission("view")(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-shared", nil)
	req.SetPathValue("id", "kb-shared")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "user-shared", Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for shared view user, got %d", rec.Code)
	}
}

// Test 7: Shared KB with "view" permission, requesting "edit" → 403.
func TestRequireKBPermission_SharedViewCannotEdit(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-shared"] = &kbaccess.KnowledgeBase{ID: "kb-shared", UserID: ptr("owner-99"), IsGlobal: false}
	store.shares["kb-shared:user-shared"] = &kbaccess.KBShare{Permission: "view"}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBPermission("edit")(okHandler)

	req := httptest.NewRequest("PUT", "/kb/kb-shared", nil)
	req.SetPathValue("id", "kb-shared")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "user-shared", Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for view-only user requesting edit, got %d", rec.Code)
	}
}

// Test 8: Unshared non-global KB → 403.
func TestRequireKBPermission_NoShareNoAccess(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-private"] = &kbaccess.KnowledgeBase{ID: "kb-private", UserID: ptr("owner-99"), IsGlobal: false}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireKBPermission("view")(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-private", nil)
	req.SetPathValue("id", "kb-private")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "user-stranger", Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for unshared private KB, got %d", rec.Code)
	}
}

// ---------- RequireAnalyticsAccess tests ----------

// Test: Non-global KB in RequireAnalyticsAccess gets 403.
func TestRequireAnalyticsAccess_NonGlobalKB403(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-1"] = &kbaccess.KnowledgeBase{ID: "kb-1", UserID: ptr("owner-99"), IsGlobal: false}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireAnalyticsAccess(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-1/analytics", nil)
	req.SetPathValue("id", "kb-1")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "admin-1", Role: "admin"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-global KB analytics, got %d", rec.Code)
	}
}

// Test: Global KB + admin role in RequireAnalyticsAccess gets 200.
func TestRequireAnalyticsAccess_GlobalKBAdminGets200(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-global"] = &kbaccess.KnowledgeBase{ID: "kb-global", UserID: ptr("owner-99"), IsGlobal: true}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireAnalyticsAccess(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-global/analytics", nil)
	req.SetPathValue("id", "kb-global")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "admin-1", Role: "admin"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for admin accessing global KB analytics, got %d", rec.Code)
	}
}

// Test: Global KB + regular user in RequireAnalyticsAccess gets 403.
func TestRequireAnalyticsAccess_GlobalKBRegularUser403(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-global"] = &kbaccess.KnowledgeBase{ID: "kb-global", UserID: ptr("owner-99"), IsGlobal: true}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireAnalyticsAccess(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-global/analytics", nil)
	req.SetPathValue("id", "kb-global")
	req = req.WithContext(claimsCtx(req.Context(), auth.Claims{ID: "user-1", Role: "user"}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for regular user accessing analytics, got %d", rec.Code)
	}
}

// Test: No auth in RequireAnalyticsAccess gets 401.
func TestRequireAnalyticsAccess_NoAuth401(t *testing.T) {
	store := newMockStore()
	store.kbs["kb-global"] = &kbaccess.KnowledgeBase{ID: "kb-global", UserID: ptr("owner-99"), IsGlobal: true}

	mw := kbaccess.NewMiddleware(store)
	handler := mw.RequireAnalyticsAccess(okHandler)

	req := httptest.NewRequest("GET", "/kb/kb-global/analytics", nil)
	req.SetPathValue("id", "kb-global")
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
