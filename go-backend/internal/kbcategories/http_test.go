package kbcategories_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/kbcategories"
)

type fakeStore struct {
	categories  []kbcategories.Category
	createErr   error
	gotName     string
	gotKBID     string
	gotCatIDs   []string
	deleteCalls []string
}

func (f *fakeStore) List(context.Context) ([]kbcategories.Category, error) {
	return f.categories, nil
}

func (f *fakeStore) Create(_ context.Context, name string, sortOrder int) (*kbcategories.Category, error) {
	f.gotName = name
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &kbcategories.Category{ID: "cat-new", Name: name, SortOrder: sortOrder}, nil
}

func (f *fakeStore) Update(_ context.Context, id, name string, sortOrder int) (*kbcategories.Category, error) {
	return &kbcategories.Category{ID: id, Name: name, SortOrder: sortOrder}, nil
}

func (f *fakeStore) Delete(_ context.Context, id string) error {
	f.deleteCalls = append(f.deleteCalls, id)
	return nil
}

func (f *fakeStore) SetKBCategories(_ context.Context, kbID string, categoryIDs []string) error {
	f.gotKBID, f.gotCatIDs = kbID, categoryIDs
	return nil
}

func (f *fakeStore) ListKBCategories(context.Context, string) ([]kbcategories.Category, error) {
	return f.categories, nil
}

func TestListCategoriesReturnsJSON(t *testing.T) {
	store := &fakeStore{categories: []kbcategories.Category{{ID: "c1", Name: "IT-Sicherheit", SortOrder: 1}}}
	h := kbcategories.NewHandler(store)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/admin/kb-categories", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []kbcategories.Category
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "IT-Sicherheit" {
		t.Fatalf("got %+v", got)
	}
}

func TestCreateRejectsEmptyName(t *testing.T) {
	h := kbcategories.NewHandler(&fakeStore{})
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/admin/kb-categories",
		strings.NewReader(`{"name":"   "}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCreateTrimsName(t *testing.T) {
	store := &fakeStore{}
	h := kbcategories.NewHandler(store)
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/admin/kb-categories",
		strings.NewReader(`{"name":"  Recht  ","sortOrder":2}`)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotName != "Recht" {
		t.Fatalf("name = %q, want %q", store.gotName, "Recht")
	}
}

func TestCreateDuplicateReturns409(t *testing.T) {
	h := kbcategories.NewHandler(&fakeStore{createErr: kbcategories.ErrDuplicateName})
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/admin/kb-categories",
		strings.NewReader(`{"name":"Recht"}`)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestSetKBCategoriesPassesIDs(t *testing.T) {
	store := &fakeStore{}
	h := kbcategories.NewHandler(store)

	req := httptest.NewRequest(http.MethodPut, "/api/kb/kb-1/categories",
		strings.NewReader(`{"categoryIds":["c1","c2"]}`))
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.SetKBCategories(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body)
	}
	if store.gotKBID != "kb-1" || len(store.gotCatIDs) != 2 {
		t.Fatalf("store got (%q, %v)", store.gotKBID, store.gotCatIDs)
	}
}
