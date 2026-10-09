// Unit tests for the kbfilters HTTP surface.
//
// Oracle: the status contract fixed in the feature's spec before any of this
// code existed — PUT/DELETE favorite 204, list 200 with [] never null, create
// 201, duplicate name or category limit 409, unusable name or out-of-range
// sortOrder 400, unknown-or-someone-else's category 404, delete 204 — plus
// the external limits the validation guards: Postgres's int4 range for
// sort_order and its refusal of NUL in text. It is independent of the code
// under test because the expected codes come from that spec and those limits,
// and the store here is a fake whose return values the test chooses: nothing
// in this file is derived from what the handler happens to do.
//
// What these tests deliberately do NOT prove: that a cross-user assignment is
// actually refused. That rule lives in the schema, and asserting it against a
// fake store would only re-assert the fake. See
// store_pg_integration_test.go, which asserts the rejection itself.

package kbfilters_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbfilters"
)

// fakeStore records its arguments and replays a canned outcome.
type fakeStore struct {
	calls int

	gotUserID, gotKBID, gotCatID string
	gotName                      string
	gotSortOrder                 int32

	categories []kbfilters.UserCategory
	created    *kbfilters.UserCategory
	err        error
}

func (f *fakeStore) AddFavorite(_ context.Context, userID, kbID string) error {
	f.calls++
	f.gotUserID, f.gotKBID = userID, kbID
	return f.err
}

func (f *fakeStore) RemoveFavorite(_ context.Context, userID, kbID string) error {
	f.calls++
	f.gotUserID, f.gotKBID = userID, kbID
	return f.err
}

func (f *fakeStore) ListCategories(_ context.Context, userID string) ([]kbfilters.UserCategory, error) {
	f.calls++
	f.gotUserID = userID
	return f.categories, f.err
}

func (f *fakeStore) CreateCategory(_ context.Context, userID, name string, sortOrder int32) (*kbfilters.UserCategory, error) {
	f.calls++
	f.gotUserID, f.gotName, f.gotSortOrder = userID, name, sortOrder
	return f.created, f.err
}

func (f *fakeStore) UpdateCategory(_ context.Context, userID, catID, name string, sortOrder int32) (*kbfilters.UserCategory, error) {
	f.calls++
	f.gotUserID, f.gotCatID, f.gotName, f.gotSortOrder = userID, catID, name, sortOrder
	return f.created, f.err
}

func (f *fakeStore) DeleteCategory(_ context.Context, userID, catID string) error {
	f.calls++
	f.gotUserID, f.gotCatID = userID, catID
	return f.err
}

func (f *fakeStore) AssignCategory(_ context.Context, userID, catID, kbID string) error {
	f.calls++
	f.gotUserID, f.gotCatID, f.gotKBID = userID, catID, kbID
	return f.err
}

func (f *fakeStore) UnassignCategory(_ context.Context, userID, catID, kbID string) error {
	f.calls++
	f.gotUserID, f.gotCatID, f.gotKBID = userID, catID, kbID
	return f.err
}

var _ kbfilters.Store = (*fakeStore)(nil)

const (
	testUserID = "user-1"
	testKBID   = "kb-1"
)

// authed returns a request carrying an authenticated user and nothing else —
// the state the authenticate middleware leaves behind for the
// /api/kb-user-categories routes.
func authed(method, target, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	return req.WithContext(auth.WithUser(req.Context(),
		&auth.Claims{ID: testUserID, Role: auth.RoleUser}))
}

// withAccess additionally attaches the KBAccessResult that kbViewChain would
// have stored, which is where the handler reads the KB id from.
func withAccess(req *http.Request) *http.Request {
	ctx := kbaccess.WithAccess(req.Context(), &kbaccess.KBAccessResult{
		KB:   &kbaccess.KnowledgeBase{ID: testKBID, IsGlobal: true, IsPublished: true},
		Role: kbaccess.RoleView,
	})
	return req.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Favorites
// ---------------------------------------------------------------------------

func TestAddFavoriteWritesTheAccessResultsKBID(t *testing.T) {
	store := &fakeStore{}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.AddFavorite(rec, withAccess(authed(http.MethodPut, "/api/kb/"+testKBID+"/favorite", "")))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotUserID != testUserID || store.gotKBID != testKBID {
		t.Fatalf("store got (%q,%q), want (%q,%q)",
			store.gotUserID, store.gotKBID, testUserID, testKBID)
	}
}

func TestRemoveFavoriteReturns204(t *testing.T) {
	store := &fakeStore{}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.RemoveFavorite(rec, withAccess(authed(http.MethodDelete, "/api/kb/"+testKBID+"/favorite", "")))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
}

// Reaching the handler without a KBAccessResult means the route was wired
// without kbViewChain. That is a wiring bug, not a client error, so it must
// not be answered with a success code.
func TestFavoriteWithoutAccessResultIs500(t *testing.T) {
	store := &fakeStore{}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.AddFavorite(rec, authed(http.MethodPut, "/api/kb/"+testKBID+"/favorite", ""))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times, want 0", store.calls)
	}
}

func TestFavoriteUnauthenticatedIs401(t *testing.T) {
	store := &fakeStore{}
	h := kbfilters.NewHandler(store)

	req := httptest.NewRequest(http.MethodPut, "/api/kb/"+testKBID+"/favorite", nil)
	rec := httptest.NewRecorder()
	h.AddFavorite(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times, want 0", store.calls)
	}
}

// ---------------------------------------------------------------------------
// Categories
// ---------------------------------------------------------------------------

// An empty list must serialise as [] and not null: the chip row maps over it.
func TestListCategoriesEmptyReturnsArray(t *testing.T) {
	h := kbfilters.NewHandler(&fakeStore{})
	rec := httptest.NewRecorder()
	h.ListCategories(rec, authed(http.MethodGet, "/api/kb-user-categories", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("body = %q, want []", body)
	}
}

func TestListCategoriesReturnsTheCallersRows(t *testing.T) {
	store := &fakeStore{categories: []kbfilters.UserCategory{
		{ID: "cat-1", Name: "Studium", SortOrder: 1},
	}}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.ListCategories(rec, authed(http.MethodGet, "/api/kb-user-categories", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotUserID != testUserID {
		t.Fatalf("store got user %q, want %q", store.gotUserID, testUserID)
	}
	var got []kbfilters.UserCategory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Studium" || got[0].SortOrder != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestCreateCategoryReturns201AndTrimsTheName(t *testing.T) {
	store := &fakeStore{created: &kbfilters.UserCategory{ID: "cat-1", Name: "Studium", SortOrder: 2}}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.CreateCategory(rec, authed(http.MethodPost, "/api/kb-user-categories",
		`{"name":"  Studium  ","sortOrder":2}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotName != "Studium" {
		t.Fatalf("name = %q, want the trimmed %q", store.gotName, "Studium")
	}
	if store.gotSortOrder != 2 {
		t.Fatalf("sortOrder = %d, want 2", store.gotSortOrder)
	}
	var got kbfilters.UserCategory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "cat-1" {
		t.Fatalf("body = %+v, want the created category", got)
	}
}

// A blank or whitespace-only chip label is unusable in the filter row, and a
// name longer than the column would be a 500 from SQLSTATE 22001. Both are
// client errors, and neither may reach the store.
func TestCreateCategoryRejectsUnusableNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty", `{"name":""}`},
		{"whitespace only", `{"name":"   "}`},
		{"over 100 runes", `{"name":"` + strings.Repeat("ä", 101) + `"}`},
		{"not JSON", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			h := kbfilters.NewHandler(store)

			rec := httptest.NewRecorder()
			h.CreateCategory(rec, authed(http.MethodPost, "/api/kb-user-categories", tc.body))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body)
			}
			if store.calls != 0 {
				t.Fatalf("store called %d times, want 0", store.calls)
			}
		})
	}
}

// Names that pass a plain TrimSpace but are still unusable. NUL is the one
// that matters most: Postgres refuses it in text with SQLSTATE 22021, so it
// would otherwise be a 500. The format-only cases (zero-width space, word
// joiner, a bidi mark) render as nothing, and strings.TrimSpace keeps them —
// the expectation that they are refused comes from the Unicode category (Cf),
// not from what the handler happens to strip.
func TestCreateCategoryRejectsControlAndInvisibleNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"NUL", `{"name":"Stu\u0000dium"}`},
		{"newline", `{"name":"Stu\ndium"}`},
		{"tab inside", `{"name":"Stu\tdium"}`},
		{"C1 control", `{"name":"Stu\u0085dium"}`},
		{"DEL", `{"name":"Stu\u007fdium"}`},
		{"zero-width space only", `{"name":"\u200b"}`},
		{"format chars and spaces only", `{"name":" \u200b\u2060\u200e "}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			h := kbfilters.NewHandler(store)

			rec := httptest.NewRecorder()
			h.CreateCategory(rec, authed(http.MethodPost, "/api/kb-user-categories", tc.body))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body)
			}
			if store.calls != 0 {
				t.Fatalf("store called %d times, want 0", store.calls)
			}
		})
	}
}

// A visible name may still carry a format character (a ZWJ inside an emoji
// sequence, say); only a name made of nothing else is refused.
func TestCreateCategoryKeepsAVisibleNameWithAFormatCharacter(t *testing.T) {
	store := &fakeStore{created: &kbfilters.UserCategory{ID: "cat-1"}}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.CreateCategory(rec, authed(http.MethodPost, "/api/kb-user-categories",
		`{"name":"Lern\u200bgruppe"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotName != "Lern\u200bgruppe" {
		t.Fatalf("name = %q, want it stored unchanged", store.gotName)
	}
}

// sort_order is a Postgres int (int4). The boundaries are int4's, written out
// here rather than taken from the math package the handler uses.
func TestCategorySortOrderMustFitInt32(t *testing.T) {
	for _, tc := range []struct {
		sortOrder string
		want      int
	}{
		{"2147483647", http.StatusCreated},
		{"-2147483648", http.StatusCreated},
		{"2147483648", http.StatusBadRequest},
		{"-2147483649", http.StatusBadRequest},
		{"9223372036854775808", http.StatusBadRequest}, // overflows int64: decode error
		{"1.5", http.StatusBadRequest},
	} {
		t.Run(tc.sortOrder, func(t *testing.T) {
			store := &fakeStore{created: &kbfilters.UserCategory{ID: "cat-1"}}
			h := kbfilters.NewHandler(store)

			rec := httptest.NewRecorder()
			h.CreateCategory(rec, authed(http.MethodPost, "/api/kb-user-categories",
				`{"name":"Studium","sortOrder":`+tc.sortOrder+`}`))

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusBadRequest && store.calls != 0 {
				t.Fatalf("store called %d times for a rejected sortOrder, want 0", store.calls)
			}
		})
	}
}

// The update route shares the body validation; one out-of-range value pins
// that it is wired there too.
func TestUpdateCategoryRejectsOutOfRangeSortOrder(t *testing.T) {
	catID := uuid.NewString()
	store := &fakeStore{}
	h := kbfilters.NewHandler(store)

	req := authed(http.MethodPatch, "/api/kb-user-categories/"+catID, `{"name":"Studium","sortOrder":2147483648}`)
	req.SetPathValue("catId", catID)
	rec := httptest.NewRecorder()
	h.UpdateCategory(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times, want 0", store.calls)
	}
}

func TestCreateCategoryLimitIs409(t *testing.T) {
	store := &fakeStore{err: kbfilters.ErrCategoryLimit}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.CreateCategory(rec, authed(http.MethodPost, "/api/kb-user-categories", `{"name":"Studium"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
}

// Exactly 100 runes is the column width, not one over it.
func TestCreateCategoryAcceptsExactlyTheColumnWidth(t *testing.T) {
	name := strings.Repeat("ä", 100)
	store := &fakeStore{created: &kbfilters.UserCategory{ID: "cat-1", Name: name}}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.CreateCategory(rec, authed(http.MethodPost, "/api/kb-user-categories",
		`{"name":"`+name+`"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
}

func TestCreateCategoryDuplicateNameIs409(t *testing.T) {
	store := &fakeStore{err: kbfilters.ErrDuplicateName}
	h := kbfilters.NewHandler(store)

	rec := httptest.NewRecorder()
	h.CreateCategory(rec, authed(http.MethodPost, "/api/kb-user-categories", `{"name":"Studium"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestUpdateCategoryReturns200(t *testing.T) {
	catID := uuid.NewString()
	store := &fakeStore{created: &kbfilters.UserCategory{ID: catID, Name: "Freizeit", SortOrder: 3}}
	h := kbfilters.NewHandler(store)

	req := authed(http.MethodPatch, "/api/kb-user-categories/"+catID, `{"name":"Freizeit","sortOrder":3}`)
	req.SetPathValue("catId", catID)
	rec := httptest.NewRecorder()
	h.UpdateCategory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotCatID != catID {
		t.Fatalf("catID = %q, want %q", store.gotCatID, catID)
	}
}

// ErrNotFound covers both "no such id" and "somebody else's id" — the store
// never distinguishes them, so the status code cannot be used to enumerate
// other users' category ids.
func TestUpdateCategoryNotFoundIs404(t *testing.T) {
	catID := uuid.NewString()
	store := &fakeStore{err: kbfilters.ErrNotFound}
	h := kbfilters.NewHandler(store)

	req := authed(http.MethodPatch, "/api/kb-user-categories/"+catID, `{"name":"Freizeit"}`)
	req.SetPathValue("catId", catID)
	rec := httptest.NewRecorder()
	h.UpdateCategory(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteCategoryReturns204AndNotFoundIs404(t *testing.T) {
	catID := uuid.NewString()

	store := &fakeStore{}
	h := kbfilters.NewHandler(store)
	req := authed(http.MethodDelete, "/api/kb-user-categories/"+catID, "")
	req.SetPathValue("catId", catID)
	rec := httptest.NewRecorder()
	h.DeleteCategory(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}

	missing := &fakeStore{err: kbfilters.ErrNotFound}
	h = kbfilters.NewHandler(missing)
	req = authed(http.MethodDelete, "/api/kb-user-categories/"+catID, "")
	req.SetPathValue("catId", catID)
	rec = httptest.NewRecorder()
	h.DeleteCategory(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// A non-UUID id in the path would otherwise reach `$1::uuid` and surface as a
// 500. It is indistinguishable from an id that does not exist, so it gets the
// same 404 — and must never reach the store.
func TestNonUUIDCategoryIDIs404(t *testing.T) {
	store := &fakeStore{}
	h := kbfilters.NewHandler(store)

	req := authed(http.MethodDelete, "/api/kb-user-categories/not-a-uuid", "")
	req.SetPathValue("catId", "not-a-uuid")
	rec := httptest.NewRecorder()
	h.DeleteCategory(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times, want 0", store.calls)
	}
}

// uuid.Parse accepts spellings Postgres's ::uuid cast does not — a
// "urn:uuid:" prefix among them — so the handler must hand the store the
// canonical form, never the raw path value. The expected string is the
// RFC 4122 text form of the same id, written by hand.
func TestCategoryPathIDIsPassedOnCanonically(t *testing.T) {
	const canonical = "0f8fad5b-d9cb-469f-a165-70867728950e"
	for _, raw := range []string{
		"urn:uuid:" + canonical,
		"{" + canonical + "}",
		"0F8FAD5BD9CB469FA16570867728950E",
	} {
		t.Run(raw, func(t *testing.T) {
			store := &fakeStore{}
			h := kbfilters.NewHandler(store)

			req := authed(http.MethodDelete, "/api/kb-user-categories/x", "")
			req.SetPathValue("catId", raw)
			rec := httptest.NewRecorder()
			h.DeleteCategory(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
			}
			if store.gotCatID != canonical {
				t.Fatalf("store got catID %q, want the canonical %q", store.gotCatID, canonical)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Assignments
// ---------------------------------------------------------------------------

func TestAssignCategoryReturns204(t *testing.T) {
	catID := uuid.NewString()
	store := &fakeStore{}
	h := kbfilters.NewHandler(store)

	req := withAccess(authed(http.MethodPut, "/api/kb/"+testKBID+"/user-categories/"+catID, ""))
	req.SetPathValue("catId", catID)
	rec := httptest.NewRecorder()
	h.AssignCategory(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotCatID != catID || store.gotKBID != testKBID || store.gotUserID != testUserID {
		t.Fatalf("store got (%q,%q,%q)", store.gotUserID, store.gotCatID, store.gotKBID)
	}
}

// The store maps the composite FK's rejection (SQLSTATE 23503, i.e. the
// category belongs to another user) to ErrNotFound. This pins that the
// handler turns it into 404 rather than a 500 — the DB rejection is the
// mechanism, but the client must still see a clean status.
func TestAssignForeignCategoryIs404(t *testing.T) {
	catID := uuid.NewString()
	store := &fakeStore{err: kbfilters.ErrNotFound}
	h := kbfilters.NewHandler(store)

	req := withAccess(authed(http.MethodPut, "/api/kb/"+testKBID+"/user-categories/"+catID, ""))
	req.SetPathValue("catId", catID)
	rec := httptest.NewRecorder()
	h.AssignCategory(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body)
	}
}

func TestUnassignCategoryReturns204(t *testing.T) {
	catID := uuid.NewString()
	store := &fakeStore{}
	h := kbfilters.NewHandler(store)

	req := withAccess(authed(http.MethodDelete, "/api/kb/"+testKBID+"/user-categories/"+catID, ""))
	req.SetPathValue("catId", catID)
	rec := httptest.NewRecorder()
	h.UnassignCategory(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
}
