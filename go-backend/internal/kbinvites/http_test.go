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
	links        []kbinvites.Link
	created      *kbinvites.Link
	createRole   string
	createLabel  *string
	deleteErr    error
	deleteCall   bool
	deleteKBID   string
	deleteLinkID string
	redeem       kbinvites.RedeemResult
	redeemErr    error
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

func (m *mockStore) Delete(_ context.Context, kbID, linkID string) error {
	m.deleteCall = true
	m.deleteKBID, m.deleteLinkID = kbID, linkID
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

func TestListLinksReturnsEmptyArrayNotNull(t *testing.T) {
	store := &mockStore{} // links is nil
	h := kbinvites.NewHandler(store, nopAudit{})

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/invite-links", nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.ListLinks(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Assert on the raw bytes: json.Marshal(nil slice) produces "null", and
	// decoding into a Go slice would hide that (both null and [] decode to a
	// nil/empty slice), so this must inspect the wire body, not a decoded
	// struct.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	linksRaw, ok := raw["links"]
	if !ok {
		t.Fatal(`response has no "links" field`)
	}
	if string(linksRaw) != "[]" {
		t.Fatalf(`raw "links" value = %s, want []`, linksRaw)
	}
}

func TestListLinksReturnsExistingLinks(t *testing.T) {
	label := "WS26"
	store := &mockStore{links: []kbinvites.Link{
		{ID: "link-1", KBID: "kb-1", Token: "tok-1", Role: "edit", Label: &label},
		{ID: "link-2", KBID: "kb-1", Token: "tok-2", Role: "view"},
	}}
	h := kbinvites.NewHandler(store, nopAudit{})

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/invite-links", nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.ListLinks(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Links []kbinvites.Link `json:"links"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Links) != 2 {
		t.Fatalf("got %d links, want 2", len(got.Links))
	}
	if got.Links[0].ID != "link-1" || got.Links[0].Role != "edit" || got.Links[0].Label == nil || *got.Links[0].Label != "WS26" {
		t.Fatalf("links[0] = %+v, want link-1/edit/WS26", got.Links[0])
	}
	if got.Links[1].ID != "link-2" || got.Links[1].Role != "view" {
		t.Fatalf("links[1] = %+v, want link-2/view", got.Links[1])
	}
}

func TestDeleteLinkSucceeds(t *testing.T) {
	store := &mockStore{}
	h := kbinvites.NewHandler(store, nopAudit{})

	req := httptest.NewRequest(http.MethodDelete, "/api/kb/kb-1/invite-links/link-9", nil)
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("linkId", "link-9")
	rec := httptest.NewRecorder()
	h.DeleteLink(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
	if !store.deleteCall {
		t.Fatal("store.Delete was never called")
	}
	if store.deleteKBID != "kb-1" || store.deleteLinkID != "link-9" {
		t.Fatalf("store.Delete called with (%q, %q), want (kb-1, link-9)", store.deleteKBID, store.deleteLinkID)
	}
}

func TestCreateLinkAcceptsLabelAtMaxLen(t *testing.T) {
	store := &mockStore{}
	h := kbinvites.NewHandler(store, nopAudit{})

	// Multi-byte runes: 100 "ä" is 100 runes but 200 bytes, so this also
	// pins the rune-vs-byte counting behaviour — byte counting would reject
	// this label even though it's exactly at the rune cap.
	label := strings.Repeat("ä", kbinvites.MaxLabelLen)
	body, _ := json.Marshal(map[string]string{
		"role":  "view",
		"label": label,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/invite-links", bytes.NewBuffer(body))
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.CreateLink(rec, withUser(req, "u-1"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if store.created == nil {
		t.Fatal("no link was created for a label at exactly MaxLabelLen runes")
	}
	if store.createLabel == nil || *store.createLabel != label {
		t.Fatalf("store got label %v, want the full %d-rune label", store.createLabel, kbinvites.MaxLabelLen)
	}
}
