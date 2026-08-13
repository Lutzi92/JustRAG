package kbsubs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbsubs"
)

type fakeStore struct {
	gotKBID, gotUserID, gotState string
	calls                        int
}

func (f *fakeStore) SetState(_ context.Context, kbID, userID, state string) error {
	f.gotKBID, f.gotUserID, f.gotState = kbID, userID, state
	f.calls++
	return nil
}

// withAccess builds a request carrying both an authenticated user and the
// KBAccessResult that kbViewChain would have stored.
func withAccess(t *testing.T, method, kbID string, isGlobal, isPublished bool) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/api/kb/"+kbID+"/subscription", nil)
	req.SetPathValue("id", kbID)
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: "user-1", Role: auth.RoleUser})
	ctx = kbaccess.WithAccess(ctx, &kbaccess.KBAccessResult{
		KB:   &kbaccess.KnowledgeBase{ID: kbID, IsGlobal: isGlobal, IsPublished: isPublished},
		Role: kbaccess.RoleView,
	})
	return req.WithContext(ctx)
}

func TestSubscribeWritesSubscribedState(t *testing.T) {
	store := &fakeStore{}
	h := kbsubs.NewHandler(store)

	rec := httptest.NewRecorder()
	h.Subscribe(rec, withAccess(t, http.MethodPut, "kb-1", true, true))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotState != kbsubs.StateSubscribed || store.gotKBID != "kb-1" || store.gotUserID != "user-1" {
		t.Fatalf("store got (%q,%q,%q), want (kb-1,user-1,subscribed)",
			store.gotKBID, store.gotUserID, store.gotState)
	}
}

func TestUnsubscribeWritesOptedOutState(t *testing.T) {
	store := &fakeStore{}
	h := kbsubs.NewHandler(store)

	rec := httptest.NewRecorder()
	h.Unsubscribe(rec, withAccess(t, http.MethodDelete, "kb-1", true, true))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if store.gotState != kbsubs.StateOptedOut {
		t.Fatalf("state = %q, want opted_out", store.gotState)
	}
}

// A private KB has no subscription concept — membership governs it. Writing a
// row would be dead data that the overview query never reads.
func TestSubscribeOnPrivateKBIsRejected(t *testing.T) {
	store := &fakeStore{}
	h := kbsubs.NewHandler(store)

	rec := httptest.NewRecorder()
	h.Subscribe(rec, withAccess(t, http.MethodPut, "kb-1", false, false))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times, want 0", store.calls)
	}
}

func TestSubscribeOnUnpublishedKBIsRejected(t *testing.T) {
	store := &fakeStore{}
	h := kbsubs.NewHandler(store)

	rec := httptest.NewRecorder()
	h.Subscribe(rec, withAccess(t, http.MethodPut, "kb-1", true, false))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times, want 0", store.calls)
	}
}
