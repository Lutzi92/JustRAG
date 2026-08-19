package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/vector"
)

// row is a terse MessageRow constructor for the tree fixtures below.
func row(id, role, content string, parent *string) MessageRow {
	return MessageRow{
		ID:              id,
		ChatID:          "chat-1",
		ParentMessageID: parent,
		Role:            role,
		Content:         content,
		CreatedAt:       time.Now(),
	}
}

// ---------------------------------------------------------------------------
// resolveRegenerateTurn — the pure resolution over an ancestor chain
// ---------------------------------------------------------------------------

func TestResolveRegenerateTurn_HangsAnswerUnderItsOwnUserMessage(t *testing.T) {
	// u1 -> a1 : regenerating a1 must reuse u1, not create a second question.
	rows := []MessageRow{
		row("u1", "user", "Wie funktioniert X?", nil),
		row("a1", "ai", "Antwort 1", ptr("u1")),
	}

	got, err := resolveRegenerateTurn(rows, "a1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.UserMsg.ID != "u1" {
		t.Errorf("UserMsg.ID = %q, want %q", got.UserMsg.ID, "u1")
	}
	if got.Question != "Wie funktioniert X?" {
		t.Errorf("Question = %q, want the stored question", got.Question)
	}
	// The first turn has no earlier history: a nil anchor means "start of chat".
	// Falling back to the chat's last leaf here is what appended the duplicate
	// prompt in the first place.
	if got.HistoryParentID != nil {
		t.Errorf("HistoryParentID = %q, want nil for a root question", *got.HistoryParentID)
	}
}

func TestResolveRegenerateTurn_AnchorsHistoryAtTheTurnBeforeTheQuestion(t *testing.T) {
	// u1 -> a1 -> u2 -> a2 : regenerating a2 must see u1/a1 as history, and
	// must NOT see a2 itself (the answer being replaced).
	rows := []MessageRow{
		row("u1", "user", "Frage 1", nil),
		row("a1", "ai", "Antwort 1", ptr("u1")),
		row("u2", "user", "Frage 2", ptr("a1")),
		row("a2", "ai", "Antwort 2", ptr("u2")),
	}

	got, err := resolveRegenerateTurn(rows, "a2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.UserMsg.ID != "u2" {
		t.Errorf("UserMsg.ID = %q, want %q", got.UserMsg.ID, "u2")
	}
	if got.HistoryParentID == nil || *got.HistoryParentID != "a1" {
		t.Errorf("HistoryParentID = %v, want %q", got.HistoryParentID, "a1")
	}
}

func TestResolveRegenerateTurn_RejectsInvalidTargets(t *testing.T) {
	orphan := []MessageRow{row("a1", "ai", "Antwort", ptr("u-gone"))}
	aiUnderAI := []MessageRow{
		row("a0", "ai", "Antwort 0", nil),
		row("a1", "ai", "Antwort 1", ptr("a0")),
	}
	normal := []MessageRow{
		row("u1", "user", "Frage", nil),
		row("a1", "ai", "Antwort", ptr("u1")),
	}

	tests := []struct {
		name   string
		rows   []MessageRow
		target string
	}{
		// GetMessageAncestors is scoped by chat_id, so a message from another
		// chat comes back as an empty chain. That must not fall through to a
		// normal turn — it would answer under a foreign conversation.
		{"message not in this chat", nil, "a1"},
		{"target is a user message", normal, "u1"},
		{"target is a root AI message", []MessageRow{row("a1", "ai", "Antwort", nil)}, "a1"},
		{"parent is not in the chain", orphan, "a1"},
		{"parent is not a user message", aiUnderAI, "a1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRegenerateTurn(tc.rows, tc.target)
			if err == nil {
				t.Fatalf("expected an error, got %+v", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// History: the answer being replaced must not be fed back in as context
// ---------------------------------------------------------------------------

func TestResolveRegenerateTurn_HistoryStopsBeforeTheQuestion(t *testing.T) {
	rows := []MessageRow{
		row("u1", "user", "Frage 1", nil),
		row("a1", "ai", "Antwort 1", ptr("u1")),
		row("u2", "user", "Frage 2", ptr("a1")),
		row("a2", "ai", "Antwort 2", ptr("u2")),
	}

	got, err := resolveRegenerateTurn(rows, "a2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var ids []string
	for _, r := range got.HistoryRows {
		ids = append(ids, r.ID)
	}
	if len(ids) != 2 || ids[0] != "u1" || ids[1] != "a1" {
		t.Errorf("HistoryRows = %v, want [u1 a1] — the question and the answer "+
			"being replaced must both be excluded", ids)
	}
}

func TestResolveRegenerateTurn_RootQuestionHasNoHistory(t *testing.T) {
	// The chat may well continue past this turn on another branch. Loading
	// "the whole chat" as history — which is what a nil parent anchor falls
	// back to — would feed the answer we are replacing straight back in.
	rows := []MessageRow{
		row("u1", "user", "Wie funktioniert X?", nil),
		row("a1", "ai", "Antwort 1", ptr("u1")),
	}

	got, err := resolveRegenerateTurn(rows, "a1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.HistoryRows) != 0 {
		t.Errorf("HistoryRows = %+v, want empty for the first turn of a chat", got.HistoryRows)
	}
}

// ---------------------------------------------------------------------------
// resolveTurnUserMessage — the single seam all three answer paths insert through
// ---------------------------------------------------------------------------

func TestResolveTurnUserMessage_RegenerateInsertsNoSecondQuestion(t *testing.T) {
	store := newMockStore()
	h := &Handler{store: store}
	existing := row("u1", "user", "Wie funktioniert X?", nil)

	got, err := h.resolveTurnUserMessage(context.Background(),
		AddMessageParams{ChatID: "chat-1", Role: "user", Content: "Wie funktioniert X?"},
		turnAnchor{Regenerate: &regenerateTurn{UserMsg: existing, Question: existing.Content}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "u1" {
		t.Errorf("returned message ID = %q, want the existing %q", got.ID, "u1")
	}
	if len(store.messages) != 0 {
		t.Errorf("inserted %d message(s); a regenerate must add no user row", len(store.messages))
	}
}

func TestResolveTurnUserMessage_NormalTurnInsertsTheQuestion(t *testing.T) {
	store := newMockStore()
	h := &Handler{store: store}

	got, err := h.resolveTurnUserMessage(context.Background(),
		AddMessageParams{ChatID: "chat-1", Role: "user", Content: "Neue Frage"}, turnAnchor{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Content != "Neue Frage" {
		t.Errorf("Content = %q, want %q", got.Content, "Neue Frage")
	}
	if len(store.messages) != 1 {
		t.Fatalf("inserted %d message(s), want 1", len(store.messages))
	}
}

// ---------------------------------------------------------------------------
// Handler wiring: SendMessage actually goes through the resolution above
// ---------------------------------------------------------------------------

// recordingSearcher captures the query PrepareChatContext searches with, then
// fails — enough to observe what the turn decided to ask without a live
// retrieval backend.
type recordingSearcher struct {
	mu      sync.Mutex
	queries []string
}

func (s *recordingSearcher) Search(_ context.Context, _, query string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	return nil, errors.New("search backend unavailable")
}

func (s *recordingSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

func (s *recordingSearcher) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

var _ vector.Searcher = (*recordingSearcher)(nil)

const (
	regenUserMsgID = "11111111-1111-4111-8111-111111111111"
	regenAIMsgID   = "22222222-2222-4222-8222-222222222222"
)

// regenHandler wires a handler over a chat that already holds one answered
// turn: user question regenUserMsgID -> answer regenAIMsgID.
func regenHandler(searcher vector.Searcher) (*Handler, *mockStore) {
	store := newMockStore()
	store.chats["chat-1"] = &ChatRow{ID: "chat-1", KbID: "kb1", UserID: "user1"}
	store.messages = []MessageRow{
		row(regenUserMsgID, "user", "Wie funktioniert X?", nil),
		row(regenAIMsgID, "ai", "Antwort 1", ptr(regenUserMsgID)),
	}
	return &Handler{store: store, aiResolver: nil, searchService: searcher}, store
}

func regenRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectUser(r, "user1")
	r.SetPathValue("id", "kb1")
	return r
}

func TestSendMessage_RegenerateAnswersTheStoredQuestion(t *testing.T) {
	searcher := &recordingSearcher{}
	h, _ := regenHandler(searcher)

	// The client's own message field is deliberately a different string: the
	// question shown above the answer is the only one that may be answered.
	w := httptest.NewRecorder()
	h.SendMessage(w, regenRequest(fmt.Sprintf(
		`{"message":"etwas ganz anderes","chatId":"chat-1","regenerateOfMessageId":%q}`, regenAIMsgID)))

	queries := searcher.snapshot()
	if len(queries) == 0 {
		t.Fatalf("turn never reached retrieval (status %d): %s", w.Code, w.Body.String())
	}
	if queries[0] != "Wie funktioniert X?" {
		t.Errorf("searched for %q, want the stored question %q", queries[0], "Wie funktioniert X?")
	}
}

// A rejected regenerate must leave nothing behind. Both the chat row and the
// usage event are written before the answer is produced, so a request that
// cannot possibly be answered has to be turned away ahead of them — otherwise
// a hand-crafted body leaves a stray empty chat in the sidebar and a billed
// turn that never answered anything.
func TestSendMessage_RegenerateWithoutChatIDCreatesNothing(t *testing.T) {
	rec := &fakeUsageRecorder{}
	h, store := regenHandler(erroringSearcher{})
	h.usageRecorder = rec

	w := httptest.NewRecorder()
	h.SendMessage(w, regenRequest(fmt.Sprintf(
		`{"message":"hallo","regenerateOfMessageId":%q}`, regenAIMsgID)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a regenerate without a chat, got %d: %s", w.Code, w.Body.String())
	}
	if len(store.chats) != 1 {
		t.Errorf("chats: got %d, want only the pre-existing one — no chat may be created", len(store.chats))
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("usage events: got %d, want 0", got)
	}
}

func TestSendMessage_RegenerateWithNonUUIDTargetRecordsNoUsage(t *testing.T) {
	rec := &fakeUsageRecorder{}
	h, _ := regenHandler(erroringSearcher{})
	h.usageRecorder = rec

	w := httptest.NewRecorder()
	h.SendMessage(w, regenRequest(
		`{"message":"hallo","chatId":"chat-1","regenerateOfMessageId":"temp-ai-123"}`))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("usage events on a malformed regenerate: got %d, want 0", got)
	}
}

func TestSendMessage_RegenerateOfUnknownAnswerIsRejected(t *testing.T) {
	h, _ := regenHandler(erroringSearcher{})

	w := httptest.NewRecorder()
	h.SendMessage(w, regenRequest(
		`{"message":"hallo","chatId":"chat-1","regenerateOfMessageId":"33333333-3333-4333-8333-333333333333"}`))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an answer that is not in this chat, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendMessage_RegenerateWithNonUUIDTargetIsRejected(t *testing.T) {
	h, _ := regenHandler(erroringSearcher{})

	// A client placeholder id must not silently degrade into a normal turn —
	// that would append the duplicate prompt this whole change removes.
	w := httptest.NewRecorder()
	h.SendMessage(w, regenRequest(
		`{"message":"hallo","chatId":"chat-1","regenerateOfMessageId":"temp-ai-123"}`))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-uuid target, got %d: %s", w.Code, w.Body.String())
	}
}
