package files

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/userfiles"
)

type fakeAdopter struct {
	calls  int
	kbID   string
	caller string
	ids    []string
	err    error
}

func (f *fakeAdopter) Adopt(_ context.Context, kbID, caller string, ids []string) (*userfiles.AdoptResult, error) {
	f.calls++
	f.kbID, f.caller, f.ids = kbID, caller, ids
	if f.err != nil {
		return nil, f.err
	}
	return &userfiles.AdoptResult{
		Adopted: []userfiles.AdoptedFile{{FileID: ids[0], UserFileID: "uf"}},
		Skipped: []userfiles.AdoptSkipped{},
	}, nil
}

func adoptReq(body, sysRole string, access *kbaccess.KBAccessResult) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/files/adopt", strings.NewReader(body))
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: "u1", Role: sysRole})
	if access != nil {
		ctx = kbaccess.WithAccess(ctx, access)
	}
	return req.WithContext(ctx)
}

func acc(global bool, role string) *kbaccess.KBAccessResult {
	return &kbaccess.KBAccessResult{KB: &kbaccess.KnowledgeBase{ID: "kb1", IsGlobal: global}, Role: role}
}

func TestAdoptLegacyAuthorization(t *testing.T) {
	cases := []struct {
		name    string
		sysRole string
		access  *kbaccess.KBAccessResult
		want    int
	}{
		{"private owner", auth.RoleUser, acc(false, kbaccess.RoleOwner), 200},
		{"private superadmin resolves to owner", auth.RoleSuperAdmin, acc(false, kbaccess.RoleOwner), 200},
		{"private kb admin non-owner", auth.RoleUser, acc(false, kbaccess.RoleAdmin), 403},
		{"private system admin without owner role", auth.RoleAdmin, acc(false, kbaccess.RoleAdmin), 403},
		{"public system admin", auth.RoleAdmin, acc(true, kbaccess.RoleAdmin), 200},
		{"public superadmin", auth.RoleSuperAdmin, acc(true, kbaccess.RoleOwner), 200},
		{"public kb admin non-system", auth.RoleUser, acc(true, kbaccess.RoleAdmin), 403},
		{"public plain user", auth.RoleUser, acc(true, kbaccess.RoleView), 403},
		{"no access in context", auth.RoleAdmin, nil, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ad := &fakeAdopter{}
			h := &Handler{adopter: ad}
			rec := httptest.NewRecorder()
			h.AdoptLegacy(rec, adoptReq(`{"fileIds":["a"]}`, c.sysRole, c.access))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, c.want, rec.Body)
			}
			if (c.want == 200) != (ad.calls == 1) {
				t.Errorf("adopter calls = %d", ad.calls)
			}
			if c.want == 200 && (ad.kbID != "kb1" || ad.caller != "u1") {
				t.Errorf("adopter args kb=%q caller=%q", ad.kbID, ad.caller)
			}
		})
	}
}

func TestAdoptLegacyValidationAndErrors(t *testing.T) {
	owner := acc(false, kbaccess.RoleOwner)
	many := `{"fileIds":["` + strings.Repeat(`a","`, 100) + `a"]}`
	for name, body := range map[string]string{"empty": `{"fileIds":[]}`, "missing": `{}`, "bad json": `x`, "101 ids": many} {
		t.Run(name, func(t *testing.T) {
			ad := &fakeAdopter{}
			h := &Handler{adopter: ad}
			rec := httptest.NewRecorder()
			h.AdoptLegacy(rec, adoptReq(body, auth.RoleUser, owner))
			if rec.Code != 400 || ad.calls != 0 || !strings.Contains(rec.Body.String(), "fileIds must contain 1-100 ids") {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, ad.calls, rec.Body)
			}
		})
	}
	t.Run("adopter error", func(t *testing.T) {
		h := &Handler{adopter: &fakeAdopter{err: errors.New("boom")}}
		rec := httptest.NewRecorder()
		h.AdoptLegacy(rec, adoptReq(`{"fileIds":["a"]}`, auth.RoleUser, owner))
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
	})
	t.Run("not wired", func(t *testing.T) {
		rec := httptest.NewRecorder()
		(&Handler{}).AdoptLegacy(rec, adoptReq(`{"fileIds":["a"]}`, auth.RoleUser, owner))
		if rec.Code != 404 {
			t.Fatalf("status=%d", rec.Code)
		}
	})
	t.Run("success body shape", func(t *testing.T) {
		h := &Handler{adopter: &fakeAdopter{}}
		rec := httptest.NewRecorder()
		h.AdoptLegacy(rec, adoptReq(`{"fileIds":["a"]}`, auth.RoleUser, owner))
		if got := rec.Body.String(); !strings.Contains(got, `"adopted":[{"fileId":"a","userFileId":"uf"}]`) || !strings.Contains(got, `"skipped":[]`) {
			t.Errorf("body = %s", got)
		}
	})
}
