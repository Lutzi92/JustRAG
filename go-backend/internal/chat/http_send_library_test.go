package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSendMessage_LibraryChatIDRejectedOnKBPath pins phase-3 P3-R1: a KB-less
// library chat (KbID "", type "library") owned by the caller is still a 403
// on the KB send path, because resolveOrCreateChat compares chat.KbID to the
// path KB. Mutation: relaxing the KbID comparison makes this go red.
func TestSendMessage_LibraryChatIDRejectedOnKBPath(t *testing.T) {
	rec := &fakeUsageRecorder{}
	store := newMockStore()
	store.chats["lib-1"] = &ChatRow{ID: "lib-1", KbID: "", Type: "library", UserID: "user1"}
	h := &Handler{
		store:         store,
		searchService: erroringSearcher{},
		usageRecorder: rec,
	}

	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", strings.NewReader(`{"message": "hello", "chatId": "lib-1"}`))
	r.Header.Set("Content-Type", "application/json")
	r = injectUser(r, "user1")
	r.SetPathValue("id", "kb1")

	w := httptest.NewRecorder()
	h.SendMessage(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("usage events on a rejected library chat id: got %d, want 0", got)
	}
}
