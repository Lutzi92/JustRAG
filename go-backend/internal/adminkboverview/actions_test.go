package adminkboverview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
)

// fakeActionStore satisfies ActionStore. Nil kb/owner model "no such row".
type fakeActionStore struct {
	kb    *KBMeta
	owner *OwnerInfo

	kbErr       error
	ownerErr    error
	transferErr error

	transferredKB    string
	transferredOwner string
	transferredPrev  *string
	transferCalls    int

	auditActions []string
}

func (f *fakeActionStore) GetKBMeta(context.Context, string) (*KBMeta, error) {
	return f.kb, f.kbErr
}
func (f *fakeActionStore) GetOwnerInfo(context.Context, string) (*OwnerInfo, error) {
	return f.owner, f.ownerErr
}
func (f *fakeActionStore) TransferKBOwner(_ context.Context, kbID, newOwnerID string, prevOwnerID *string) error {
	f.transferCalls++
	f.transferredKB, f.transferredOwner, f.transferredPrev = kbID, newOwnerID, prevOwnerID
	return f.transferErr
}
func (f *fakeActionStore) LogAuditAction(_ context.Context, _, action, _, _ string, _ any) error {
	f.auditActions = append(f.auditActions, action)
	return nil
}

// fakeDeleter satisfies CascadeDeleter and records which arm was taken.
type fakeDeleter struct {
	deletedKB       string
	deletedGlobalKB string
	err             error
}

func (f *fakeDeleter) DeleteKB(_ context.Context, kbID string) error {
	f.deletedKB = kbID
	return f.err
}
func (f *fakeDeleter) DeleteGlobalKB(_ context.Context, kbID string) error {
	f.deletedGlobalKB = kbID
	return f.err
}

// withSuperadmin attaches claims so operatorID() finds an acting user.
func withSuperadmin(r *http.Request) *http.Request {
	return r.WithContext(auth.WithUser(r.Context(), &auth.Claims{ID: "op-1", Role: "superadmin"}))
}

// deleteRequest builds a DELETE with {id} bound, as ServeMux would.
func deleteRequest(id string) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/kbs/"+id, nil)
	r.SetPathValue("id", id)
	return withSuperadmin(r)
}

const testKBID = "11111111-1111-1111-1111-111111111111"

func TestDeleteKB_PersonalUsesDeleteKB(t *testing.T) {
	store := &fakeActionStore{kb: &KBMeta{ID: testKBID, Name: "Alpha", IsGlobal: false}}
	del := &fakeDeleter{}
	h := NewHandlerWithActions(nil, store, del)

	w := httptest.NewRecorder()
	h.DeleteKB(w, deleteRequest(testKBID))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if del.deletedKB != testKBID {
		t.Errorf("DeleteKB not called with %q (got %q)", testKBID, del.deletedKB)
	}
	if del.deletedGlobalKB != "" {
		t.Errorf("global arm taken for a personal KB")
	}
	if len(store.auditActions) != 1 || store.auditActions[0] != "kb.admin_delete" {
		t.Errorf("audit actions = %v, want [kb.admin_delete]", store.auditActions)
	}
}

func TestDeleteKB_GlobalUsesDeleteGlobalKB(t *testing.T) {
	store := &fakeActionStore{kb: &KBMeta{ID: testKBID, Name: "Global", IsGlobal: true}}
	del := &fakeDeleter{}
	h := NewHandlerWithActions(nil, store, del)

	w := httptest.NewRecorder()
	h.DeleteKB(w, deleteRequest(testKBID))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if del.deletedGlobalKB != testKBID {
		t.Errorf("DeleteGlobalKB not called with %q (got %q)", testKBID, del.deletedGlobalKB)
	}
	if del.deletedKB != "" {
		t.Errorf("personal arm taken for a global KB")
	}
}

func TestDeleteKB_UnknownKBIs404(t *testing.T) {
	store := &fakeActionStore{kb: nil}
	del := &fakeDeleter{}
	h := NewHandlerWithActions(nil, store, del)

	w := httptest.NewRecorder()
	h.DeleteKB(w, deleteRequest(testKBID))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if del.deletedKB != "" || del.deletedGlobalKB != "" {
		t.Errorf("deleter must not be called for an unknown KB")
	}
	if len(store.auditActions) != 0 {
		t.Errorf("no audit entry expected, got %v", store.auditActions)
	}
}

func TestDeleteKB_MalformedIDIs400(t *testing.T) {
	store := &fakeActionStore{kb: &KBMeta{ID: "x", Name: "Alpha"}}
	del := &fakeDeleter{}
	h := NewHandlerWithActions(nil, store, del)

	w := httptest.NewRecorder()
	h.DeleteKB(w, deleteRequest("not-a-uuid"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if del.deletedKB != "" || del.deletedGlobalKB != "" {
		t.Errorf("deleter must not be called for a malformed id")
	}
}

// patchOwnerRequest builds a PATCH with {id} bound and the given JSON body.
func patchOwnerRequest(id, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/kbs/"+id+"/owner", strings.NewReader(body))
	r.SetPathValue("id", id)
	return withSuperadmin(r)
}

const testUserID = "22222222-2222-2222-2222-222222222222"

func TestTransferOwner_Succeeds(t *testing.T) {
	prev := "33333333-3333-3333-3333-333333333333"
	store := &fakeActionStore{
		kb:    &KBMeta{ID: testKBID, Name: "Alpha", IsGlobal: false, OwnerID: &prev},
		owner: &OwnerInfo{ID: testUserID, Username: "ada", DisplayName: "Ada Lovelace"},
	}
	h := NewHandlerWithActions(nil, store, &fakeDeleter{})

	w := httptest.NewRecorder()
	h.TransferOwner(w, patchOwnerRequest(testKBID, `{"userId":"`+testUserID+`"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp TransferOwnerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if resp.OwnerID != testUserID || resp.OwnerUsername != "ada" || resp.OwnerName != "Ada Lovelace" {
		t.Errorf("response = %+v, want the new owner's identity", resp)
	}
	if store.transferredKB != testKBID || store.transferredOwner != testUserID {
		t.Errorf("transfer called with (%q, %q), want (%q, %q)",
			store.transferredKB, store.transferredOwner, testKBID, testUserID)
	}
	if store.transferredPrev == nil || *store.transferredPrev != prev {
		t.Errorf("previous owner = %v, want %q", store.transferredPrev, prev)
	}
	if len(store.auditActions) != 1 || store.auditActions[0] != "kb.owner_change" {
		t.Errorf("audit actions = %v, want [kb.owner_change]", store.auditActions)
	}
}

func TestTransferOwner_GlobalKBIs400(t *testing.T) {
	store := &fakeActionStore{
		kb:    &KBMeta{ID: testKBID, Name: "Global", IsGlobal: true},
		owner: &OwnerInfo{ID: testUserID, Username: "ada", DisplayName: "Ada Lovelace"},
	}
	h := NewHandlerWithActions(nil, store, &fakeDeleter{})

	w := httptest.NewRecorder()
	h.TransferOwner(w, patchOwnerRequest(testKBID, `{"userId":"`+testUserID+`"}`))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if store.transferCalls != 0 {
		t.Errorf("transfer must not run for a global KB")
	}
}

func TestTransferOwner_SameOwnerIs400(t *testing.T) {
	same := testUserID
	store := &fakeActionStore{
		kb:    &KBMeta{ID: testKBID, Name: "Alpha", IsGlobal: false, OwnerID: &same},
		owner: &OwnerInfo{ID: testUserID, Username: "ada", DisplayName: "Ada Lovelace"},
	}
	h := NewHandlerWithActions(nil, store, &fakeDeleter{})

	w := httptest.NewRecorder()
	h.TransferOwner(w, patchOwnerRequest(testKBID, `{"userId":"`+testUserID+`"}`))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if store.transferCalls != 0 {
		t.Errorf("transfer must not run when the user is already the owner")
	}
}

func TestTransferOwner_UnknownKBIs404(t *testing.T) {
	store := &fakeActionStore{kb: nil}
	h := NewHandlerWithActions(nil, store, &fakeDeleter{})

	w := httptest.NewRecorder()
	h.TransferOwner(w, patchOwnerRequest(testKBID, `{"userId":"`+testUserID+`"}`))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestTransferOwner_UnknownUserIs404(t *testing.T) {
	store := &fakeActionStore{
		kb:    &KBMeta{ID: testKBID, Name: "Alpha", IsGlobal: false},
		owner: nil,
	}
	h := NewHandlerWithActions(nil, store, &fakeDeleter{})

	w := httptest.NewRecorder()
	h.TransferOwner(w, patchOwnerRequest(testKBID, `{"userId":"`+testUserID+`"}`))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if store.transferCalls != 0 {
		t.Errorf("transfer must not run for an unknown user")
	}
}

func TestTransferOwner_MissingUserIdIs400(t *testing.T) {
	store := &fakeActionStore{kb: &KBMeta{ID: testKBID, Name: "Alpha"}}
	h := NewHandlerWithActions(nil, store, &fakeDeleter{})

	w := httptest.NewRecorder()
	h.TransferOwner(w, patchOwnerRequest(testKBID, `{}`))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestTransferOwner_ConcurrentlyDeletedKBIs404(t *testing.T) {
	prev := "33333333-3333-3333-3333-333333333333"
	store := &fakeActionStore{
		kb:          &KBMeta{ID: testKBID, Name: "Alpha", IsGlobal: false, OwnerID: &prev},
		owner:       &OwnerInfo{ID: testUserID, Username: "ada", DisplayName: "Ada Lovelace"},
		transferErr: ErrKBNotFound,
	}
	h := NewHandlerWithActions(nil, store, &fakeDeleter{})

	w := httptest.NewRecorder()
	h.TransferOwner(w, patchOwnerRequest(testKBID, `{"userId":"`+testUserID+`"}`))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
	if len(store.auditActions) != 0 {
		t.Errorf("audit actions = %v, want none for a transfer that never happened", store.auditActions)
	}
}

func TestTransferOwner_OwnerlessKBPassesNilPrevious(t *testing.T) {
	store := &fakeActionStore{
		kb:    &KBMeta{ID: testKBID, Name: "Orphan", IsGlobal: false, OwnerID: nil},
		owner: &OwnerInfo{ID: testUserID, Username: "ada", DisplayName: "Ada Lovelace"},
	}
	h := NewHandlerWithActions(nil, store, &fakeDeleter{})

	w := httptest.NewRecorder()
	h.TransferOwner(w, patchOwnerRequest(testKBID, `{"userId":"`+testUserID+`"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if store.transferredPrev != nil {
		t.Errorf("previous owner = %v, want nil for an ownerless KB", store.transferredPrev)
	}
}
