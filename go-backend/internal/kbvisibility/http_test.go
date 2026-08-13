package kbvisibility_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbvisibility"
)

// fakeStore records calls and returns canned errors.
type fakeStore struct {
	publishErr   error
	unpublishErr error
	impact       kbvisibility.Impact
	gotKBID      string
	gotOwnerID   string
}

func (f *fakeStore) Publish(_ context.Context, kbID string) error {
	f.gotKBID = kbID
	return f.publishErr
}

func (f *fakeStore) Unpublish(_ context.Context, kbID, newOwnerID string) error {
	f.gotKBID, f.gotOwnerID = kbID, newOwnerID
	return f.unpublishErr
}

func (f *fakeStore) UnpublishImpact(_ context.Context, kbID string) (kbvisibility.Impact, error) {
	f.gotKBID = kbID
	return f.impact, nil
}

// noopAudit satisfies the handler's audit dependency without a database.
type noopAudit struct{ actions []string }

func (n *noopAudit) LogAuditAction(_ context.Context, _, action, _, _ string, _ any) error {
	n.actions = append(n.actions, action)
	return nil
}

func request(t *testing.T, h http.HandlerFunc, method, target, body, kbID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.SetPathValue("id", kbID)
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: "operator-1", Role: auth.RoleAdmin})
	rec := httptest.NewRecorder()
	h(rec, req.WithContext(ctx))
	return rec
}

func TestPublishReturns204AndAudits(t *testing.T) {
	store := &fakeStore{}
	audit := &noopAudit{}
	h := kbvisibility.NewHandler(store, audit)

	rec := request(t, h.Publish, http.MethodPost, "/api/admin/kb/kb-1/publish", "", "kb-1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotKBID != "kb-1" {
		t.Fatalf("store got kbID %q, want kb-1", store.gotKBID)
	}
	if len(audit.actions) != 1 || audit.actions[0] != "kb_publish" {
		t.Fatalf("audit actions = %v, want [kb_publish]", audit.actions)
	}
}

func TestPublishAlreadyPublicReturns409(t *testing.T) {
	h := kbvisibility.NewHandler(&fakeStore{publishErr: kbvisibility.ErrAlreadyPublic}, &noopAudit{})
	rec := request(t, h.Publish, http.MethodPost, "/api/admin/kb/kb-1/publish", "", "kb-1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestUnpublishRequiresNewOwner(t *testing.T) {
	h := kbvisibility.NewHandler(&fakeStore{}, &noopAudit{})
	rec := request(t, h.Unpublish, http.MethodPost, "/api/admin/kb/kb-1/unpublish", `{}`, "kb-1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUnpublishPassesNewOwner(t *testing.T) {
	store := &fakeStore{}
	h := kbvisibility.NewHandler(store, &noopAudit{})
	rec := request(t, h.Unpublish, http.MethodPost,
		"/api/admin/kb/kb-1/unpublish", `{"newOwnerId":"user-7"}`, "kb-1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotOwnerID != "user-7" {
		t.Fatalf("store got ownerID %q, want user-7", store.gotOwnerID)
	}
}

func TestUnpublishImpactReturnsJSON(t *testing.T) {
	store := &fakeStore{impact: kbvisibility.Impact{
		Subscribers: 3,
		Candidates:  []kbvisibility.Candidate{{UserID: "u1", Username: "alice"}},
	}}
	h := kbvisibility.NewHandler(store, &noopAudit{})
	rec := request(t, h.UnpublishImpact, http.MethodGet,
		"/api/admin/kb/kb-1/unpublish-impact", "", "kb-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got kbvisibility.Impact
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Subscribers != 3 || len(got.Candidates) != 1 {
		t.Fatalf("got %+v, want 3 subscribers and 1 candidate", got)
	}
}
