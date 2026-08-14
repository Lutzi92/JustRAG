package kbsubs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbsubs"
)

type fakeStore struct {
	gotKBID, gotUserID, gotState string
	calls                        int

	catalog        []kbsubs.CatalogEntry
	gotQuery       string
	gotCategoryIDs []string
}

func (f *fakeStore) SetState(_ context.Context, kbID, userID, state string) error {
	f.gotKBID, f.gotUserID, f.gotState = kbID, userID, state
	f.calls++
	return nil
}

func (f *fakeStore) Catalog(_ context.Context, userID, query string, categoryIDs []string) ([]kbsubs.CatalogEntry, error) {
	f.gotUserID, f.gotQuery, f.gotCategoryIDs = userID, query, categoryIDs
	return f.catalog, nil
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

func TestCatalogPassesFiltersAndReturnsJSON(t *testing.T) {
	store := &fakeStore{catalog: []kbsubs.CatalogEntry{
		{ID: "kb-1", Name: "IT-Handbuch", Subscribed: true},
	}}
	h := kbsubs.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/kb/catalog?q=hand&category=c1&category=c2", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: "user-1", Role: auth.RoleUser}))
	rec := httptest.NewRecorder()
	h.Catalog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotQuery != "hand" {
		t.Fatalf("query = %q, want hand", store.gotQuery)
	}
	if len(store.gotCategoryIDs) != 2 {
		t.Fatalf("categoryIds = %v, want two", store.gotCategoryIDs)
	}
	var got []kbsubs.CatalogEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || !got[0].Subscribed {
		t.Fatalf("got %+v", got)
	}
}

// An empty catalog must serialise as [] and not null: the frontend maps over
// it directly.
func TestCatalogEmptyReturnsArray(t *testing.T) {
	h := kbsubs.NewHandler(&fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/kb/catalog", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: "user-1", Role: auth.RoleUser}))
	rec := httptest.NewRecorder()
	h.Catalog(rec, req)

	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("body = %q, want []", body)
	}
}

// A staged public KB (public, not yet published) does accept subscription
// writes. Only a member reaches the view-gated route for one, and their
// opt-out row is what takes the tile out of Favoriten — the star writes
// nothing else, so rejecting this would make a staged KB impossible to
// un-favorite. Access itself never depended on the row.
func TestSubscribeOnStagedPublicKBIsAllowed(t *testing.T) {
	store := &fakeStore{}
	h := kbsubs.NewHandler(store)

	rec := httptest.NewRecorder()
	h.Unsubscribe(rec, withAccess(t, http.MethodDelete, "kb-1", true, false))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotState != kbsubs.StateOptedOut {
		t.Fatalf("state = %q, want %q", store.gotState, kbsubs.StateOptedOut)
	}
}
