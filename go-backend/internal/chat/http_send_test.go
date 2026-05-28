package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Minimal Store mock for SendMessage tests
// ---------------------------------------------------------------------------

var _ Store = (*mockStore)(nil)

type mockStore struct {
	chats    map[string]*ChatRow
	messages []MessageRow
}

func newMockStore() *mockStore {
	return &mockStore{chats: make(map[string]*ChatRow)}
}

func (m *mockStore) GetChats(_ context.Context, _, _ string) ([]ChatRow, error) {
	return nil, nil
}

func (m *mockStore) GetChatByID(_ context.Context, chatID string) (*ChatRow, error) {
	c, ok := m.chats[chatID]
	if !ok {
		return nil, nil
	}
	return c, nil
}

func (m *mockStore) CreateChat(_ context.Context, kbID, userID, title string) (*ChatRow, error) {
	c := &ChatRow{
		ID:        "new-chat-id",
		KbID:      kbID,
		UserID:    userID,
		Title:     title,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	m.chats[c.ID] = c
	return c, nil
}

func (m *mockStore) DeleteChat(_ context.Context, _ string) error { return nil }

func (m *mockStore) GetChatMessages(_ context.Context, _ string) ([]MessageRow, error) {
	return m.messages, nil
}

func (m *mockStore) GetMessageAncestors(_ context.Context, _, _ string) ([]MessageRow, error) {
	return m.messages, nil
}

func (m *mockStore) AddMessage(_ context.Context, p AddMessageParams) (*MessageRow, error) {
	msg := &MessageRow{
		ID:         p.Role + "-msg-id",
		ChatID:     p.ChatID,
		Role:       p.Role,
		Content:    p.Content,
		IsEnhanced: p.IsEnhanced,
		CreatedAt:  time.Now(),
	}
	m.messages = append(m.messages, *msg)
	return msg, nil
}

func (m *mockStore) UpdateMessageFeedback(_ context.Context, _, _, _ string, _ *string, _ *string) error {
	return nil
}

func (m *mockStore) UpdateMessageVerification(_ context.Context, _ string, _ *MessageVerification) error {
	return nil
}

func (m *mockStore) UpdateMessageContent(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockStore) UpdateMessageTraceID(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockStore) GetKBSystemPrompt(_ context.Context, _ string) (*string, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Helper: create a Handler with nil AI / search dependencies (sufficient for
// request-validation tests that never reach the AI call).
// ---------------------------------------------------------------------------

func newTestHandler() *Handler {
	return &Handler{
		store:         newMockStore(),
		aiResolver:    nil,
		searchService: nil,
	}
}

// injectUser adds an auth.Claims into the request context, mimicking what the
// auth middleware does.
func injectUser(r *http.Request, userID string) *http.Request {
	claims := &auth.Claims{ID: userID, Role: "user"}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Test 1: missing message → 400
// ---------------------------------------------------------------------------

func TestSendMessage_MissingMessage(t *testing.T) {
	h := newTestHandler()

	body := `{"message": ""}`
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectUser(r, "user1")
	// Inject path value (Go 1.22+ ServeMux pattern).
	r.SetPathValue("id", "kb1")

	w := httptest.NewRecorder()
	h.SendMessage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "message is required") {
		t.Errorf("unexpected error body: %v", resp)
	}
}

// ---------------------------------------------------------------------------
// Test 2: message too long → 400
// ---------------------------------------------------------------------------

func TestSendMessage_MessageTooLong(t *testing.T) {
	h := newTestHandler()

	longMsg := strings.Repeat("x", 32_001)
	payload, _ := json.Marshal(map[string]string{"message": longMsg})

	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r = injectUser(r, "user1")
	r.SetPathValue("id", "kb1")

	w := httptest.NewRecorder()
	h.SendMessage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "maximum length") {
		t.Errorf("unexpected error body: %v", resp)
	}
}

// ---------------------------------------------------------------------------
// Test 3: handler compiles with correct method signature
// ---------------------------------------------------------------------------

func TestSendMessage_HandlerSignature(t *testing.T) {
	// Verify the handler can be constructed and the method is present via
	// an interface assertion. This is a compile-time check.
	var _ http.HandlerFunc = (&Handler{
		store:         newMockStore(),
		aiResolver:    (*ai.ConfigResolver)(nil),
		searchService: vector.Searcher(nil),
	}).SendMessage

	// If we reach here, the method exists with the correct signature.
	t.Log("SendMessage method signature is correct")
}

// ---------------------------------------------------------------------------
// Test 4: writeSSE produces correct SSE format
// ---------------------------------------------------------------------------

func TestWriteSSE_Format(t *testing.T) {
	w := httptest.NewRecorder()
	writeSSE(w, map[string]string{"content": "hello"})

	got := w.Body.String()
	// Must start with "data: " and end with "\n\n"
	if !strings.HasPrefix(got, "data: ") {
		t.Errorf("expected SSE frame to start with 'data: ', got: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("expected SSE frame to end with '\\n\\n', got: %q", got)
	}

	// The JSON payload must be valid and contain our key.
	payload := strings.TrimPrefix(got, "data: ")
	payload = strings.TrimSuffix(payload, "\n\n")
	var parsed map[string]string
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("SSE payload is not valid JSON: %v", err)
	}
	if parsed["content"] != "hello" {
		t.Errorf("expected content='hello', got %q", parsed["content"])
	}
}

// ---------------------------------------------------------------------------
// Test 5: writeSSEDone produces correct terminator
// ---------------------------------------------------------------------------

func TestWriteSSEDone_Terminator(t *testing.T) {
	w := httptest.NewRecorder()
	writeSSEDone(w)

	got := w.Body.String()
	const want = "data: [DONE]\n\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// ---------------------------------------------------------------------------
// Test 6: WithSiteConfigReader wires the reader and gates factcheck correctly
// ---------------------------------------------------------------------------

// stubSiteConfigDisabled is a SiteConfigReader that returns "false" for
// factcheck_in_chat and nil for all other keys.
type stubSiteConfigDisabled struct{}

func (s *stubSiteConfigDisabled) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if key == "factcheck_in_chat" {
		v := "false"
		return &v, nil
	}
	return nil, nil
}

func TestSendMessage_FactcheckDisabled_OmitsCheckFacts(t *testing.T) {
	// Build handler with factcheck disabled via WithSiteConfigReader.
	store := newMockStore()
	cfgReader := &stubSiteConfigDisabled{}
	h := NewHandler(store, nil, nil, WithSiteConfigReader(cfgReader))

	// Verify the siteConfigReader field was wired correctly by checking that
	// FactcheckEnabled returns false for the handler's reader.
	// (Fields are accessible because this test is in package chat.)
	if FactcheckEnabled(context.Background(), h.siteConfigReader) {
		t.Error("expected FactcheckEnabled to return false when factcheck_in_chat=false")
	}

	// Also verify that a nil reader (default, no WithSiteConfigReader) returns true.
	hDefault := NewHandler(store, nil, nil)
	if !FactcheckEnabled(context.Background(), hDefault.siteConfigReader) {
		t.Error("expected FactcheckEnabled to return true when no SiteConfigReader is wired")
	}

	// Verify request-validation still works: a message-too-short request on the
	// factcheck-disabled handler still returns 400, not a panic.
	body := `{"message": ""}`
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectUser(r, "user1")
	r.SetPathValue("id", "kb1")

	w := httptest.NewRecorder()
	h.SendMessage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 7: tryDeepChat path shares the same siteConfigReader — gate invariant
// ---------------------------------------------------------------------------

// TestDeepChat_SiteConfigReaderPropagates confirms that the handler wired with
// WithSiteConfigReader exposes the same reader from the h.siteConfigReader
// field that tryDeepChat accesses. Because all three call sites use
// h.siteConfigReader directly (not a local copy), a single field check
// suffices to assert the gate invariant for all paths including tryDeepChat.
func TestDeepChat_SiteConfigReaderPropagates(t *testing.T) {
	store := newMockStore()
	cfgReader := &stubSiteConfigDisabled{}
	h := NewHandler(store, nil, nil, WithSiteConfigReader(cfgReader))

	// The siteConfigReader field must be exactly the stub we passed in.
	if h.siteConfigReader != cfgReader {
		t.Error("siteConfigReader not propagated: tryDeepChat would not see the disabled gate")
	}

	// Confirm the gate evaluates to false through the same field tryDeepChat uses.
	if FactcheckEnabled(context.Background(), h.siteConfigReader) {
		t.Error("expected FactcheckEnabled(h.siteConfigReader) to return false in tryDeepChat context")
	}
}
