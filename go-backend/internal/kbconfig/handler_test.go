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
	// Merge into overrides too, so a second call against the same fakeStore
	// (simulating two sequential PUT requests) sees what the first one
	// committed -- real ListKBOverrides reads would.
	if f.overrides == nil {
		f.overrides = map[string]*string{}
	}
	for k, v := range kv {
		f.overrides[k] = v
	}
	return nil
}
func (f *fakeStore) DeleteKey(_ context.Context, _, key string) (bool, error) {
	f.deleted = append(f.deleted, key)
	delete(f.overrides, key)
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

// --- Conflict enforcement on the per-KB save path (Task 4 / Part A) ---
//
// ValidateConflicts (internal/siteconfig/conflicts.go) already rejects a
// global save leaving both halves of chat_self_rag_enabled /
// chat_factuality_verifier_enabled, or raptor_enabled / parent_child_enabled,
// enabled. These tests wire the same enforcement into PutSettings/
// DeleteSetting and pin the "effective resolved values, not raw overrides"
// decision (conflictState's doc comment): a KB save is judged against
// KBOverlayReader's own resolution (KB override, else global), because that
// is what actually determines behavior at answer/ingest time.

func TestPut_RejectsConflictingPair(t *testing.T) {
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	body := `{"configs":{"chat_self_rag_enabled":"true","chat_factuality_verifier_enabled":"true"}}`
	h.PutSettings(w, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a batch that saves both halves of the pair, got %d body=%s", w.Code, w.Body.String())
	}
	if st.upserted != nil {
		t.Fatal("must not upsert when the batch creates a conflict")
	}
	msg := w.Body.String()
	if !strings.Contains(msg, "chat_self_rag_enabled") || !strings.Contains(msg, "chat_factuality_verifier_enabled") {
		t.Fatalf("error should name both conflicting keys, got: %s", msg)
	}
}

func TestPut_RejectsConflictingIngestionPair(t *testing.T) {
	// Same wiring, the OTHER documented pair (raptor vs parent-child) --
	// proves the fix isn't hardcoded to the self-RAG pair.
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	body := `{"configs":{"raptor_enabled":"true","parent_child_enabled":"true"}}`
	h.PutSettings(w, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for raptor+parent_child, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestPut_OneHalfAloneSucceedsWhenOtherIsOff(t *testing.T) {
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	body := `{"configs":{"chat_self_rag_enabled":"true"}}`
	h.PutSettings(w, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", body))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 when the other half of the pair is off, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestPut_RejectsConflictInheritedFromGlobal(t *testing.T) {
	// The global config already has the factuality verifier on; this KB has
	// never overridden it. Saving only chat_self_rag_enabled=true per-KB
	// still creates a conflict -- against the value the KB will actually
	// resolve to at answer time (global, via KBOverlayReader), not against
	// anything the KB itself has overridden. This is the case that
	// distinguishes "overrides-only" from "effective resolved values".
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{"chat_factuality_verifier_enabled": sp("true")}})
	w := httptest.NewRecorder()
	body := `{"configs":{"chat_self_rag_enabled":"true"}}`
	h.PutSettings(w, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400: self_rag override conflicts with the globally-inherited factuality verifier, got %d body=%s", w.Code, w.Body.String())
	}
	if st.upserted != nil {
		t.Fatal("must not upsert when the batch creates a conflict")
	}
}

func TestPut_ClearingOneHalfOfExistingConflictSucceeds(t *testing.T) {
	// Simulates a pre-existing incoherent KB (e.g. rows written before this
	// validator existed): both halves already true as KB overrides. A save
	// that turns one of them off must be allowed -- ValidateConflicts only
	// blocks a batch that LEAVES both true, never one that resolves it.
	st := &fakeStore{overrides: map[string]*string{
		"chat_self_rag_enabled":            sp("true"),
		"chat_factuality_verifier_enabled": sp("true"),
	}}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})
	w := httptest.NewRecorder()
	body := `{"configs":{"chat_factuality_verifier_enabled":"false"}}`
	h.PutSettings(w, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", body))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 when clearing one half of an existing conflict, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestPut_SequentialSavesStillRejected(t *testing.T) {
	// Two single-key saves, each individually harmless, combine into a
	// conflict when applied back-to-back. The second call must see the
	// first call's committed override via a fresh ListKBOverrides read.
	st := &fakeStore{}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{}})

	w1 := httptest.NewRecorder()
	h.PutSettings(w1, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", `{"configs":{"chat_self_rag_enabled":"true"}}`))
	if w1.Code != http.StatusOK {
		t.Fatalf("first save should succeed on its own, got %d body=%s", w1.Code, w1.Body.String())
	}

	w2 := httptest.NewRecorder()
	h.PutSettings(w2, reqWithID(http.MethodPut, "/api/kb/k1/settings", "k1", "", `{"configs":{"chat_factuality_verifier_enabled":"true"}}`))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("second save should be rejected as a conflict with the first, got %d body=%s", w2.Code, w2.Body.String())
	}
}

func TestDelete_RejectsWhenFallbackToGlobalConflicts(t *testing.T) {
	// The KB explicitly overrides the factuality verifier to false, keeping
	// it disabled while self_rag is on (a valid, non-conflicting state).
	// Deleting that override doesn't turn the key "off" -- it falls back to
	// the global value, which is true here -- reintroducing the conflict.
	// This must be rejected too, not only PUT.
	st := &fakeStore{overrides: map[string]*string{
		"chat_self_rag_enabled":            sp("true"),
		"chat_factuality_verifier_enabled": sp("false"),
	}}
	h := NewHandler(st, fakeGlobal{m: map[string]*string{"chat_factuality_verifier_enabled": sp("true")}})
	w := httptest.NewRecorder()
	h.DeleteSetting(w, reqWithID(http.MethodDelete, "/api/kb/k1/settings/chat_factuality_verifier_enabled", "k1", "chat_factuality_verifier_enabled", ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400: clearing the override falls back to a conflicting global value, got %d body=%s", w.Code, w.Body.String())
	}
	if len(st.deleted) != 0 {
		t.Fatal("must not delete when clearing would reintroduce a conflict")
	}
}
