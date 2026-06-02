package kbconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeStore implements the handler's store dependency.
type fakeStore struct {
	overrides map[string]*string
	upserted  map[string]*string
	deleted   []string
}

func (f *fakeStore) ListKBOverrides(_ context.Context, _ string) (map[string]*string, error) {
	return f.overrides, nil
}
func (f *fakeStore) UpsertBatch(_ context.Context, _ string, kv map[string]*string) error {
	f.upserted = kv
	return nil
}
func (f *fakeStore) DeleteKey(_ context.Context, _, key string) (bool, error) {
	f.deleted = append(f.deleted, key)
	return true, nil
}

// fakeGlobal is the global batch reader.
type fakeGlobal struct{ m map[string]*string }

func (g fakeGlobal) GetSiteConfigValues(_ context.Context, keys []string) (map[string]*string, error) {
	out := map[string]*string{}
	for _, k := range keys {
		out[k] = g.m[k]
	}
	return out, nil
}

func sp(s string) *string { return &s }

// reqWithID builds a request whose {id} / {key} path values are set, mimicking
// the std mux. httptest does not populate PathValue, so set it explicitly.
func reqWithID(method, target, id, key, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.SetPathValue("id", id)
	if key != "" {
		r.SetPathValue("key", key)
	}
	return r
}

func TestGet_MergesOverrideAndGlobal(t *testing.T) {
	h := NewHandler(
		&fakeStore{overrides: map[string]*string{"rerank_blend_alpha": sp("0.3")}},
		fakeGlobal{m: map[string]*string{"rerank_blend_alpha": sp("0.8"), "mmr_lambda": sp("0.5")}},
	)
	w := httptest.NewRecorder()
	h.GetSettings(w, reqWithID(http.MethodGet, "/api/kb/k1/settings", "k1", "", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Registry []map[string]any `json:"registry"`
		Values   map[string]struct {
			Override  *string `json:"override"`
			Global    *string `json:"global"`
			Effective *string `json:"effective"`
		} `json:"values"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	a := resp.Values["rerank_blend_alpha"]
	if a.Override == nil || *a.Override != "0.3" || a.Effective == nil || *a.Effective != "0.3" {
		t.Fatalf("alpha override/effective wrong: %+v", a)
	}
	m := resp.Values["mmr_lambda"]
	if m.Override != nil || m.Effective == nil || *m.Effective != "0.5" {
		t.Fatalf("mmr should inherit global: %+v", m)
	}
	if len(resp.Registry) == 0 {
		t.Fatal("registry should be present for the UI")
	}
}

func TestPut_RejectsNonRegistryKey(t *testing.T) {
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	body := `{"configs":{"jwt_secret":"attacker"}}`
	h.PutSettings(w, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for non-registry key, got %d", w.Code)
	}
	if st.upserted != nil {
		t.Fatal("must not upsert when validation fails")
	}
}

func TestPut_RejectsOutOfRange(t *testing.T) {
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	body := `{"configs":{"rerank_blend_alpha":"1.5"}}`
	h.PutSettings(w, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for out-of-range, got %d", w.Code)
	}
}

func TestPut_AcceptsValid(t *testing.T) {
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	body := `{"configs":{"rerank_blend_alpha":"0.3","crag_enabled":"true"}}`
	h.PutSettings(w, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", body))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if st.upserted["rerank_blend_alpha"] == nil || *st.upserted["rerank_blend_alpha"] != "0.3" {
		t.Fatalf("expected upsert of alpha, got %+v", st.upserted)
	}
}

func TestDelete_ResetsKey(t *testing.T) {
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	h.DeleteSetting(w, reqWithID(http.MethodDelete, "/api/kb/k1/settings/rerank_blend_alpha", "k1", "rerank_blend_alpha", ""))
	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", w.Code)
	}
	if len(st.deleted) != 1 || st.deleted[0] != "rerank_blend_alpha" {
		t.Fatalf("expected delete of alpha, got %v", st.deleted)
	}
}

func TestDelete_RejectsNonRegistryKey(t *testing.T) {
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	h.DeleteSetting(w, reqWithID(http.MethodDelete, "/api/kb/k1/settings/jwt_secret", "k1", "jwt_secret", ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for non-registry key, got %d", w.Code)
	}
	if len(st.deleted) != 0 {
		t.Fatal("must not delete a non-registry key")
	}
}
