// Unit tests for the GET /api/search HTTP surface.
//
// Oracle: the request/response contract in API.md ("Search") — q trimmed,
// under 3 characters (counted in characters) is 400 and never a listing;
// invalid UTF-8 or a NUL in q is 400; limit per group, default 5, a larger
// value (however large) clamped to 20, below 1 or not an integer 400; a kb_id
// the caller cannot see is 404, not 403; all four groups always present as
// arrays; a request the client cancelled is no server error. The store is a
// fake whose return values the test chooses, so nothing asserted here is
// derived from what the handler happens to do.
//
// What these tests deliberately do NOT prove: which topics a caller may see.
// That rule is SQL, and asserting it against a fake would only re-assert the
// fake. store_pg_integration_test.go proves it against kbaccess.EffectiveRole.

package globalsearch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/globalsearch"
)

type fakeStore struct {
	calls     int
	gotCaller globalsearch.Caller
	gotQuery  globalsearch.Query

	resp *globalsearch.Response
	err  error
}

func (f *fakeStore) Search(_ context.Context, caller globalsearch.Caller, q globalsearch.Query) (*globalsearch.Response, error) {
	f.calls++
	f.gotCaller, f.gotQuery = caller, q
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &globalsearch.Response{Query: q.Text}, nil
}

const testUserID = "0b7f7c1e-6f1e-4c55-9d0e-2a4b9f6a0c11"

// get builds GET /api/search with the given raw query parameters and an
// authenticated caller — the state the authenticate middleware leaves behind.
func get(params url.Values, role string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/search?"+params.Encode(), nil)
	return req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: testUserID, Role: role}))
}

func serve(t *testing.T, store globalsearch.Store, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	globalsearch.NewHandler(store).Search(rec, req)
	return rec
}

func TestSearchRequiresAuthentication(t *testing.T) {
	store := &fakeStore{}
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=abc", nil)
	rec := serve(t, store, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if store.calls != 0 {
		t.Fatal("store must not be called without a user")
	}
}

// Below three characters after trimming is a 400 and never reaches the store
// — in particular an empty query is not "match everything".
func TestSearchRejectsShortQueries(t *testing.T) {
	for _, q := range []string{"", " ", "a", "  a  ", "\tä\n", "ab", "  ab  ", "äö"} {
		store := &fakeStore{}
		rec := serve(t, store, get(url.Values{"q": {q}}, auth.RoleUser))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("q=%q: status = %d, want 400", q, rec.Code)
		}
		if store.calls != 0 {
			t.Errorf("q=%q: store was called %d times, want 0", q, store.calls)
		}
	}
	// No q parameter at all is the same case.
	store := &fakeStore{}
	req := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: testUserID, Role: auth.RoleUser}))
	if rec := serve(t, store, req); rec.Code != http.StatusBadRequest || store.calls != 0 {
		t.Errorf("missing q: status = %d, calls = %d, want 400 and 0", rec.Code, store.calls)
	}
}

// The minimum is counted in characters: "äöü" is three characters (six
// bytes) and must be accepted; the trimmed text is what reaches the store.
func TestSearchCountsCharactersAndTrims(t *testing.T) {
	store := &fakeStore{}
	rec := serve(t, store, get(url.Values{"q": {"  äöü "}}, auth.RoleUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if store.gotQuery.Text != "äöü" {
		t.Fatalf("store got q = %q, want the trimmed %q", store.gotQuery.Text, "äöü")
	}
}

// Postgres rejects invalid UTF-8 and NUL in a text parameter; both must be a
// 400 before the store is asked, not a 500 from the database. url.Values
// percent-encodes the raw bytes (%FF, %00) and the server decodes them back,
// exactly as a client sending them would.
func TestSearchRejectsInvalidText(t *testing.T) {
	for _, q := range []string{"ab\xffcd", "\xc3\x28abc", "abc\x00", "\x00abc", "ab\x00cd"} {
		store := &fakeStore{}
		rec := serve(t, store, get(url.Values{"q": {q}}, auth.RoleUser))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("q=%q: status = %d, want 400", q, rec.Code)
		}
		if store.calls != 0 {
			t.Errorf("q=%q: store was called %d times, want 0", q, store.calls)
		}
	}
}

func TestSearchRejectsOverlongQueries(t *testing.T) {
	long := make([]rune, globalsearch.MaxQueryLen+1)
	for i := range long {
		long[i] = 'x'
	}
	store := &fakeStore{}
	rec := serve(t, store, get(url.Values{"q": {string(long)}}, auth.RoleUser))
	if rec.Code != http.StatusBadRequest || store.calls != 0 {
		t.Fatalf("status = %d, calls = %d, want 400 and 0", rec.Code, store.calls)
	}
	// Exactly at the bound is fine.
	store = &fakeStore{}
	rec = serve(t, store, get(url.Values{"q": {string(long[:globalsearch.MaxQueryLen])}}, auth.RoleUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("q of exactly MaxQueryLen: status = %d, want 200", rec.Code)
	}
}

// limit is per group, default 5, max 20; any larger positive integer is
// clamped, including one beyond the range of int.
func TestSearchLimit(t *testing.T) {
	cases := []struct {
		raw        string
		wantStatus int
		wantLimit  int
	}{
		{"", http.StatusOK, 5},
		{"1", http.StatusOK, 1},
		{"20", http.StatusOK, 20},
		{"21", http.StatusOK, 20},
		{"1000", http.StatusOK, 20},
		{"99999999999999999999999", http.StatusOK, 20},
		{"+7", http.StatusOK, 7},
		{"0", http.StatusBadRequest, 0},
		{"-3", http.StatusBadRequest, 0},
		{"-99999999999999999999999", http.StatusBadRequest, 0},
		{"abc", http.StatusBadRequest, 0},
		{"5.5", http.StatusBadRequest, 0},
	}
	for _, c := range cases {
		params := url.Values{"q": {"abc"}}
		if c.raw != "" {
			params.Set("limit", c.raw)
		}
		store := &fakeStore{}
		rec := serve(t, store, get(params, auth.RoleUser))
		if rec.Code != c.wantStatus {
			t.Errorf("limit=%q: status = %d, want %d", c.raw, rec.Code, c.wantStatus)
			continue
		}
		if c.wantStatus == http.StatusOK && store.gotQuery.Limit != c.wantLimit {
			t.Errorf("limit=%q: store got limit %d, want %d", c.raw, store.gotQuery.Limit, c.wantLimit)
		}
		if c.wantStatus != http.StatusOK && store.calls != 0 {
			t.Errorf("limit=%q: store was called on a 400", c.raw)
		}
	}
}

// The caller handed to the store is taken from the auth claims — the same
// user id and system role RequireKBRole hands to kbaccess.EffectiveRole.
func TestSearchPassesTheClaimsCaller(t *testing.T) {
	store := &fakeStore{}
	serve(t, store, get(url.Values{"q": {"abc"}}, auth.RoleSuperAdmin))
	want := globalsearch.Caller{UserID: testUserID, SysRole: auth.RoleSuperAdmin}
	if store.gotCaller != want {
		t.Fatalf("store got caller %+v, want %+v", store.gotCaller, want)
	}
	if store.gotQuery.KBID != "" {
		t.Fatalf("no kb_id given, store got KBID %q", store.gotQuery.KBID)
	}
}

// A malformed kb_id is answered like an id that does not exist: 404, and the
// store is never asked (a raw value would reach $3::uuid and 500).
func TestSearchMalformedKBIDIs404(t *testing.T) {
	for _, id := range []string{"not-a-uuid", "123", "'; DROP TABLE files; --"} {
		store := &fakeStore{}
		rec := serve(t, store, get(url.Values{"q": {"abc"}, "kb_id": {id}}, auth.RoleUser))
		if rec.Code != http.StatusNotFound {
			t.Errorf("kb_id=%q: status = %d, want 404", id, rec.Code)
		}
		if store.calls != 0 {
			t.Errorf("kb_id=%q: store was called", id)
		}
	}
}

// A well-formed kb_id reaches the store in canonical form.
func TestSearchCanonicalisesKBID(t *testing.T) {
	store := &fakeStore{}
	rec := serve(t, store, get(url.Values{"q": {"abc"}, "kb_id": {"{6F9619FF-8B86-D011-B42D-00C04FC964FF}"}}, auth.RoleUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if want := "6f9619ff-8b86-d011-b42d-00c04fc964ff"; store.gotQuery.KBID != want {
		t.Fatalf("store got KBID %q, want %q", store.gotQuery.KBID, want)
	}
}

// kb_id the caller cannot see -> 404, not 403.
func TestSearchInvisibleKBIs404(t *testing.T) {
	store := &fakeStore{err: globalsearch.ErrNotFound}
	rec := serve(t, store, get(url.Values{"q": {"abc"}, "kb_id": {testUserID}}, auth.RoleUser))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestSearchStoreErrorIs500(t *testing.T) {
	store := &fakeStore{err: errors.New("boom")}
	rec := serve(t, store, get(url.Values{"q": {"abc"}}, auth.RoleUser))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// A client that goes away mid-search (closes the dropdown, types the next
// character) cancels the request context, and pgx returns an error wrapping
// context.Canceled. That is not a server error: no 500 and no ERROR log line.
// The guard is the request context itself, so a store error that merely
// wraps context.Canceled while the request is still live stays a 500.
func TestSearchClientCancelIsNotAServerError(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := get(url.Values{"q": {"abc"}}, auth.RoleUser)
	req = req.WithContext(auth.WithUser(ctx, &auth.Claims{ID: testUserID, Role: auth.RoleUser}))
	store := &fakeStore{err: fmt.Errorf("search topics: %w", context.Canceled)}
	rec := serve(t, store, req)
	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("status = 500 for a cancelled request")
	}
	if rec.Code != 499 {
		t.Errorf("status = %d, want 499 (client closed request)", rec.Code)
	}
	if store.calls != 1 {
		t.Errorf("store calls = %d, want 1 (the cancellation came from the store)", store.calls)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a cancelled request logged at ERROR:\n%s", logs.String())
	}

	// Same error, live request: a genuine failure, still a 500.
	logs.Reset()
	store = &fakeStore{err: fmt.Errorf("search topics: %w", context.Canceled)}
	rec = serve(t, store, get(url.Values{"q": {"abc"}}, auth.RoleUser))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("live request, store error wrapping context.Canceled: status = %d, want 500", rec.Code)
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("live request failure was not logged at ERROR")
	}
}

// All four groups are always present as arrays — [] not null, the dropdown
// maps over them — even when the store returns nil.
func TestSearchResponseShape(t *testing.T) {
	store := &fakeStore{resp: &globalsearch.Response{Query: "abc"}} // nil slices on purpose
	rec := serve(t, store, get(url.Values{"q": {"abc"}}, auth.RoleUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"topics", "sources", "chats", "messages"} {
		if got, ok := body[key]; !ok || string(got) != "[]" {
			t.Errorf("%s = %s (present=%v), want []", key, got, ok)
		}
	}
	if got := string(body["query"]); got != `"abc"` {
		t.Errorf("query = %s, want \"abc\"", got)
	}
	if got := string(body["kbId"]); got != "null" {
		t.Errorf("kbId = %s, want null for a global search", got)
	}
}

// The chat and message fields are serialised under the documented names.
func TestSearchChatAndMessageFieldNames(t *testing.T) {
	store := &fakeStore{resp: &globalsearch.Response{
		Query:    "abc",
		Chats:    []globalsearch.ChatHit{{ID: "c1", Title: "t", Type: "chat", KBID: "k", KBName: "n", Match: globalsearch.MatchPrefix}},
		Messages: []globalsearch.MessageHit{{ID: "m1", ChatID: "c1", ChatTitle: "t", ChatType: "research", KBID: "k", KBName: "n", Role: "ai", Snippet: "s"}},
	}}
	rec := serve(t, store, get(url.Values{"q": {"abc"}}, auth.RoleUser))
	var body struct {
		Chats    []map[string]json.RawMessage `json:"chats"`
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Chats) != 1 || len(body.Messages) != 1 {
		t.Fatalf("body = %s", rec.Body)
	}
	for _, key := range []string{"id", "title", "type", "kbId", "kbName", "updatedAt", "match"} {
		if _, ok := body.Chats[0][key]; !ok {
			t.Errorf("chat hit lacks %q: %s", key, rec.Body)
		}
	}
	for _, key := range []string{"id", "chatId", "chatTitle", "chatType", "kbId", "kbName", "role", "snippet", "createdAt"} {
		if _, ok := body.Messages[0][key]; !ok {
			t.Errorf("message hit lacks %q: %s", key, rec.Body)
		}
	}
}
