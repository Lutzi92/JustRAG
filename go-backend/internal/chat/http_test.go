package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ chat.Store = (*mockStore)(nil)

type mockStore struct {
	chats    []chat.ChatRow
	chat     *chat.ChatRow
	messages []chat.MessageRow
	err      error

	// captured args from the most recent UpdateMessageFeedback call
	lastUserID   string
	lastFeedback *string
	lastComment  *string
}

func (m *mockStore) GetChats(_ context.Context, _, _ string) ([]chat.ChatRow, error) {
	return m.chats, m.err
}

func (m *mockStore) GetChatByID(_ context.Context, _ string) (*chat.ChatRow, error) {
	return m.chat, m.err
}

func (m *mockStore) CreateChat(_ context.Context, _, _, _ string) (*chat.ChatRow, error) {
	return m.chat, m.err
}

func (m *mockStore) DeleteChat(_ context.Context, _ string) error {
	return m.err
}

func (m *mockStore) GetChatMessages(_ context.Context, _ string) ([]chat.MessageRow, error) {
	return m.messages, m.err
}

func (m *mockStore) GetMessageAncestors(_ context.Context, _, _ string) ([]chat.MessageRow, error) {
	return m.messages, m.err
}

func (m *mockStore) AddMessage(_ context.Context, _ chat.AddMessageParams) (*chat.MessageRow, error) {
	if len(m.messages) == 0 {
		return nil, m.err
	}
	return &m.messages[0], m.err
}

func (m *mockStore) UpdateMessageFeedback(_ context.Context, _, _, userID string, feedback *string, comment *string) error {
	m.lastUserID = userID
	m.lastFeedback = feedback
	m.lastComment = comment
	return m.err
}

func (m *mockStore) UpdateMessageVerification(_ context.Context, _ string, _ *chat.MessageVerification) error {
	return m.err
}

func (m *mockStore) UpdateMessageContent(_ context.Context, _ string, _ string) error {
	return m.err
}

func (m *mockStore) UpdateMessageTraceID(_ context.Context, _ string, _ string) error {
	return m.err
}

func (m *mockStore) GetKBSystemPrompt(_ context.Context, _ string) (*string, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

func newRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	return httptest.NewRequest(method, path, &buf)
}

// withUser injects auth.Claims into the request context.
func withUser(r *http.Request, claims *auth.Claims) *http.Request {
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// withKBAccess injects a KBAccessResult into the request context.
func withKBAccess(r *http.Request, kbID string) *http.Request {
	access := &kbaccess.KBAccessResult{
		KB:         &kbaccess.KnowledgeBase{ID: kbID},
		Permission: "view",
	}
	return r.WithContext(kbaccess.WithAccess(r.Context(), access))
}

func testUser() *auth.Claims {
	return &auth.Claims{ID: "user-1", Username: "alice", Role: "user"}
}

func otherUser() *auth.Claims {
	return &auth.Claims{ID: "user-2", Username: "bob", Role: "user"}
}

// ---------------------------------------------------------------------------
// Tests: ListChats
// ---------------------------------------------------------------------------

func TestListChats_OK(t *testing.T) {
	row := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chats: []chat.ChatRow{row}}
	h := chat.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/kb/kb-1/chats", nil), testUser())
	req = withKBAccess(req, "kb-1")
	rr := httptest.NewRecorder()
	h.ListChats(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
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

// ---------------------------------------------------------------------------
// Tests: GetMessages
// ---------------------------------------------------------------------------

func TestGetMessages_OwnChat_OK(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	msg := makeMessage("msg-1", "chat-1")
	store := &mockStore{
		chat:     &chatRow,
		messages: []chat.MessageRow{msg},
	}
	h := chat.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/chats/chat-1/messages", nil), testUser())
	rr := httptest.NewRecorder()
	h.GetMessages(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
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

func TestGetMessages_OtherUserChat_404(t *testing.T) {
	// Chat belongs to user-1, but caller is user-2.
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chat: &chatRow}
	h := chat.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodGet, "/api/chats/chat-1/messages", nil), otherUser())
	rr := httptest.NewRecorder()
	h.GetMessages(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: DeleteChat
// ---------------------------------------------------------------------------

func TestDeleteChat_OK(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chat: &chatRow}
	h := chat.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodDelete, "/api/chats/chat-1", nil), testUser())
	rr := httptest.NewRecorder()
	h.DeleteChat(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

func TestDeleteChat_OtherUser_404(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chat: &chatRow}
	h := chat.NewHandler(store, nil, nil)

	req := withUser(newRequest(http.MethodDelete, "/api/chats/chat-1", nil), otherUser())
	rr := httptest.NewRecorder()
	h.DeleteChat(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: SubmitFeedback
// ---------------------------------------------------------------------------

func TestSubmitFeedback_OK(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chat: &chatRow}
	h := chat.NewHandler(store, nil, nil)

	positive := "positive"
	body := map[string]any{"feedback": positive}
	req := withUser(newRequest(http.MethodPost, "/api/kb/kb-1/chats/chat-1/messages/msg-1/feedback", body), testUser())
	req = withKBAccess(req, "kb-1")
	rr := httptest.NewRecorder()
	h.SubmitFeedback(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got map[string]bool
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got["success"] {
		t.Errorf("expected success=true")
	}
}

func TestSubmitFeedback_NullFeedback_OK(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chat: &chatRow}
	h := chat.NewHandler(store, nil, nil)

	body := map[string]any{"feedback": nil}
	req := withUser(newRequest(http.MethodPost, "/api/kb/kb-1/chats/chat-1/messages/msg-1/feedback", body), testUser())
	req = withKBAccess(req, "kb-1")
	rr := httptest.NewRecorder()
	h.SubmitFeedback(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestSubmitFeedback_WithComment_OK(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chat: &chatRow}
	h := chat.NewHandler(store, nil, nil)

	body := map[string]any{
		"feedback": "positive",
		"comment":  "Exactly what I needed",
	}
	req := withUser(newRequest(http.MethodPost, "/api/kb/kb-1/chats/chat-1/messages/msg-1/feedback", body), testUser())
	req = withKBAccess(req, "kb-1")
	rr := httptest.NewRecorder()
	h.SubmitFeedback(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if store.lastComment == nil || *store.lastComment != "Exactly what I needed" {
		t.Errorf("store did not receive comment; got %v", store.lastComment)
	}
}

func TestSubmitFeedback_CommentTooLong_400(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chat: &chatRow}
	h := chat.NewHandler(store, nil, nil)

	long := strings.Repeat("a", 2001)
	body := map[string]any{"feedback": "positive", "comment": long}
	req := withUser(newRequest(http.MethodPost, "/api/kb/kb-1/chats/chat-1/messages/msg-1/feedback", body), testUser())
	req = withKBAccess(req, "kb-1")
	rr := httptest.NewRecorder()
	h.SubmitFeedback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSubmitFeedback_ClearWithComment_400(t *testing.T) {
	chatRow := makeChat("chat-1", "kb-1", "user-1")
	store := &mockStore{chat: &chatRow}
	h := chat.NewHandler(store, nil, nil)

	body := map[string]any{"feedback": nil, "comment": "should not be allowed"}
	req := withUser(newRequest(http.MethodPost, "/api/kb/kb-1/chats/chat-1/messages/msg-1/feedback", body), testUser())
	req = withKBAccess(req, "kb-1")
	rr := httptest.NewRecorder()
	h.SubmitFeedback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}
