package kb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// recordingUpdateStore captures the KBUpdate the handler hands to the store so
// tests can assert on what would be persisted (e.g. the trimmed name).
type recordingUpdateStore struct {
	mockUpdateStore
	got *kb.KBUpdate
}

func (m *recordingUpdateStore) UpdateKnowledgeBase(_ context.Context, _ string, data kb.KBUpdate) (*kb.KBRow, error) {
	m.got = &data
	return m.kb, m.err
}

func patchKB(t *testing.T, h *kb.UpdateHandler, body map[string]any, kbRow *kbaccess.KnowledgeBase, role, sysRole string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPatch, "/api/kb/"+kbRow.ID, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r = injectKBAccessAs(r, kbRow, role, sysRole)
	w := httptest.NewRecorder()
	h.UpdateKB(w, r)
	return w
}

var (
	privateKB = &kbaccess.KnowledgeBase{ID: "kb-1", IsGlobal: false}
	publicKB  = &kbaccess.KnowledgeBase{ID: "kb-1", IsGlobal: true, IsPublished: true}
)

func TestUpdateKB_Rename_AdminMemberForbidden(t *testing.T) {
	st := &recordingUpdateStore{mockUpdateStore: mockUpdateStore{kb: makeKBRow("kb-1", "New")}}
	h := kb.NewUpdateHandler(st, nil)

	w := patchKB(t, h, map[string]any{"name": "New"}, privateKB, kbaccess.RoleAdmin, auth.RoleUser)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
	if st.got != nil {
		t.Fatalf("store must not be called on a forbidden rename")
	}
}

func TestUpdateKB_Rename_AdminMemberMayStillEditOtherFields(t *testing.T) {
	st := &recordingUpdateStore{mockUpdateStore: mockUpdateStore{kb: makeKBRow("kb-1", "Old")}}
	h := kb.NewUpdateHandler(st, nil)

	w := patchKB(t, h, map[string]any{"description": "d"}, privateKB, kbaccess.RoleAdmin, auth.RoleUser)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestUpdateKB_Rename_OwnerAllowed(t *testing.T) {
	st := &recordingUpdateStore{mockUpdateStore: mockUpdateStore{kb: makeKBRow("kb-1", "New")}}
	h := kb.NewUpdateHandler(st, nil)

	w := patchKB(t, h, map[string]any{"name": "New"}, privateKB, kbaccess.RoleOwner, auth.RoleUser)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestUpdateKB_Rename_PublicKBSystemAdminAllowed(t *testing.T) {
	st := &recordingUpdateStore{mockUpdateStore: mockUpdateStore{kb: makeKBRow("kb-1", "New")}}
	h := kb.NewUpdateHandler(st, nil)

	w := patchKB(t, h, map[string]any{"name": "New"}, publicKB, kbaccess.RoleAdmin, auth.RoleAdmin)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestUpdateKB_Rename_PublicKBMemberAdminForbidden(t *testing.T) {
	st := &recordingUpdateStore{mockUpdateStore: mockUpdateStore{kb: makeKBRow("kb-1", "New")}}
	h := kb.NewUpdateHandler(st, nil)

	w := patchKB(t, h, map[string]any{"name": "New"}, publicKB, kbaccess.RoleAdmin, auth.RoleUser)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestUpdateKB_Rename_EmptyNameRejected(t *testing.T) {
	st := &recordingUpdateStore{mockUpdateStore: mockUpdateStore{kb: makeKBRow("kb-1", "x")}}
	h := kb.NewUpdateHandler(st, nil)

	w := patchKB(t, h, map[string]any{"name": "   "}, privateKB, kbaccess.RoleOwner, auth.RoleUser)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
	}
	if st.got != nil {
		t.Fatalf("store must not be called for an empty name")
	}
}

func TestUpdateKB_Rename_NameIsTrimmed(t *testing.T) {
	st := &recordingUpdateStore{mockUpdateStore: mockUpdateStore{kb: makeKBRow("kb-1", "New name")}}
	h := kb.NewUpdateHandler(st, nil)

	w := patchKB(t, h, map[string]any{"name": "  New name \n"}, privateKB, kbaccess.RoleOwner, auth.RoleUser)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	if st.got == nil || st.got.Name == nil || *st.got.Name != "New name" {
		t.Fatalf("expected trimmed name %q handed to store, got %+v", "New name", st.got)
	}
}
