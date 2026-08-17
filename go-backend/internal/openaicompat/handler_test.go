package openaicompat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/openaicompat"
	"github.com/justrag/go-backend/internal/usage"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ openaicompat.Store = (*mockStore)(nil)

type mockStore struct {
	personalKBs  []kb.KBRow
	globalKBs    []kb.KBRow
	kbByID       *kbaccess.KnowledgeBase
	kbRole       string
	systemPrompt *string
	err          error
}

func (m *mockStore) ListKnowledgeBases(_ context.Context, _ string, _, _ int) ([]kb.KBRow, error) {
	return m.personalKBs, m.err
}

func (m *mockStore) ListGlobalKnowledgeBases(_ context.Context, _ string, _ bool) ([]kb.KBRow, error) {
	return m.globalKBs, m.err
}

func (m *mockStore) GetKBByID(_ context.Context, _ string) (*kbaccess.KnowledgeBase, error) {
	return m.kbByID, m.err
}

func (m *mockStore) GetKBRole(_ context.Context, _, _ string) (string, error) {
	return m.kbRole, m.err
}

func (m *mockStore) GetKBSystemPrompt(_ context.Context, _ string) (*string, error) {
	return m.systemPrompt, m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testUser() *auth.Claims {
	return &auth.Claims{ID: "user-1", Username: "alice", Role: "user"}
}

func withUser(r *http.Request, claims *auth.Claims) *http.Request {
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

func newRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	return httptest.NewRequest(method, path, &buf)
}

func makeKBRow(id, name string) kb.KBRow {
	userID := "user-1"
	return kb.KBRow{
		ID:        id,
		Name:      name,
		UserID:    &userID,
		IsGlobal:  false,
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// Tests: ListModels
// ---------------------------------------------------------------------------

func TestListModels_OK(t *testing.T) {
	store := &mockStore{
		personalKBs: []kb.KBRow{
			makeKBRow("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "My KB"),
		},
		globalKBs: []kb.KBRow{},
	}
	h := openaicompat.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/openai/v1/models", nil), testUser())
	rr := httptest.NewRecorder()
	h.ListModels(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
			Name    string `json:"name"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Object != "list" {
		t.Errorf("expected object=list, got %q", resp.Object)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.Data))
	}

	m := resp.Data[0]
	if m.ID != "kb-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("expected id=kb-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, got %q", m.ID)
	}
	if m.Object != "model" {
		t.Errorf("expected object=model, got %q", m.Object)
	}
	if m.OwnedBy != "justrag" {
		t.Errorf("expected owned_by=justrag, got %q", m.OwnedBy)
	}
	if m.Name != "My KB" {
		t.Errorf("expected name=My KB, got %q", m.Name)
	}
	if m.Created == 0 {
		t.Errorf("expected non-zero created timestamp")
	}
}

func TestListModels_Deduplicates(t *testing.T) {
	// The same KB appears in both personal and global lists.
	shared := makeKBRow("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "Shared KB")
	store := &mockStore{
		personalKBs: []kb.KBRow{shared},
		globalKBs:   []kb.KBRow{shared},
	}
	h := openaicompat.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/openai/v1/models", nil), testUser())
	rr := httptest.NewRecorder()
	h.ListModels(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Errorf("expected 1 deduplicated model, got %d", len(resp.Data))
	}
}

func TestListModels_Unauthenticated(t *testing.T) {
	store := &mockStore{}
	h := openaicompat.NewHandler(store, nil, nil)

	req := newRequest(http.MethodGet, "/openai/v1/models", nil)
	rr := httptest.NewRecorder()
	h.ListModels(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	var resp struct {
		Error struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// Tests: ChatCompletions — validation
// ---------------------------------------------------------------------------

func TestChatCompletions_MissingModel_400(t *testing.T) {
	store := &mockStore{}
	h := openaicompat.NewHandler(store, nil, nil)

	body := map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}
	req := withUser(newRequest(http.MethodPost, "/openai/v1/chat/completions", body), testUser())
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
	if resp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type=invalid_request_error, got %q", resp.Error.Type)
	}
	if resp.Error.Code != "invalid_request" {
		t.Errorf("expected code=invalid_request, got %q", resp.Error.Code)
	}
}

func TestChatCompletions_InvalidModelFormat_400(t *testing.T) {
	store := &mockStore{}
	h := openaicompat.NewHandler(store, nil, nil)

	body := map[string]any{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}
	req := withUser(newRequest(http.MethodPost, "/openai/v1/chat/completions", body), testUser())
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Type != "invalid_request_error" {
		t.Errorf("expected type=invalid_request_error, got %q", resp.Error.Type)
	}
}

func TestChatCompletions_EmptyMessages_400(t *testing.T) {
	store := &mockStore{}
	h := openaicompat.NewHandler(store, nil, nil)

	body := map[string]any{
		"model":    "kb-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"messages": []map[string]string{},
	}
	req := withUser(newRequest(http.MethodPost, "/openai/v1/chat/completions", body), testUser())
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChatCompletions_KBNotFound_404(t *testing.T) {
	store := &mockStore{kbByID: nil} // GetKBByID returns nil
	h := openaicompat.NewHandler(store, nil, nil)

	body := map[string]any{
		"model":    "kb-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}
	req := withUser(newRequest(http.MethodPost, "/openai/v1/chat/completions", body), testUser())
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Error struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestChatCompletions_AccessDenied_403(t *testing.T) {
	otherUser := "user-99"
	store := &mockStore{
		kbByID: &kbaccess.KnowledgeBase{
			ID:       "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			UserID:   &otherUser,
			IsGlobal: false,
		},
		// kbRole zero-value: no kb_members row → no access.
	}
	h := openaicompat.NewHandler(store, nil, nil)

	body := map[string]any{
		"model":    "kb-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}
	req := withUser(newRequest(http.MethodPost, "/openai/v1/chat/completions", body), testUser())
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChatCompletions_Unauthenticated_401(t *testing.T) {
	store := &mockStore{}
	h := openaicompat.NewHandler(store, nil, nil)

	body := map[string]any{
		"model":    "kb-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}
	req := newRequest(http.MethodPost, "/openai/v1/chat/completions", body)
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Usage ledger (Task 6): one usage_events row per accepted OpenAI-compat turn.
// ---------------------------------------------------------------------------

const (
	testKBID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testUserID = "user-1"
)

// fakeUsageRecorder captures usage events for assertions.
type fakeUsageRecorder struct {
	mu     sync.Mutex
	events []usage.Event
}

func (f *fakeUsageRecorder) Record(_ context.Context, e usage.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeUsageRecorder) snapshot() []usage.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]usage.Event(nil), f.events...)
}

// erroringSearcher implements vector.Searcher and always fails Search, so a
// turn that reaches chat.PrepareChatContext is ACCEPTED (auth + KB access
// resolved) but then fails downstream — exactly the case the usage ledger is
// defined to still count. Mirrors publicapi_test.erroringSearcher; the two
// packages don't share a test-support package, so it is duplicated here.
type erroringSearcher struct{}

func (erroringSearcher) Search(_ context.Context, _, _ string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	return nil, errors.New("search backend unavailable")
}

func (erroringSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

var _ vector.Searcher = erroringSearcher{}

// accessibleKBStore returns a mockStore whose GetKBByID/GetKBRole grant the
// caller view access to testKBID via an explicit kb_members row (kbRole:
// "view"), independent of ownership — the same shape kbaccess.EffectiveRole
// resolves through rule 2 in the role ladder.
func accessibleKBStore() *mockStore {
	return &mockStore{
		kbByID: &kbaccess.KnowledgeBase{ID: testKBID},
		kbRole: "view",
	}
}

// TestChatCompletions_RecordsOneUsageEvent pins the property the whole design
// rests on: an ACCEPTED OpenAI-compat turn (valid body, KB access resolved)
// writes exactly one usage event, tagged openai_compat, carrying the
// authenticating API key id — even though the turn then fails.
//
// Why assert on a failing turn: usage.Event's package doc defines a turn as
// counted the moment it is ACCEPTED, before the answer is produced, so the
// numbers are comparable with the LLM gateway's own usage view — a turn that
// fails downstream (here: chat.PrepareChatContext's search call, via
// erroringSearcher) still spent model budget getting there. The Record call
// sits after checkKBAccess but before the RAG pipeline runs, so this test
// drives a genuinely accepted turn through the real handler without needing a
// live AI backend.
func TestChatCompletions_RecordsOneUsageEvent(t *testing.T) {
	rec := &fakeUsageRecorder{}
	store := accessibleKBStore()
	h := openaicompat.NewHandler(store, nil, erroringSearcher{})
	h.SetUsageRecorder(rec)

	keyID := "33333333-3333-3333-3333-333333333333"
	body := map[string]any{
		"model":    "kb-" + testKBID,
		"messages": []map[string]string{{"role": "user", "content": "hallo"}},
	}
	req := newRequest(http.MethodPost, "/openai/v1/chat/completions", body)
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: testUserID, Username: "u", Role: "user"})
	ctx = auth.WithAPIKeyID(ctx, keyID)
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req.WithContext(ctx))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (proving the turn failed after being accepted), got %d: %s", rr.Code, rr.Body.String())
	}

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("usage events: got %d, want 1", len(events))
	}
	if events[0].Surface != usage.SurfaceOpenAICompat {
		t.Errorf("surface: got %q, want openai_compat", events[0].Surface)
	}
	if events[0].APIKeyID == nil || *events[0].APIKeyID != keyID {
		t.Errorf("api key id: got %v, want %s", events[0].APIKeyID, keyID)
	}
	if events[0].KbID != testKBID {
		t.Errorf("kb_id: got %q, want %q", events[0].KbID, testKBID)
	}
	if events[0].UserID != testUserID {
		t.Errorf("user_id: got %q, want %q", events[0].UserID, testUserID)
	}
}

// TestChatCompletions_BadModelRecordsNothing: the model string is where the
// KB comes from, so a request that fails extractKBID has no KB to attribute
// and must not be counted.
func TestChatCompletions_BadModelRecordsNothing(t *testing.T) {
	rec := &fakeUsageRecorder{}
	store := &mockStore{}
	h := openaicompat.NewHandler(store, nil, erroringSearcher{})
	h.SetUsageRecorder(rec)

	body := `{"model":"not-a-kb","messages":[{"role":"user","content":"hallo"}]}`
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(body))
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: testUserID, Username: "u", Role: "user"})
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req.WithContext(ctx))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("usage events on a bad model string: got %d, want 0", got)
	}
}

// TestChatCompletions_AccessDeniedRecordsNothing: a KB the caller cannot view
// must record nothing — the guard for the "after checkKBAccess" half of the
// placement requirement. Unlike the bad-model mutation, moving Record above
// checkKBAccess still compiles here (kbID already exists), which makes this
// the stronger of the two placement guards.
func TestChatCompletions_AccessDeniedRecordsNothing(t *testing.T) {
	rec := &fakeUsageRecorder{}
	otherUser := "user-99"
	store := &mockStore{
		kbByID: &kbaccess.KnowledgeBase{
			ID:       testKBID,
			UserID:   &otherUser,
			IsGlobal: false,
		},
		// kbRole zero-value: no kb_members row → no access.
	}
	h := openaicompat.NewHandler(store, nil, erroringSearcher{})
	h.SetUsageRecorder(rec)

	body := map[string]any{
		"model":    "kb-" + testKBID,
		"messages": []map[string]string{{"role": "user", "content": "hallo"}},
	}
	req := withUser(newRequest(http.MethodPost, "/openai/v1/chat/completions", body), testUser())
	rr := httptest.NewRecorder()
	h.ChatCompletions(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("usage events on access denial: got %d, want 0", got)
	}
}
