package publicapi_test

import (
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
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/publicapi"
	"github.com/justrag/go-backend/internal/usage"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ publicapi.Store = (*mockStore)(nil)

type mockStore struct {
	kbs      []kb.KBRow
	chats    []chat.ChatRow
	chatRow  *chat.ChatRow
	messages []chat.MessageRow
	err      error
}

func (m *mockStore) ListKnowledgeBases(_ context.Context, _ string, _, _ int) ([]kb.KBRow, error) {
	return m.kbs, m.err
}

func (m *mockStore) GetChats(_ context.Context, _, _ string) ([]chat.ChatRow, error) {
	return m.chats, m.err
}

func (m *mockStore) GetChatByID(_ context.Context, _ string) (*chat.ChatRow, error) {
	return m.chatRow, m.err
}

func (m *mockStore) GetChatMessages(_ context.Context, _ string) ([]chat.MessageRow, error) {
	return m.messages, m.err
}

func (m *mockStore) CreateChat(_ context.Context, _, _, _ string) (*chat.ChatRow, error) {
	return m.chatRow, m.err
}

func (m *mockStore) AddMessage(_ context.Context, _ chat.AddMessageParams) (*chat.MessageRow, error) {
	if len(m.messages) == 0 {
		return nil, m.err
	}
	return &m.messages[0], m.err
}

func (m *mockStore) GetKBSystemPrompt(_ context.Context, _ string) (*string, error) {
	return nil, nil
}

func (m *mockStore) GetMessageAncestors(_ context.Context, _, _ string) ([]chat.MessageRow, error) {
	return m.messages, m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeKB(id, name string) kb.KBRow {
	return kb.KBRow{
		ID:          id,
		Name:        name,
		IsGlobal:    false,
		IsPublished: true,
		Language:    "de",
		CreatedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func makeChat(id, kbID, userID string) chat.ChatRow {
	return chat.ChatRow{
		ID:        id,
		KbID:      kbID,
		UserID:    userID,
		Title:     "Test Chat",
		Type:      "rag",
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func makeMessage(id, chatID string) chat.MessageRow {
	return chat.MessageRow{
		ID:        id,
		ChatID:    chatID,
		Role:      "user",
		Content:   "Hello",
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func newRequest(method, path string) *http.Request {
	return httptest.NewRequest(method, path, nil)
}

// withUser injects auth.Claims into the request context.
func withUser(r *http.Request, claims *auth.Claims) *http.Request {
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

func testUser() *auth.Claims {
	return &auth.Claims{ID: "user-1", Username: "alice", Role: "user"}
}

// ---------------------------------------------------------------------------
// Tests: ListKBs
// ---------------------------------------------------------------------------

func TestListKBs_OK(t *testing.T) {
	row := makeKB("kb-1", "My KB")
	store := &mockStore{kbs: []kb.KBRow{row}}
	h := publicapi.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/v1/kb"), testUser())
	rr := httptest.NewRecorder()
	h.ListKBs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
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

func TestListKBs_EmptyList(t *testing.T) {
	store := &mockStore{kbs: nil}
	h := publicapi.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/v1/kb"), testUser())
	rr := httptest.NewRecorder()
	h.ListKBs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []kb.KBRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty array, got %d items", len(got))
	}
}

func TestListKBs_Unauthenticated(t *testing.T) {
	store := &mockStore{}
	h := publicapi.NewHandler(store, nil, nil)

	req := newRequest(http.MethodGet, "/api/v1/kb") // no user in context
	rr := httptest.NewRecorder()
	h.ListKBs(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: ListChats
// ---------------------------------------------------------------------------

func TestListChats_OK(t *testing.T) {
	row := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chats: []chat.ChatRow{row}}
	h := publicapi.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/v1/kb/kb-1/chats"), testUser())
	req.SetPathValue("id", "kb-1")
	rr := httptest.NewRecorder()
	h.ListChats(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got []chat.ChatRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(got))
	}
	if got[0].ID != "chat-1" {
		t.Errorf("expected id=chat-1, got %s", got[0].ID)
	}
	if rr.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("expected Cache-Control: no-cache header")
	}
}

func TestListChats_EmptyList(t *testing.T) {
	store := &mockStore{chats: nil}
	h := publicapi.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/v1/kb/kb-1/chats"), testUser())
	req.SetPathValue("id", "kb-1")
	rr := httptest.NewRecorder()
	h.ListChats(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got []chat.ChatRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty array, got %d items", len(got))
	}
}

// ---------------------------------------------------------------------------
// Tests: GetMessages
// ---------------------------------------------------------------------------

func TestGetMessages_OK(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	msg := makeMessage("msg-1", "chat-1")
	store := &mockStore{
		chatRow:  &chatRow,
		messages: []chat.MessageRow{msg},
	}
	h := publicapi.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/v1/kb/kb-1/chats/chat-1/messages"), testUser())
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("chatId", "chat-1")
	rr := httptest.NewRecorder()
	h.GetMessages(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got []chat.MessageRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].ID != "msg-1" {
		t.Errorf("expected id=msg-1, got %s", got[0].ID)
	}
	if rr.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("expected Cache-Control: no-cache header")
	}
}

func TestGetMessages_WrongKB_404(t *testing.T) {
	// Chat belongs to kb-2, but request targets kb-1.
	chatRow := makeChat("chat-1", "kb-2", "user-1")
	store := &mockStore{chatRow: &chatRow}
	h := publicapi.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/v1/kb/kb-1/chats/chat-1/messages"), testUser())
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("chatId", "chat-1")
	rr := httptest.NewRecorder()
	h.GetMessages(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong KB, got %d", rr.Code)
	}
}

func TestGetMessages_WrongUser_404(t *testing.T) {
	// Chat belongs to user-2, but caller is user-1.
	chatRow := makeChat("chat-1", "kb-1", "user-2")
	store := &mockStore{chatRow: &chatRow}
	h := publicapi.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/v1/kb/kb-1/chats/chat-1/messages"), testUser())
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("chatId", "chat-1")
	rr := httptest.NewRecorder()
	h.GetMessages(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong user, got %d", rr.Code)
	}
}

func TestGetMessages_ChatNotFound_404(t *testing.T) {
	store := &mockStore{chatRow: nil}
	h := publicapi.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/v1/kb/kb-1/chats/nonexistent/messages"), testUser())
	req.SetPathValue("id", "kb-1")
	req.SetPathValue("chatId", "nonexistent")
	rr := httptest.NewRecorder()
	h.GetMessages(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing chat, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Usage ledger (Task 5): one usage_events row per accepted /api/v1 turn.
// ---------------------------------------------------------------------------

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
// turn that reaches chat.PrepareChatContext is ACCEPTED (auth + KB access +
// chat resolution all succeeded) but then fails downstream — exactly the
// case the usage ledger is defined to still count.
type erroringSearcher struct{}

func (erroringSearcher) Search(_ context.Context, _, _ string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	return nil, errors.New("search backend unavailable")
}

func (erroringSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

var _ vector.Searcher = erroringSearcher{}

// TestSendMessage_RecordsOneAPIv1UsageEvent pins the property the whole
// design rests on: an ACCEPTED /api/v1 turn (valid body, KB access, chat
// resolved) writes exactly one usage event, tagged api_v1, carrying the
// authenticating API key id — even though the turn then fails.
//
// Why assert on a failing turn: usage.Event's package doc defines a turn as
// counted the moment it is ACCEPTED, before the answer is produced, so the
// numbers are comparable with the LLM gateway's own usage view — a turn that
// fails downstream (here: chat.PrepareChatContext's search call, via
// erroringSearcher) still spent model budget getting there. The Record call
// sits after chat resolution but before PrepareChatContext, so this test
// drives a genuinely accepted turn through the real handler without needing
// a live AI backend.
func TestSendMessage_RecordsOneAPIv1UsageEvent(t *testing.T) {
	rec := &fakeUsageRecorder{}
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chatRow: &chatRow}
	h := publicapi.NewHandler(store, nil, erroringSearcher{})
	h.SetUsageRecorder(rec)

	keyID := "22222222-2222-2222-2222-222222222222"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/kb-1/chat", strings.NewReader(`{"message":"hallo"}`))
	req.SetPathValue("id", "kb-1")
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: "user-1", Username: "u", Role: "user"})
	ctx = auth.WithAPIKeyID(ctx, keyID)
	rr := httptest.NewRecorder()
	h.SendMessage(rr, req.WithContext(ctx))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (proving the turn failed after being accepted), got %d: %s", rr.Code, rr.Body.String())
	}

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("usage events: got %d, want 1", len(events))
	}
	if events[0].Surface != usage.SurfaceAPIv1 {
		t.Errorf("surface: got %q, want api_v1", events[0].Surface)
	}
	if events[0].APIKeyID == nil || *events[0].APIKeyID != keyID {
		t.Errorf("api key id: got %v, want %s", events[0].APIKeyID, keyID)
	}
	if events[0].KbID != "kb-1" {
		t.Errorf("kb_id: got %q, want %q", events[0].KbID, "kb-1")
	}
	if events[0].UserID != "user-1" {
		t.Errorf("user_id: got %q, want %q", events[0].UserID, "user-1")
	}
}

// TestSendMessage_UnauthenticatedRecordsNothing pins that a request rejected
// before the Record call (no auth claims → 401) records nothing. This is the
// guard that fails if anyone hoists the Record call above the auth check.
func TestSendMessage_UnauthenticatedRecordsNothing(t *testing.T) {
	rec := &fakeUsageRecorder{}
	store := &mockStore{}
	h := publicapi.NewHandler(store, nil, erroringSearcher{})
	h.SetUsageRecorder(rec)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kb/kb-1/chat", strings.NewReader(`{"message":"hallo"}`))
	req.SetPathValue("id", "kb-1")
	rr := httptest.NewRecorder()
	h.SendMessage(rr, req) // no auth.WithUser on context

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("usage events without auth: got %d, want 0", got)
	}
}
