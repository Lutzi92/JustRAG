package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/kbaccess"
	apperrors "github.com/justrag/go-backend/internal/store"
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

	// args of the most recent UpdateChatTitle call; renameErr is returned
	renamedID, renamedBy, renamedTo string
	renameErr                       error

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

func (m *mockStore) UpdateChatTitle(_ context.Context, chatID, userID, title string) error {
	m.renamedID, m.renamedBy, m.renamedTo = chatID, userID, title
	return m.renameErr
}

func (m *mockStore) StarterContext(_ context.Context, _ string, _ int) (chat.StarterSource, error) {
	return chat.StarterSource{}, m.err
}

func (m *mockStore) UpdateChatAgentSelection(_ context.Context, _ string, _, _ *string) error {
	return m.err
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
		KB:   &kbaccess.KnowledgeBase{ID: kbID},
		Role: kbaccess.RoleView,
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
// Tests: RenameChat
// ---------------------------------------------------------------------------

// renameChatID is a valid chat id in a non-canonical spelling (upper case):
// the handler must pass on uuid.Parse's canonical lower-case form.
const (
	renameChatID          = "6F9619FF-8B86-D011-B42D-00C04FC964FF"
	renameChatIDCanonical = "6f9619ff-8b86-d011-b42d-00c04fc964ff"
)

func renameRequest(id string, title string, user *auth.Claims) *http.Request {
	req := withUser(newRequest(http.MethodPatch, "/api/chats/"+id, map[string]string{"title": title}), user)
	req.SetPathValue("id", id)
	return req
}

// Oracle: the request's own inputs — the store must receive the trimmed title,
// the caller's user id and the canonical form of the path id.
func TestRenameChat_OK(t *testing.T) {
	store := &mockStore{}
	h := chat.NewHandler(store, nil, nil)

	rr := httptest.NewRecorder()
	h.RenameChat(rr, renameRequest(renameChatID, "  Budget 2027 ", testUser()))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.renamedTo != "Budget 2027" || store.renamedBy != "user-1" || store.renamedID != renameChatIDCanonical {
		t.Fatalf("store got (%q, %q, %q), want (%q, user-1, Budget 2027)", store.renamedID, store.renamedBy, store.renamedTo, renameChatIDCanonical)
	}
}

// Oracle: the maintainer's rule — a malformed path id is a 404 and never
// reaches the store (the raw value would make Postgres fail with a 500).
func TestRenameChat_MalformedID_404(t *testing.T) {
	store := &mockStore{}
	h := chat.NewHandler(store, nil, nil)
	for _, id := range []string{"chat-1", "urn:uuid:", ""} {
		rr := httptest.NewRecorder()
		h.RenameChat(rr, renameRequest(id, "x", testUser()))
		if rr.Code != http.StatusNotFound || store.renamedTo != "" {
			t.Fatalf("id %q: got %d (store saw %q), want 404 and no store call", id, rr.Code, store.renamedTo)
		}
	}
}

// Oracle: the length limit counts characters. 200 "ä" are 400 bytes and must
// pass; 201 characters must not.
func TestRenameChat_TitleLengthCountsRunes(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  int
	}{
		{strings.Repeat("ä", 200), http.StatusNoContent},
		{strings.Repeat("ä", 201), http.StatusBadRequest},
		{"   ", http.StatusBadRequest},
	} {
		store := &mockStore{}
		h := chat.NewHandler(store, nil, nil)
		rr := httptest.NewRecorder()
		h.RenameChat(rr, renameRequest(renameChatID, tc.title, testUser()))
		if rr.Code != tc.want {
			t.Fatalf("title of %d runes: got %d, want %d", len([]rune(tc.title)), rr.Code, tc.want)
		}
		if tc.want != http.StatusNoContent && store.renamedTo != "" {
			t.Fatalf("rejected title reached the store: %q", store.renamedTo)
		}
	}
}

// Oracle: a chat title is one line — whitespace runs (incl. line breaks)
// collapse to one space; any other control character is refused.
func TestRenameChat_TitleIsOneLine(t *testing.T) {
	for _, tc := range []struct {
		title     string
		wantCode  int
		wantStore string
	}{
		{"Budget\n\t2027\u2028plan", http.StatusNoContent, "Budget 2027 plan"},
		{"Budget\x00 2027", http.StatusBadRequest, ""},
		{"Budget\u200b\x07", http.StatusBadRequest, ""},
	} {
		store := &mockStore{}
		h := chat.NewHandler(store, nil, nil)
		rr := httptest.NewRecorder()
		h.RenameChat(rr, renameRequest(renameChatID, tc.title, testUser()))
		if rr.Code != tc.wantCode || store.renamedTo != tc.wantStore {
			t.Fatalf("title %q: got %d / store %q, want %d / %q", tc.title, rr.Code, store.renamedTo, tc.wantCode, tc.wantStore)
		}
	}
}

// Oracle: the store contract — store.ErrNotFound (not the owner, or no such
// chat) is a 404; any other error a 500.
func TestRenameChat_StoreErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("chat x: %w", apperrors.ErrNotFound), http.StatusNotFound},
		{errors.New("connection reset"), http.StatusInternalServerError},
	} {
		store := &mockStore{renameErr: tc.err}
		h := chat.NewHandler(store, nil, nil)
		rr := httptest.NewRecorder()
		h.RenameChat(rr, renameRequest(renameChatID, "x", otherUser()))
		if rr.Code != tc.want {
			t.Fatalf("store error %v: got %d, want %d", tc.err, rr.Code, tc.want)
		}
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
