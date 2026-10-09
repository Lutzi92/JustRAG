//go:build integration

// End-to-end KB-less library chat over the real chat + userfiles stores, a
// real LibraryTextSource on temp local storage and a fake model provider.
// Skipped when DB_* env is unset; run through the withdb.sh wrapper.

package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/userfiles"
)

func TestLibraryChat_EndToEnd(t *testing.T) {
	pool := conflictTestPool(t)
	ctx := context.Background()
	userID := seedLibraryUser(t, pool, "e2e-send")
	fileID := seedLibraryFile(t, pool, userID, "notes.txt", "e")

	stor, err := storage.New(storage.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := stor.StoreFile(ctx, "users/x/notes.txt", []byte("Der Himmel ist blau. Das Gras ist gruen."), "text/plain"); err != nil {
		t.Fatal(err)
	}

	store := NewStore(pool)
	fakeAI := &libAI{}
	decisions := &fakeDecisionRecorder{}
	h := NewHandler(store, fakeAI.resolver(t), erroringSearcher{},
		WithSiteConfigReader(&fakeSiteConfigReader{values: map[string]*string{}}),
		WithDecisionRecorder(decisions),
		WithLibraryChat(store, userfiles.NewStore(pool), NewLibraryTextSource(stor, parser.DefaultFactoryWith(nil))),
	)

	r := httptest.NewRequest(http.MethodPost, "/api/library/chat?stream=true",
		strings.NewReader(`{"message":"Welche Farbe hat der Himmel?","fileIds":["`+fileID+`"]}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithUser(r.Context(), &auth.Claims{ID: userID, Role: "user"}))
	w := httptest.NewRecorder()
	h.SendLibraryMessage(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	frames := sseFrames(t, w.Body.String())
	// frames[0] is {"stage":"library_prepare"}; the opening frame follows.
	chatID, _ := openingFrame(frames)["chatId"].(string)
	if frames[0]["stage"] != "library_prepare" || chatID == "" || frames[len(frames)-1] != nil {
		t.Fatalf("frames = %v", frames)
	}

	// The chat row: KB-less, typed library, owned by the user.
	var kbNull bool
	var typ, owner string
	if err := pool.QueryRow(ctx, `SELECT kb_id IS NULL, type, user_id::text FROM chats WHERE id = $1::uuid`, chatID).
		Scan(&kbNull, &typ, &owner); err != nil {
		t.Fatalf("chat row: %v", err)
	}
	if !kbNull || typ != "library" || owner != userID {
		t.Fatalf("chat row kb_id NULL=%v type=%q owner=%q", kbNull, typ, owner)
	}
	refs, err := store.GetChatFileRefs(ctx, chatID)
	if err != nil || len(refs) != 1 || refs[0] != fileID {
		t.Fatalf("refs = %v, %v", refs, err)
	}

	// Both messages persisted; the AI answer carries the library source.
	msgs, err := store.GetChatMessages(ctx, chatID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	var ai *MessageRow
	for i := range msgs {
		if msgs[i].Role == "ai" {
			ai = &msgs[i]
		}
	}
	if ai == nil {
		t.Fatal("no ai message persisted")
	}
	var srcs []ChatSource
	if err := json.Unmarshal(ai.Sources, &srcs); err != nil {
		t.Fatalf("decode sources %s: %v", ai.Sources, err)
	}
	if len(srcs) == 0 || srcs[0].UserFileID != fileID || srcs[0].FileID != "" {
		t.Fatalf("persisted sources = %s", ai.Sources)
	}

	// No KB-scoped row from a library chat.
	var chunkLinks int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM message_chunks mc JOIN messages m ON m.id = mc.message_id
		WHERE m.chat_id = $1::uuid`, chatID).Scan(&chunkLinks); err != nil {
		t.Fatal(err)
	}
	if chunkLinks != 0 {
		t.Errorf("message_chunks rows = %d, want 0", chunkLinks)
	}
	decisions.mu.Lock()
	recorded := decisions.called
	decisions.mu.Unlock()
	if recorded {
		t.Error("an agent_decisions row was recorded for a library chat")
	}

	// A parentMessageId from another user's chat neither leaks into the
	// history nor gets stored as the parent.
	foreignChat := seedChat(t, pool, "lib-foreign-parent")
	secret, err := store.AddMessage(ctx, AddMessageParams{ChatID: foreignChat, Role: "user", Content: "FREMDES-GEHEIMNIS"})
	if err != nil {
		t.Fatal(err)
	}
	before := len(fakeAI.snapshot())
	rf := httptest.NewRequest(http.MethodPost, "/api/library/chat",
		strings.NewReader(`{"message":"Und weiter?","chatId":"`+chatID+`","parentMessageId":"`+secret.ID+`"}`))
	rf = rf.WithContext(auth.WithUser(rf.Context(), &auth.Claims{ID: userID, Role: "user"}))
	wf := httptest.NewRecorder()
	h.SendLibraryMessage(wf, rf)
	if wf.Code != http.StatusOK {
		t.Fatalf("foreign-parent turn %d: %s", wf.Code, wf.Body.String())
	}
	for _, b := range fakeAI.snapshot()[before:] {
		if strings.Contains(b, "FREMDES-GEHEIMNIS") {
			t.Fatalf("foreign chat content reached the model: %s", b)
		}
	}
	var foreignParents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE chat_id = $1::uuid AND parent_message_id = $2::uuid`,
		chatID, secret.ID).Scan(&foreignParents); err != nil {
		t.Fatal(err)
	}
	if foreignParents != 0 {
		t.Errorf("%d messages stored with a foreign parent", foreignParents)
	}

	// Second turn on the stored refs, then list + get.
	r2 := httptest.NewRequest(http.MethodPost, "/api/library/chat",
		strings.NewReader(`{"message":"Und das Gras?","chatId":"`+chatID+`"}`))
	r2 = r2.WithContext(auth.WithUser(r2.Context(), &auth.Claims{ID: userID, Role: "user"}))
	w2 := httptest.NewRecorder()
	h.SendLibraryMessage(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second turn %d: %s", w2.Code, w2.Body.String())
	}
	lr := httptest.NewRequest(http.MethodGet, "/api/library/chats", nil)
	lr = lr.WithContext(auth.WithUser(lr.Context(), &auth.Claims{ID: userID, Role: "user"}))
	lw := httptest.NewRecorder()
	h.ListLibraryChats(lw, lr)
	if lw.Code != http.StatusOK || !strings.Contains(lw.Body.String(), chatID) || !strings.Contains(lw.Body.String(), fileID) {
		t.Fatalf("list %d: %s", lw.Code, lw.Body.String())
	}

	// Deleting the library file empties the selection: the next turn is a 400,
	// and the old messages still load.
	if _, err := pool.Exec(ctx, `DELETE FROM user_files WHERE id = $1::uuid`, fileID); err != nil {
		t.Fatal(err)
	}
	r3 := httptest.NewRequest(http.MethodPost, "/api/library/chat",
		strings.NewReader(`{"message":"Noch da?","chatId":"`+chatID+`"}`))
	r3 = r3.WithContext(auth.WithUser(r3.Context(), &auth.Claims{ID: userID, Role: "user"}))
	w3 := httptest.NewRecorder()
	h.SendLibraryMessage(w3, r3)
	if w3.Code != http.StatusBadRequest || !strings.Contains(w3.Body.String(), "no library files selected") {
		t.Fatalf("after delete %d: %s", w3.Code, w3.Body.String())
	}
	if msgs, err := store.GetChatMessages(ctx, chatID); err != nil || len(msgs) != 6 {
		t.Fatalf("history after delete = %d msgs, %v", len(msgs), err)
	}
}
