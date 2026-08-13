package kb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kb"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ kb.Store = (*mockStore)(nil)

type mockStore struct {
	kbs []kb.KBRow
	kb  *kb.KBRow
	err error
	// gotIsAdmin records the last isAdmin argument ListGlobalKnowledgeBases
	// was called with, so tests can pin which store arm the overview picks.
	gotIsAdmin bool
	// gotKBID records the id GetKnowledgeBase was called with.
	gotKBID string
}

func (m *mockStore) ListKnowledgeBases(_ context.Context, _ string, _, _ int) ([]kb.KBRow, error) {
	return m.kbs, m.err
}

func (m *mockStore) ListGlobalKnowledgeBases(_ context.Context, _ string, isAdmin bool) ([]kb.KBRow, error) {
	m.gotIsAdmin = isAdmin
	return m.kbs, m.err
}

func (m *mockStore) GetKnowledgeBase(_ context.Context, kbID, _ string) (*kb.KBRow, error) {
	m.gotKBID = kbID
	return m.kb, m.err
}

func (m *mockStore) CreateKnowledgeBase(_ context.Context, _ string, _ *string, _ string, _ *string) (*kb.KBRow, error) {
	return m.kb, m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeKB(id, name string) kb.KBRow {
	desc := "Test description"
	return kb.KBRow{
		ID:          id,
		Name:        name,
		Description: &desc,
		IsGlobal:    false,
		IsPublished: false,
		Language:    "de",
		CreatedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func newRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	return httptest.NewRequest(method, path, &buf)
}

// withUser injects auth.Claims into the request context via auth.WithUser.
func withUser(r *http.Request, claims *auth.Claims) *http.Request {
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

func testUser() *auth.Claims {
	return &auth.Claims{ID: "user-1", Username: "alice", Role: "user"}
}

func adminUser() *auth.Claims {
	return &auth.Claims{ID: "admin-1", Username: "admin", Role: "admin"}
}

// ---------------------------------------------------------------------------
// Tests: ListKnowledgeBases
// ---------------------------------------------------------------------------

func TestListKnowledgeBases_OK(t *testing.T) {
	row := makeKB("kb-1", "My KB")
	store := &mockStore{kbs: []kb.KBRow{row}}
	h := kb.NewHandler(store)

	req := withUser(httptest.NewRequest(http.MethodGet, "/api/kb", nil), testUser())
	rr := httptest.NewRecorder()
	h.ListKnowledgeBases(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []kb.KBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 KB, got %d", len(got))
	}
	if got[0].ID != "kb-1" {
		t.Errorf("expected id=kb-1, got %s", got[0].ID)
	}
}

func TestListKnowledgeBases_Empty(t *testing.T) {
	store := &mockStore{kbs: []kb.KBRow{}}
	h := kb.NewHandler(store)

	req := withUser(httptest.NewRequest(http.MethodGet, "/api/kb", nil), testUser())
	rr := httptest.NewRecorder()
	h.ListKnowledgeBases(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []kb.KBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 KBs, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Tests: ListGlobalKnowledgeBases
// ---------------------------------------------------------------------------

func TestListGlobalKnowledgeBases_OK(t *testing.T) {
	row := makeKB("kb-g1", "Global KB")
	store := &mockStore{kbs: []kb.KBRow{row}}
	h := kb.NewHandler(store)

	req := withUser(httptest.NewRequest(http.MethodGet, "/api/kb/global", nil), testUser())
	rr := httptest.NewRecorder()
	h.ListGlobalKnowledgeBases(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []kb.KBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 global KB, got %d", len(got))
	}
	if got[0].ID != "kb-g1" {
		t.Errorf("expected id=kb-g1, got %s", got[0].ID)
	}
}

// TestListGlobalKnowledgeBases_AdminIsSubscriptionFiltered pins that the
// overview asks the store for the subscription-filtered arm even for a system
// admin. The endpoint backs the "Favoriten" section, which means "the public
// KBs I chose to keep": the store's admin arm returns every public KB
// regardless of subscription, which left an admin unable to remove a tile at
// all — it came back on the next fetch whatever they clicked. The unfiltered
// inventory is still reachable from the discovery panel and the admin tabs,
// and openaicompat still passes isAdmin=true for its own listing.
func TestListGlobalKnowledgeBases_AdminIsSubscriptionFiltered(t *testing.T) {
	row := makeKB("kb-g2", "Unpublished Global KB")
	store := &mockStore{kbs: []kb.KBRow{row}}
	h := kb.NewHandler(store)

	req := withUser(httptest.NewRequest(http.MethodGet, "/api/kb/global", nil), adminUser())
	rr := httptest.NewRecorder()
	h.ListGlobalKnowledgeBases(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if store.gotIsAdmin {
		t.Fatal("overview passed isAdmin=true for an admin caller — Favoriten must stay subscription-filtered for everyone")
	}

	var got []kb.KBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 KB, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Tests: GetKnowledgeBase
// ---------------------------------------------------------------------------

func TestGetKnowledgeBase_OK(t *testing.T) {
	row := makeKB("kb-detail", "Discovered KB")
	store := &mockStore{kb: &row}
	h := kb.NewHandler(store)

	req := withUser(httptest.NewRequest(http.MethodGet, "/api/kb/kb-detail", nil), testUser())
	req.SetPathValue("id", "kb-detail")
	rr := httptest.NewRecorder()
	h.GetKnowledgeBase(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if store.gotKBID != "kb-detail" {
		t.Errorf("store called with id %q, want %q", store.gotKBID, "kb-detail")
	}

	var got kb.KBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != "kb-detail" {
		t.Errorf("expected id=kb-detail, got %s", got.ID)
	}
}

// A missing row is 404, not 403: hiding a KB the caller may not see is the
// route chain's job (kbViewChain → RequireKBRole), which answers before the
// handler ever runs. The handler must not grow a second, diverging copy of
// that rule.
func TestGetKnowledgeBase_NotFound(t *testing.T) {
	store := &mockStore{kb: nil}
	h := kb.NewHandler(store)

	req := withUser(httptest.NewRequest(http.MethodGet, "/api/kb/missing", nil), testUser())
	req.SetPathValue("id", "missing")
	rr := httptest.NewRecorder()
	h.GetKnowledgeBase(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestGetKnowledgeBase_MissingID(t *testing.T) {
	store := &mockStore{}
	h := kb.NewHandler(store)

	req := withUser(httptest.NewRequest(http.MethodGet, "/api/kb/", nil), testUser())
	rr := httptest.NewRecorder()
	h.GetKnowledgeBase(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: CreateKnowledgeBase
// ---------------------------------------------------------------------------

func TestCreateKnowledgeBase_OK(t *testing.T) {
	row := makeKB("kb-new", "My New KB")
	store := &mockStore{kb: &row}
	h := kb.NewHandler(store)

	body := map[string]any{"name": "My New KB"}
	req := withUser(newRequest(http.MethodPost, "/api/kb", body), testUser())
	rr := httptest.NewRecorder()
	h.CreateKnowledgeBase(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}

	var got kb.KBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != "kb-new" {
		t.Errorf("expected id=kb-new, got %s", got.ID)
	}
}

func TestCreateKnowledgeBase_WithDescriptionAndSystemPrompt(t *testing.T) {
	desc := "A detailed description"
	prompt := "You are a helpful assistant."
	row := makeKB("kb-full", "Full KB")
	row.Description = &desc
	row.SystemPrompt = &prompt
	store := &mockStore{kb: &row}
	h := kb.NewHandler(store)

	body := map[string]any{
		"name":         "Full KB",
		"description":  desc,
		"systemPrompt": prompt,
	}
	req := withUser(newRequest(http.MethodPost, "/api/kb", body), testUser())
	rr := httptest.NewRecorder()
	h.CreateKnowledgeBase(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}

	var got kb.KBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.SystemPrompt == nil || *got.SystemPrompt != prompt {
		t.Errorf("expected systemPrompt=%q, got %v", prompt, got.SystemPrompt)
	}
}

func TestCreateKnowledgeBase_MissingName(t *testing.T) {
	store := &mockStore{}
	h := kb.NewHandler(store)

	body := map[string]any{"name": ""}
	req := withUser(newRequest(http.MethodPost, "/api/kb", body), testUser())
	rr := httptest.NewRecorder()
	h.CreateKnowledgeBase(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestCreateKnowledgeBase_WhitespaceName(t *testing.T) {
	store := &mockStore{}
	h := kb.NewHandler(store)

	body := map[string]any{"name": "   "}
	req := withUser(newRequest(http.MethodPost, "/api/kb", body), testUser())
	rr := httptest.NewRecorder()
	h.CreateKnowledgeBase(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for whitespace-only name, got %d", rr.Code)
	}
}

func TestCreateKnowledgeBase_Unauthenticated(t *testing.T) {
	store := &mockStore{}
	h := kb.NewHandler(store)

	body := map[string]any{"name": "Some KB"}
	req := newRequest(http.MethodPost, "/api/kb", body) // no user injected
	rr := httptest.NewRecorder()
	h.CreateKnowledgeBase(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
