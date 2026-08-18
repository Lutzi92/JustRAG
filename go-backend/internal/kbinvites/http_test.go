package kbinvites_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbinvites"
)

type mockStore struct {
	links       []kbinvites.Link
	created     *kbinvites.Link
	createRole  string
	createLabel *string
	deleteErr   error
	deleteCall  bool
	redeem      kbinvites.RedeemResult
	redeemErr   error
}

func (m *mockStore) List(context.Context, string) ([]kbinvites.Link, error) {
	return m.links, nil
}

func (m *mockStore) Create(_ context.Context, kbID, token, role string, label *string, _ string) (kbinvites.Link, error) {
	m.createRole, m.createLabel = role, label
	l := kbinvites.Link{ID: "link-1", KBID: kbID, Token: token, Role: role, Label: label}
	m.created = &l
	return l, nil
}

func (m *mockStore) Delete(context.Context, string, string) error {
	m.deleteCall = true
	return m.deleteErr
}

func (m *mockStore) Redeem(context.Context, string, string) (kbinvites.RedeemResult, error) {
	return m.redeem, m.redeemErr
}

var _ kbinvites.Store = (*mockStore)(nil)

type nopAudit struct{}

func (nopAudit) LogAuditAction(context.Context, string, string, string, string, any) error {
	return nil
}

// withUser puts an authenticated user into the request context the way the
// auth middleware does. auth.WithUser takes *auth.Claims (see
// internal/kbmembers/http_test.go:128 for the same call).
func withUser(r *http.Request, id string) *http.Request {
	return r.WithContext(auth.WithUser(r.Context(), &auth.Claims{ID: id, Role: "user"}))
}

func TestCreateLinkRejectsOwnerRole(t *testing.T) {
	store := &mockStore{}
	h := kbinvites.NewHandler(store, nopAudit{})

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/invite-links",
		bytes.NewBufferString(`{"role":"owner"}`))
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.CreateLink(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if store.created != nil {
		t.Fatal("a link was created for role=owner")
	}
}

func TestCreateLinkRejectsOverlongLabel(t *testing.T) {
	store := &mockStore{}
	h := kbinvites.NewHandler(store, nopAudit{})

	body, _ := json.Marshal(map[string]string{
		"role":  "view",
		"label": strings.Repeat("x", kbinvites.MaxLabelLen+1),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/invite-links", bytes.NewBuffer(body))
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.CreateLink(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if store.created != nil {
		t.Fatal("a link was created with an overlong label")
	}
}

func TestCreateLinkMintsToken(t *testing.T) {
	store := &mockStore{}
	h := kbinvites.NewHandler(store, nopAudit{})

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/invite-links",
		bytes.NewBufferString(`{"role":"edit","label":"WS26"}`))
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.CreateLink(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var got kbinvites.Link
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Token) != 43 {
		t.Fatalf("token %q has length %d, want 43", got.Token, len(got.Token))
	}
	if store.createRole != "edit" {
		t.Fatalf("store got role %q, want edit", store.createRole)
	}
}

func TestDeleteLinkNotFound(t *testing.T) {
	store := &mockStore{deleteErr: kbinvites.ErrNotFound}
	h := kbinvites.NewHandler(store, nopAudit{})

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/invite-links/link-9", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("linkId", "link-9")
	rec := httptest.NewRecorder()
	h.DeleteLink(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRedeemUnknownTokenIs404(t *testing.T) {
	store := &mockStore{redeemErr: kbinvites.ErrNotFound}
	h := kbinvites.NewHandler(store, nopAudit{})

	req := httptest.NewRequest(http.MethodPost, "/api/invites/nope/redeem", nil)
	req.SetPathValue("token", "nope")
	rec := httptest.NewRecorder()
	h.Redeem(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRedeemReturnsResult(t *testing.T) {
	store := &mockStore{redeem: kbinvites.RedeemResult{
		KBID: "kb-1", KBName: "Kurs", Role: "edit", AlreadyMember: true,
	}}
	h := kbinvites.NewHandler(store, nopAudit{})

	req := httptest.NewRequest(http.MethodPost, "/api/invites/tok/redeem", nil)
	req.SetPathValue("token", "tok")
	rec := httptest.NewRecorder()
	h.Redeem(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got kbinvites.RedeemResult
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.KBID != "kb-1" || got.Role != "edit" || !got.AlreadyMember {
		t.Fatalf("body = %+v, want kb-1/edit/alreadyMember", got)
	}
}
