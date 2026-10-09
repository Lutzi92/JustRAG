package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/longmem"
	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/sessionmem"
	"github.com/justrag/go-backend/internal/userfiles"
)

// ---------------------------------------------------------------------------
// Fakes for the KB-less library chat endpoint (P3-R4/R5).
// ---------------------------------------------------------------------------

const (
	libUser      = "user1"
	libOtherUser = "user2"
	libChatID    = "11111111-1111-4111-8111-111111111111"
	libNewChatID = "22222222-2222-4222-8222-222222222222"
	libFileA     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	libFileB     = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	libFileForgn = "ffffffff-ffff-4fff-8fff-ffffffffffff"
)

// libStore is mockStore plus the LibraryChatStore surface; it records the
// AddMessage params (sources included) and every refs replacement.
type libStore struct {
	*mockStore
	mu       sync.Mutex
	refs     map[string][]string
	replaced map[string][]string
	added    []AddMessageParams
	// msgChat maps message id → chat id for MessageInChat.
	msgChat map[string]string
	// ancestors, when set, is what GetMessageAncestors returns (a stand-in
	// for an unscoped walk that would leak another chat's messages).
	ancestors []MessageRow
	refsErr   error
	deleted   []string
	// batchRefCalls counts GetChatsFileRefs calls (the list must not N+1).
	batchRefCalls int
	singleRefCall int
}

func newLibStore() *libStore {
	return &libStore{mockStore: newMockStore(), refs: map[string][]string{}, replaced: map[string][]string{}, msgChat: map[string]string{}}
}

func (s *libStore) MessageInChat(_ context.Context, messageID, chatID string) (bool, error) {
	return s.msgChat[messageID] == chatID, nil
}

func (s *libStore) GetMessageAncestors(ctx context.Context, messageID, chatID string) ([]MessageRow, error) {
	if s.ancestors != nil {
		return s.ancestors, nil
	}
	return s.mockStore.GetMessageAncestors(ctx, messageID, chatID)
}

func (s *libStore) DeleteChat(_ context.Context, chatID string) error {
	s.deleted = append(s.deleted, chatID)
	delete(s.chats, chatID)
	return nil
}

func (s *libStore) AddMessage(ctx context.Context, p AddMessageParams) (*MessageRow, error) {
	s.mu.Lock()
	s.added = append(s.added, p)
	s.mu.Unlock()
	return s.mockStore.AddMessage(ctx, p)
}

func (s *libStore) CreateLibraryChat(_ context.Context, userID, title string) (*ChatRow, error) {
	c := &ChatRow{ID: libNewChatID, UserID: userID, Title: title, Type: "library", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.chats[c.ID] = c
	return c, nil
}

func (s *libStore) GetLibraryChats(_ context.Context, userID string) ([]ChatRow, error) {
	var out []ChatRow
	for _, c := range s.chats {
		if c.UserID == userID && c.Type == "library" && c.KbID == "" {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (s *libStore) GetChatFileRefs(_ context.Context, chatID string) ([]string, error) {
	s.mu.Lock()
	s.singleRefCall++
	s.mu.Unlock()
	return append([]string{}, s.refs[chatID]...), nil
}

func (s *libStore) GetChatsFileRefs(_ context.Context, chatIDs []string) (map[string][]string, error) {
	s.mu.Lock()
	s.batchRefCalls++
	s.mu.Unlock()
	out := map[string][]string{}
	for _, id := range chatIDs {
		if r, ok := s.refs[id]; ok && len(r) > 0 {
			out[id] = append([]string{}, r...)
		}
	}
	return out, nil
}

func (s *libStore) ReplaceChatFileRefs(_ context.Context, chatID string, ids []string) error {
	if s.refsErr != nil {
		return s.refsErr
	}
	s.replaced[chatID] = append([]string{}, ids...)
	s.refs[chatID] = append([]string{}, ids...)
	return nil
}

var _ LibraryChatStore = (*libStore)(nil)

// fakeLibFiles mirrors userfiles.Store.Get's contract: ErrNotFound for a
// missing, foreign or malformed id.
type fakeLibFiles struct {
	files map[string]*userfiles.UserFile
}

func (f *fakeLibFiles) Get(_ context.Context, ownerID, id string) (*userfiles.UserFile, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, userfiles.ErrNotFound
	}
	uf, ok := f.files[id]
	if !ok || uf.OwnerUserID != ownerID {
		return nil, userfiles.ErrNotFound
	}
	return uf, nil
}

type fakeLibText struct {
	mu     sync.Mutex
	texts  map[string]*parser.ParseResult
	errs   map[string]error
	called []string
	// onText, when set, runs at the start of every Text call.
	onText func()
}

func (f *fakeLibText) Text(_ context.Context, uf *userfiles.UserFile) (*parser.ParseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onText != nil {
		f.onText()
	}
	f.called = append(f.called, uf.ID)
	if err := f.errs[uf.ID]; err != nil {
		return nil, err
	}
	return f.texts[uf.ID], nil
}

// libAI is a fake model provider: streams "Antwort [1]" for a streaming
// request and returns a JSON array for any unary completion (follow-ups). It
// records every request body so tests can count the unary calls and assert
// the answer request carried no tools.
type libAI struct {
	mu     sync.Mutex
	bodies []string
}

func (a *libAI) resolver(t *testing.T) *ai.ConfigResolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.bodies = append(a.bodies, r.URL.Path+" "+string(raw))
		a.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
			return
		}
		var probe struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Antwort [1]\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[\"Weiter?\"]"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return ai.NewConfigResolver(&chatTestConfigStore{baseURL: srv.URL + "/v1/", model: "fake-model"})
}

func (a *libAI) snapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.bodies...)
}

type libChatFixture struct {
	h     *Handler
	store *libStore
	files *fakeLibFiles
	text  *fakeLibText
	ai    *libAI
	usage *fakeUsageRecorder
}

func newLibChatFixture(t *testing.T, cfg map[string]*string) *libChatFixture {
	t.Helper()
	fx := &libChatFixture{
		store: newLibStore(),
		files: &fakeLibFiles{files: map[string]*userfiles.UserFile{
			libFileA:     {ID: libFileA, OwnerUserID: libUser, Name: "alpha.txt"},
			libFileB:     {ID: libFileB, OwnerUserID: libUser, Name: "beta.txt"},
			libFileForgn: {ID: libFileForgn, OwnerUserID: libOtherUser, Name: "secret.txt"},
		}},
		text: &fakeLibText{
			texts: map[string]*parser.ParseResult{
				libFileA: {Text: "Alpha sagt: Der Himmel ist blau."},
				libFileB: {Text: "Beta sagt: Das Gras ist gruen."},
			},
			errs: map[string]error{},
		},
		ai:    &libAI{},
		usage: &fakeUsageRecorder{},
	}
	if cfg == nil {
		cfg = map[string]*string{}
	}
	fx.h = NewHandler(fx.store, fx.ai.resolver(t), erroringSearcher{},
		WithSiteConfigReader(&fakeSiteConfigReader{values: cfg}),
		WithUsageRecorder(fx.usage),
		WithLibraryChat(fx.store, fx.files, fx.text),
	)
	return fx
}

func (fx *libChatFixture) send(t *testing.T, body string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/library/chat"
	if stream {
		target += "?stream=true"
	}
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectUser(r, libUser)
	w := httptest.NewRecorder()
	fx.h.SendLibraryMessage(w, r)
	return w
}

// sseFrames splits an SSE body into decoded frames; "[DONE]" is kept as a
// nil map so its position can be asserted.
func sseFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			out = append(out, nil)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("bad frame %q: %v", payload, err)
		}
		out = append(out, m)
	}
	return out
}

// openingFrame is the first frame carrying a chatId (after the prepare and
// per-file parse progress frames).
func openingFrame(frames []map[string]any) map[string]any {
	for _, f := range frames {
		if _, ok := f["chatId"]; ok {
			return f
		}
	}
	return map[string]any{}
}

func (fx *libChatFixture) seedLibraryChat(id, owner string, refs ...string) {
	fx.store.chats[id] = &ChatRow{ID: id, UserID: owner, Type: "library", Title: "t"}
	fx.store.refs[id] = refs
}

// ---------------------------------------------------------------------------
// Send
// ---------------------------------------------------------------------------

func TestSendLibraryMessage_NewChatStreamsKBFramesAndPersistsRefs(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	w := fx.send(t, `{"message":"Welche Farbe hat der Himmel?","fileIds":["`+libFileA+`","`+libFileB+`"]}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	frames := sseFrames(t, w.Body.String())
	if len(frames) < 5 {
		t.Fatalf("frames = %v", frames)
	}
	// The stream opens with library_prepare, before the files are parsed.
	if len(frames[0]) != 1 || frames[0]["stage"] != "library_prepare" {
		t.Fatalf("first frame = %v, want {stage: library_prepare}", frames[0])
	}
	open := openingFrame(frames)
	if open["chatId"] != libNewChatID || open["userMessageId"] != "user-msg-id" {
		t.Fatalf("opening frame = %v", open)
	}
	srcs, _ := open["sources"].([]any)
	if len(srcs) != 2 {
		t.Fatalf("sources = %v", open["sources"])
	}
	for i, want := range []string{libFileA, libFileB} {
		s := srcs[i].(map[string]any)
		if s["userFileId"] != want {
			t.Errorf("source %d userFileId = %v, want %s", i, s["userFileId"], want)
		}
		if fid, ok := s["fileId"]; ok && fid != "" {
			t.Errorf("source %d fileId = %v, want empty", i, fid)
		}
	}
	// Order: prepare → opening → content → aiMessageId → … → [DONE] last.
	idxContent, idxAI := -1, -1
	for i, f := range frames {
		if f == nil {
			continue
		}
		if _, ok := f["content"]; ok && idxContent < 0 {
			idxContent = i
		}
		if _, ok := f["aiMessageId"]; ok {
			idxAI = i
		}
	}
	if idxContent < 2 || idxAI <= idxContent {
		t.Fatalf("frame order wrong: content at %d, aiMessageId at %d: %v", idxContent, idxAI, frames)
	}
	if frames[len(frames)-1] != nil {
		t.Fatalf("last frame is not [DONE]: %v", frames[len(frames)-1])
	}
	if got := fx.store.replaced[libNewChatID]; len(got) != 2 || got[0] != libFileA || got[1] != libFileB {
		t.Errorf("refs replaced = %v", got)
	}
	// Persisted AI message carries the library sources.
	var aiMsg *AddMessageParams
	for i := range fx.store.added {
		if fx.store.added[i].Role == "ai" {
			aiMsg = &fx.store.added[i]
		}
	}
	if aiMsg == nil || len(aiMsg.Sources) != 2 || aiMsg.Sources[0].UserFileID != libFileA {
		t.Fatalf("persisted ai message = %+v", aiMsg)
	}
	ev := fx.usage.snapshot()
	if len(ev) != 1 || ev[0].KbID != "" || ev[0].UserID != libUser {
		t.Errorf("usage events = %+v", ev)
	}
}

func TestSendLibraryMessage_JSONMode(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	w := fx.send(t, `{"message":"Himmel?","fileIds":["`+libFileA+`"]}`, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ChatID      string       `json:"chatId"`
		AIMessageID string       `json:"aiMessageId"`
		Sources     []ChatSource `json:"sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ChatID != libNewChatID || resp.AIMessageID == "" || len(resp.Sources) != 1 || resp.Sources[0].UserFileID != libFileA {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSendLibraryMessage_ExistingChatUsesStoredRefs(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.seedLibraryChat(libChatID, libUser, libFileB)
	w := fx.send(t, `{"message":"Gras?","chatId":"`+libChatID+`"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(fx.text.called) != 1 || fx.text.called[0] != libFileB {
		t.Errorf("text source called for %v, want only %s", fx.text.called, libFileB)
	}
	if _, ok := fx.store.replaced[libChatID]; ok {
		t.Error("stored refs must not be replaced when the body carries no fileIds")
	}
	if frames := sseFrames(t, w.Body.String()); openingFrame(frames)["chatId"] != libChatID {
		t.Errorf("chatId = %v", openingFrame(frames)["chatId"])
	}
}

func TestSendLibraryMessage_ExistingChatBodyIDsReplaceRefs(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.seedLibraryChat(libChatID, libUser, libFileB)
	w := fx.send(t, `{"message":"Himmel?","chatId":"`+libChatID+`","fileIds":["`+libFileA+`"]}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := fx.store.replaced[libChatID]; len(got) != 1 || got[0] != libFileA {
		t.Errorf("replaced = %v", got)
	}
}

func TestSendLibraryMessage_RejectsBadFileIDs(t *testing.T) {
	many := make([]string, 21)
	for i := range many {
		many[i] = `"` + uuid.NewString() + `"`
	}
	cases := []struct {
		name string
		ids  string
		code int
	}{
		{"foreign", `["` + libFileForgn + `"]`, http.StatusNotFound},
		{"malformed", `["not-a-uuid"]`, http.StatusNotFound},
		{"missing", `["` + uuid.NewString() + `"]`, http.StatusNotFound},
		{"too many", `[` + strings.Join(many, ",") + `]`, http.StatusBadRequest},
		{"none", `[]`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newLibChatFixture(t, nil)
			w := fx.send(t, `{"message":"x","fileIds":`+tc.ids+`}`, true)
			if w.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret.txt") {
				t.Error("error names the foreign file")
			}
			if len(fx.store.chats) != 0 {
				t.Errorf("a chat was created on a rejected turn: %v", fx.store.chats)
			}
			if len(fx.usage.snapshot()) != 0 {
				t.Error("usage recorded on a rejected turn")
			}
		})
	}
}

func TestSendLibraryMessage_RejectsNonLibraryOrForeignChat(t *testing.T) {
	cases := map[string]func(fx *libChatFixture) string{
		"kb chat": func(fx *libChatFixture) string {
			fx.store.chats[libChatID] = &ChatRow{ID: libChatID, KbID: "kb1", UserID: libUser, Type: "chat"}
			return libChatID
		},
		"library-typed chat with a kb": func(fx *libChatFixture) string {
			fx.store.chats[libChatID] = &ChatRow{ID: libChatID, KbID: "kb1", UserID: libUser, Type: "library"}
			return libChatID
		},
		"foreign library chat": func(fx *libChatFixture) string {
			fx.seedLibraryChat(libChatID, libOtherUser, libFileA)
			return libChatID
		},
		"missing chat": func(*libChatFixture) string { return libChatID },
		"malformed":    func(*libChatFixture) string { return "nope" },
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newLibChatFixture(t, nil)
			id := seed(fx)
			w := fx.send(t, `{"message":"x","chatId":"`+id+`","fileIds":["`+libFileA+`"]}`, true)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404: %s", w.Code, w.Body.String())
			}
			if len(fx.store.replaced) != 0 {
				t.Error("refs replaced on a rejected chat")
			}
		})
	}
}

func TestSendLibraryMessage_TooLarge(t *testing.T) {
	fx := newLibChatFixture(t, map[string]*string{
		"chat_library_fulltext_max_tokens": strPtr("4000"),
		"chat_longcontext_max_tokens":      strPtr("10000"),
	})
	fx.text.texts[libFileA] = &parser.ParseResult{Text: bigText(4000)}
	w := fx.send(t, `{"message":"x","fileIds":["`+libFileA+`"]}`, false)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "selected files are too large for one chat turn (") ||
		!strings.Contains(w.Body.String(), "maximum 10000)") {
		t.Errorf("body = %s", w.Body.String())
	}
	if len(fx.store.chats) != 0 {
		t.Error("chat created on a too-large turn")
	}
}

func TestSendLibraryMessage_UnparseableNamesFile(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.text.errs[libFileB] = ErrUnparseable
	w := fx.send(t, `{"message":"x","fileIds":["`+libFileA+`","`+libFileB+`"]}`, false)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "beta.txt") {
		t.Errorf("body does not name the file: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), ErrUnparseable.Error()) {
		t.Errorf("body echoes the error: %s", w.Body.String())
	}
}

func TestSendLibraryMessage_TextSourceErrorIs500(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.text.errs[libFileA] = errors.New("s3 down: secret-bucket")
	w := fx.send(t, `{"message":"x","fileIds":["`+libFileA+`"]}`, false)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-bucket") {
		t.Errorf("body leaks the cause: %s", w.Body.String())
	}
}

func TestSendLibraryMessage_StoredRefsAllDeleted(t *testing.T) {
	t.Run("no refs left", func(t *testing.T) {
		fx := newLibChatFixture(t, nil)
		fx.seedLibraryChat(libChatID, libUser)
		w := fx.send(t, `{"message":"x","chatId":"`+libChatID+`"}`, true)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no library files selected") {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("ref to a since-deleted file is skipped", func(t *testing.T) {
		fx := newLibChatFixture(t, nil)
		gone := uuid.NewString()
		fx.seedLibraryChat(libChatID, libUser, gone, libFileA)
		w := fx.send(t, `{"message":"x","chatId":"`+libChatID+`"}`, true)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if len(fx.text.called) != 1 || fx.text.called[0] != libFileA {
			t.Errorf("text called for %v", fx.text.called)
		}
	})
	t.Run("only deleted refs", func(t *testing.T) {
		fx := newLibChatFixture(t, nil)
		fx.seedLibraryChat(libChatID, libUser, uuid.NewString())
		w := fx.send(t, `{"message":"x","chatId":"`+libChatID+`"}`, true)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no library files selected") {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestSendLibraryMessage_RejectsRegenerate(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.seedLibraryChat(libChatID, libUser, libFileA)
	w := fx.send(t, `{"message":"x","chatId":"`+libChatID+`","regenerateOfMessageId":"`+uuid.NewString()+`"}`, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

// The stream opens (library_prepare) before the first file is parsed.
func TestSendLibraryMessage_PrepareFrameBeforeParse(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/library/chat?stream=true",
		strings.NewReader(`{"message":"x","fileIds":["`+libFileA+`"]}`))
	r = injectUser(r, libUser)
	w := httptest.NewRecorder()
	var bodyAtParse string
	var ctAtParse string
	fx.text.onText = func() {
		bodyAtParse = w.Body.String()
		ctAtParse = w.Header().Get("Content-Type")
	}
	fx.h.SendLibraryMessage(w, r)
	want := "data: {\"stage\":\"library_prepare\"}\n\n" +
		"data: {\"file\":\"alpha.txt\",\"index\":0,\"stage\":\"library_parse\",\"total\":1}\n\n"
	if bodyAtParse != want {
		t.Fatalf("body when parsing started = %q", bodyAtParse)
	}
	if ctAtParse != "text/event-stream" {
		t.Fatalf("content type when parsing started = %q", ctAtParse)
	}
}

// After the stream opened, every rejection is an SSE error frame + [DONE]
// with the same message the JSON mode returns; nothing is created.
func TestSendLibraryMessage_StreamErrorsAfterPrepareFrame(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(fx *libChatFixture)
		want    string
		created bool // the new chat is created and then removed again
	}{
		{"too large", func(fx *libChatFixture) {
			fx.text.texts[libFileA] = &parser.ParseResult{Text: bigText(4000)}
		}, "selected files are too large for one chat turn (at least 12000 tokens, maximum 10000)", false},
		{"unparseable", func(fx *libChatFixture) { fx.text.errs[libFileA] = ErrUnparseable },
			`the file "alpha.txt" cannot be read as text`, false},
		{"parse timeout", func(fx *libChatFixture) { fx.text.errs[libFileA] = ErrLibraryParseTimeout },
			`the file "alpha.txt" took too long to read`, false},
		{"text source error", func(fx *libChatFixture) { fx.text.errs[libFileA] = errors.New("s3: secret") },
			"failed to read library file", false},
		{"no text", func(fx *libChatFixture) { fx.text.texts[libFileA] = &parser.ParseResult{Text: "  "} },
			"the selected files contain no text", false},
		{"refs race", func(fx *libChatFixture) {
			fx.store.refsErr = fmt.Errorf("x: %w", ErrChatFileRefGone)
		}, "file not found", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newLibChatFixture(t, map[string]*string{
				"chat_library_fulltext_max_tokens": strPtr("4000"),
				"chat_longcontext_max_tokens":      strPtr("10000"),
			})
			tc.setup(fx)
			w := fx.send(t, `{"message":"x","fileIds":["`+libFileA+`"]}`, true)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d (stream already open): %s", w.Code, w.Body.String())
			}
			frames := sseFrames(t, w.Body.String())
			if len(frames) != 4 || frames[0]["stage"] != "library_prepare" ||
				frames[1]["stage"] != "library_parse" ||
				frames[2]["error"] != tc.want || frames[3] != nil {
				t.Fatalf("frames = %v, want [prepare, parse, {error: %q}, DONE]", frames, tc.want)
			}
			if len(fx.store.chats) != 0 || len(fx.usage.snapshot()) != 0 {
				t.Errorf("chat or usage left on a rejected turn: %v", fx.store.chats)
			}
			if tc.created != (len(fx.store.deleted) == 1) {
				t.Errorf("deleted = %v", fx.store.deleted)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Library mode skips every KB-bound post-response task (Review Focus 4).
// ---------------------------------------------------------------------------

type spyLongmem struct {
	longmem.Store
	mu    sync.Mutex
	calls int
}

func (s *spyLongmem) hit() { s.mu.Lock(); s.calls++; s.mu.Unlock() }
func (s *spyLongmem) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
func (s *spyLongmem) Insert(context.Context, string, string, string, string, float64, []float64) error {
	s.hit()
	return nil
}
func (s *spyLongmem) Recall(context.Context, string, string, int, int) ([]longmem.Memory, error) {
	s.hit()
	return nil, nil
}
func (s *spyLongmem) RecallSemantic(context.Context, string, string, []float64, int, int) ([]longmem.Memory, error) {
	s.hit()
	return nil, nil
}
func (s *spyLongmem) NearestMemories(context.Context, string, string, []float64, int) ([]longmem.Memory, error) {
	s.hit()
	return nil, nil
}
func (s *spyLongmem) InsertWithSupersede(context.Context, string, string, string, string, float64, []float64, []int64) error {
	s.hit()
	return nil
}

type spySessionMem struct {
	mu    sync.Mutex
	calls int
}

func (s *spySessionMem) hit() { s.mu.Lock(); s.calls++; s.mu.Unlock() }
func (s *spySessionMem) Get(context.Context, string) (sessionmem.SessionMemory, error) {
	s.hit()
	return sessionmem.SessionMemory{}, nil
}
func (s *spySessionMem) AppendNote(context.Context, string, sessionmem.SessionNote) error {
	s.hit()
	return nil
}
func (s *spySessionMem) AppendFinding(context.Context, string, sessionmem.FindingRef) error {
	s.hit()
	return nil
}
func (s *spySessionMem) Delete(context.Context, string) error { s.hit(); return nil }

// toolCatalogDispatcher is a real MCPDispatcher with a non-empty answer-tool
// catalog, so a writer that offered answer tools would put "tools" into the
// answer request (asserted below).
func toolCatalogDispatcher() *MCPDispatcher {
	reg := mcp.NewRegistry()
	reg.RegisterBuiltin(mcp.Tool{Name: "calculator", Description: "math",
		InputSchema: json.RawMessage(`{"type":"object"}`)})
	return NewMCPDispatcher(reg)
}

func TestSendLibraryMessage_LibraryModeSkipsKBBoundTasks(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := "json"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			on := strPtr("true")
			fx := newLibChatFixture(t, map[string]*string{
				"chat_longmem_enabled":                on,
				"chat_session_memory_enabled":         on,
				"chat_answer_tools_enabled":           on,
				"factcheck_in_chat":                   on,
				"chat_factuality_verifier_enabled":    on,
				"chat_factuality_verifier_always_run": on,
				"chat_self_rag_enabled":               on,
				"chat_factuality_gate_enabled":        on,
				"chat_conflict_surfacing_enabled":     on,
				"chat_tabular_query_enabled":          on,
				"chat_citation_spans_enabled":         on,
				"ragas_sampling_enabled":              on,
				"ragas_sampling_rate":                 strPtr("1"),
			})
			lm, sm, td := &spyLongmem{}, &spySessionMem{}, toolCatalogDispatcher()
			dr, fd, tl := &fakeDecisionRecorder{}, &fakeFileDates{}, &fakeTabularQueryLogger{}
			fx.h.longmemStore = lm
			fx.h.sessionMemory = sm
			fx.h.toolDispatcher = td
			fx.h.decisionRecorder = dr
			fx.h.fileDates = fd
			fx.h.tabularQueryLog = tl

			w := fx.send(t, `{"message":"Himmel?","fileIds":["`+libFileA+`"]}`, stream)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			// Let any stray fire-and-forget goroutine land before asserting.
			time.Sleep(50 * time.Millisecond)

			if n := lm.n(); n != 0 {
				t.Errorf("longmem store called %d times", n)
			}
			if sm.calls != 0 {
				t.Errorf("session memory called %d times", sm.calls)
			}
			dr.mu.Lock()
			if dr.called {
				t.Error("agent decision recorded for a library turn")
			}
			dr.mu.Unlock()
			if fd.calls != 0 {
				t.Errorf("file dates looked up %d times", fd.calls)
			}
			if got := tl.calls(); len(got) != 0 {
				t.Errorf("tabular query log rows: %v", got)
			}

			// The model provider saw exactly: one answer completion without
			// tools, and one unary completion (follow-up questions). Any
			// factcheck / verifier / Self-RAG / longmem-extract / span call
			// would be a further unary completion.
			unary, answer := 0, 0
			for _, b := range fx.ai.snapshot() {
				if !strings.Contains(b, "/chat/completions") {
					continue
				}
				if strings.Contains(b, `"tool_choice"`) || strings.Contains(b, `"tools"`) {
					t.Errorf("answer request carried tools: %s", b)
				}
				var probe struct {
					Stream bool `json:"stream"`
				}
				_ = json.Unmarshal([]byte(b[strings.Index(b, " ")+1:]), &probe)
				if probe.Stream {
					answer++
				} else {
					unary++
				}
			}
			wantAnswer, wantUnary := 1, 1
			if !stream {
				// Non-streaming: the answer itself is a unary completion.
				wantAnswer, wantUnary = 0, 2
			}
			if answer != wantAnswer || unary != wantUnary {
				t.Errorf("model calls: %d streaming + %d unary, want %d + %d: %v", answer, unary, wantAnswer, wantUnary, fx.ai.snapshot())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// List / Get
// ---------------------------------------------------------------------------

func TestListLibraryChats_OnlyOwnLibraryChats(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.seedLibraryChat(libChatID, libUser, libFileA, libFileB)
	fx.seedLibraryChat(libNewChatID, libOtherUser, libFileForgn)
	fx.store.chats["33333333-3333-4333-8333-333333333333"] = &ChatRow{ID: "33333333-3333-4333-8333-333333333333", KbID: "kb1", UserID: libUser, Type: "chat"}

	r := injectUser(httptest.NewRequest(http.MethodGet, "/api/library/chats", nil), libUser)
	w := httptest.NewRecorder()
	fx.h.ListLibraryChats(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct {
			ID      string   `json:"id"`
			Title   string   `json:"title"`
			FileIDs []string `json:"fileIds"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != libChatID || len(resp.Items[0].FileIDs) != 2 {
		t.Fatalf("items = %+v", resp.Items)
	}
	for _, k := range []string{`"createdAt"`, `"updatedAt"`, `"fileIds"`} {
		if !strings.Contains(w.Body.String(), k) {
			t.Errorf("response lacks %s: %s", k, w.Body.String())
		}
	}
	if fx.store.batchRefCalls != 1 || fx.store.singleRefCall != 0 {
		t.Errorf("refs lookups: batch %d, single %d; want one batch query", fx.store.batchRefCalls, fx.store.singleRefCall)
	}
}

// A chat without refs still lists fileIds as [] (the batch map omits it).
func TestListLibraryChats_ChatWithoutRefs(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.seedLibraryChat(libChatID, libUser)
	r := injectUser(httptest.NewRequest(http.MethodGet, "/api/library/chats", nil), libUser)
	w := httptest.NewRecorder()
	fx.h.ListLibraryChats(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"fileIds":[]`) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestListLibraryChats_EmptyIsArray(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	r := injectUser(httptest.NewRequest(http.MethodGet, "/api/library/chats", nil), libUser)
	w := httptest.NewRecorder()
	fx.h.ListLibraryChats(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestGetLibraryChat(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.seedLibraryChat(libChatID, libUser, libFileA)
	fx.seedLibraryChat(libNewChatID, libOtherUser)
	kbChat := "33333333-3333-4333-8333-333333333333"
	fx.store.chats[kbChat] = &ChatRow{ID: kbChat, KbID: "kb1", UserID: libUser, Type: "chat"}

	get := func(id string) *httptest.ResponseRecorder {
		r := injectUser(httptest.NewRequest(http.MethodGet, "/api/library/chats/"+id, nil), libUser)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		fx.h.GetLibraryChat(w, r)
		return w
	}
	w := get(libChatID)
	if w.Code != http.StatusOK {
		t.Fatalf("own chat: %d %s", w.Code, w.Body.String())
	}
	var item struct {
		ID      string   `json:"id"`
		FileIDs []string `json:"fileIds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.ID != libChatID || len(item.FileIDs) != 1 || item.FileIDs[0] != libFileA {
		t.Fatalf("item = %+v", item)
	}
	for _, id := range []string{kbChat, libNewChatID, uuid.NewString(), "bad"} {
		if w := get(id); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", id, w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Fix round 1
// ---------------------------------------------------------------------------

// A library turn streams and persists snippet-capped sources, while citation
// validation still reads the full page text.
func TestSendLibraryMessage_SourcesSnippetCappedValidationFull(t *testing.T) {
	long := strings.Repeat("Der Himmel ist blau und weit. ", 100) // 3000 runes, one page
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var mu sync.Mutex
			var validated []ChatSource
			old := libraryCitationValidator
			t.Cleanup(func() { libraryCitationValidator = old })
			libraryCitationValidator = func(ctx context.Context, answer string, sources []ChatSource, sem *SemanticConfig) []CitationStatus {
				mu.Lock()
				validated = append([]ChatSource(nil), sources...)
				mu.Unlock()
				return old(ctx, answer, sources, sem)
			}

			fx := newLibChatFixture(t, nil)
			fx.text.texts[libFileA] = &parser.ParseResult{Text: long}
			w := fx.send(t, `{"message":"Himmel?","fileIds":["`+libFileA+`"]}`, stream)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}

			var wire []any
			if stream {
				wire, _ = openingFrame(sseFrames(t, w.Body.String()))["sources"].([]any)
			} else {
				var resp map[string]any
				_ = json.Unmarshal(w.Body.Bytes(), &resp)
				wire, _ = resp["sources"].([]any)
			}
			if len(wire) != 1 {
				t.Fatalf("wire sources = %v", wire)
			}
			wc, _ := wire[0].(map[string]any)["content"].(string)
			if n := len([]rune(wc)); n != librarySourceSnippetRunes {
				t.Errorf("wire content = %d runes, want %d", n, librarySourceSnippetRunes)
			}

			var persisted []ChatSource
			for _, m := range fx.store.added {
				if m.Role == "ai" {
					persisted = m.Sources
				}
			}
			if len(persisted) != 1 || len([]rune(persisted[0].Content)) != librarySourceSnippetRunes {
				t.Fatalf("persisted sources = %+v", persisted)
			}
			if persisted[0].UserFileID != libFileA {
				t.Errorf("persisted userFileId = %q", persisted[0].UserFileID)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(validated) != 1 || validated[0].Content != strings.TrimSpace(long) {
				t.Fatalf("validator got %d sources, content %d runes; want the full page", len(validated), len([]rune(validated[0].Content)))
			}
		})
	}
}

func TestWireSources_KBTurnUnchanged(t *testing.T) {
	src := []ChatSource{{Index: 1, Content: strings.Repeat("x", 5000)}}
	got := chatResponseParams{chatCtx: &ChatContext{Sources: src}}.wireSources()
	if &got[0] != &src[0] || len(got[0].Content) != 5000 {
		t.Fatal("a KB turn must stream and persist its own, uncapped slice")
	}
	lib := chatResponseParams{library: true, chatCtx: &ChatContext{Sources: src}}.wireSources()
	if &lib[0] == &src[0] || len(lib[0].Content) != librarySourceSnippetRunes || len(src[0].Content) != 5000 {
		t.Fatal("a library turn must cap a copy, leaving the in-memory sources full")
	}
}

// A parentMessageId from another chat must neither steer the history walk nor
// be stored: the turn falls back to the chat's own linear history.
func TestSendLibraryMessage_ForeignParentFallsBackToOwnHistory(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.seedLibraryChat(libChatID, libUser, libFileA)
	foreignMsg := uuid.NewString()
	fx.store.msgChat[foreignMsg] = "99999999-9999-4999-8999-999999999999"
	// What an unscoped ancestor walk from the foreign parent would yield.
	fx.store.ancestors = []MessageRow{{ID: foreignMsg, Role: "user", Content: "GEHEIMNIS-DES-ANDEREN"}}
	fx.store.messages = []MessageRow{{ID: uuid.NewString(), ChatID: libChatID, Role: "user", Content: "EIGENE-FRAGE"}}

	w := fx.send(t, `{"message":"Weiter?","chatId":"`+libChatID+`","parentMessageId":"`+foreignMsg+`"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var sawOwn bool
	for _, b := range fx.ai.snapshot() {
		if strings.Contains(b, "GEHEIMNIS-DES-ANDEREN") {
			t.Fatalf("foreign message reached the model: %s", b)
		}
		sawOwn = sawOwn || strings.Contains(b, "EIGENE-FRAGE")
	}
	if !sawOwn {
		t.Error("the chat's own linear history was not used")
	}
	for _, p := range fx.store.added {
		if p.Role == "user" && p.ParentMessageID != nil {
			t.Errorf("user message stored with parent %q, want nil", *p.ParentMessageID)
		}
	}
}

func TestParentInChat(t *testing.T) {
	st := newLibStore()
	own, foreign := uuid.NewString(), uuid.NewString()
	st.msgChat[own] = libChatID
	st.msgChat[foreign] = libNewChatID
	h := &Handler{store: st}
	ctx := context.Background()
	if got := h.parentInChat(ctx, libChatID, &own); got == nil || *got != own {
		t.Errorf("own parent dropped: %v", got)
	}
	if got := h.parentInChat(ctx, libChatID, &foreign); got != nil {
		t.Errorf("foreign parent kept: %v", *got)
	}
	if got := h.parentInChat(ctx, libChatID, nil); got != nil {
		t.Error("nil parent became non-nil")
	}
	// A store without the check keeps the id (the store itself is chat-scoped).
	plain := &Handler{store: newMockStore()}
	if got := plain.parentInChat(ctx, libChatID, &foreign); got == nil {
		t.Error("store without MessageInChat must keep the id")
	}
}

// A file deleted between validation and the refs write (FK violation) is a
// 404 like any missing file, and the just-created chat is removed again.
func TestSendLibraryMessage_RefsRaceIs404AndLeavesNoChat(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	fx.store.refsErr = fmt.Errorf("ReplaceChatFileRefs insert: %w", ErrChatFileRefGone)
	w := fx.send(t, `{"message":"x","fileIds":["`+libFileA+`"]}`, false)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "file not found") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(fx.store.chats) != 0 || len(fx.store.deleted) != 1 || fx.store.deleted[0] != libNewChatID {
		t.Errorf("chats = %v, deleted = %v", fx.store.chats, fx.store.deleted)
	}
	if len(fx.usage.snapshot()) != 0 {
		t.Error("usage recorded on a rejected turn")
	}

	// An existing chat is never deleted on a refs failure.
	fx2 := newLibChatFixture(t, nil)
	fx2.seedLibraryChat(libChatID, libUser, libFileB)
	fx2.store.refsErr = errors.New("db down")
	w2 := fx2.send(t, `{"message":"x","chatId":"`+libChatID+`","fileIds":["`+libFileA+`"]}`, false)
	if w2.Code != http.StatusInternalServerError || len(fx2.store.deleted) != 0 {
		t.Fatalf("existing chat: %d, deleted %v", w2.Code, fx2.store.deleted)
	}
}

func TestChatResponseParams_LowConfidenceNeverForLibrary(t *testing.T) {
	if !(chatResponseParams{}).lowConfidence(1) {
		t.Error("KB turn with 1 source must stay low confidence")
	}
	if (chatResponseParams{}).lowConfidence(3) {
		t.Error("KB turn with 3 sources is not low confidence")
	}
	if (chatResponseParams{library: true}).lowConfidence(1) {
		t.Error("a library turn must never count as low confidence")
	}
}

// Stream mode: library_prepare, then one flushed library_parse per file in
// selection order, then the opening frame.
func TestSendLibraryMessage_StreamParseProgressFrames(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	w := fx.send(t, `{"message":"x","fileIds":["`+libFileA+`","`+libFileB+`"]}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	frames := sseFrames(t, w.Body.String())
	if len(frames) < 4 || frames[0]["stage"] != "library_prepare" {
		t.Fatalf("frames = %v", frames)
	}
	for i, name := range []string{"alpha.txt", "beta.txt"} {
		f := frames[1+i]
		if f["stage"] != "library_parse" || f["file"] != name || f["index"] != float64(i) || f["total"] != float64(2) {
			t.Errorf("frame %d = %v", 1+i, f)
		}
	}
	if frames[3]["chatId"] != libNewChatID {
		t.Errorf("frame 3 = %v, want the opening frame", frames[3])
	}
}

func TestSendLibraryMessage_NonStreamHasNoProgressFrames(t *testing.T) {
	fx := newLibChatFixture(t, nil)
	w := fx.send(t, `{"message":"x","fileIds":["`+libFileA+`"]}`, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "library_parse") || strings.Contains(w.Body.String(), "library_prepare") {
		t.Fatalf("progress frame in non-stream body: %s", w.Body.String())
	}
}
