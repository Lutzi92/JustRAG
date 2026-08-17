package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/usage"
)

type fakeCfg struct{ enabled string } // "" => key absent

func (f fakeCfg) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if key != "mcp_server_enabled" || f.enabled == "" {
		return nil, nil
	}
	v := f.enabled
	return &v, nil
}

func newReq(kbID, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/kb/"+kbID+"/mcp", strings.NewReader(body))
	r.SetPathValue("id", kbID)
	return r
}

func TestHandler_DisabledReturns404(t *testing.T) {
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: ""})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq("kb-1", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_Initialize(t *testing.T) {
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: "true"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq("kb-1", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error != nil {
		t.Fatalf("resp = %s err=%v", rec.Body.String(), err)
	}
	var result struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if result.ProtocolVersion == "" {
		t.Error("protocolVersion empty")
	}
	if _, ok := result.Capabilities["tools"]; !ok {
		t.Error("capabilities.tools missing")
	}
}

func TestHandler_ToolsList(t *testing.T) {
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: "true"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq("kb-1", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp rpcResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	var result struct {
		Tools []toolDescriptor `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Name != "ask_kb" {
		t.Fatalf("tools = %+v", result.Tools)
	}
}

func TestHandler_ToolsCall_InjectsPathKBID(t *testing.T) {
	fa := &fakeAnswerer{result: AnswerResult{Answer: "ok"}}
	h := NewHandler(fa, fakeCfg{enabled: "true"})
	rec := httptest.NewRecorder()
	// kb_id in params must be ignored; path kb wins.
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_kb","arguments":{"question":"hi","kb_id":"EVIL"}}}`
	h.ServeHTTP(rec, newReq("kb-real", body))
	var resp rpcResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	if fa.gotKB != "kb-real" {
		t.Errorf("kbID = %q, want kb-real (path), not from params", fa.gotKB)
	}
}

func TestHandler_UnknownMethod(t *testing.T) {
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: "true"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq("kb-1", `{"jsonrpc":"2.0","id":4,"method":"nope"}`))
	var resp rpcResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("error = %+v, want method-not-found", resp.Error)
	}
}

func TestHandler_UnknownTool(t *testing.T) {
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: "true"})
	rec := httptest.NewRecorder()
	body := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"bogus","arguments":{}}}`
	h.ServeHTTP(rec, newReq("kb-1", body))
	var resp rpcResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("error = %+v, want method-not-found for unknown tool", resp.Error)
	}
}

func TestHandler_BadJSON(t *testing.T) {
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: "true"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq("kb-1", `{not json`))
	var resp rpcResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeParse {
		t.Fatalf("error = %+v, want parse error", resp.Error)
	}
}

func TestHandler_MissingQuestionIsInvalidParams(t *testing.T) {
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: "true"})
	rec := httptest.NewRecorder()
	body := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"ask_kb","arguments":{}}}`
	h.ServeHTTP(rec, newReq("kb-1", body))
	var resp rpcResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Fatalf("error = %+v, want invalid params", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// Usage ledger (Task 7): one usage_events row per ask_kb tools/call.
// ---------------------------------------------------------------------------

const (
	testKBID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testUserID = "user-1"
)

// fakeUsageRecorder captures usage events for assertions. Same shape as
// Task 6's openaicompat fakeUsageRecorder.
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

// TestServeHTTP_ToolsCallRecordsOneUsageEvent: the fake Answerer makes this a
// genuinely successful tools/call, so this pins the real happy path — one
// usage event, tagged mcp, carrying the exact fixture kb/user/api-key values.
func TestServeHTTP_ToolsCallRecordsOneUsageEvent(t *testing.T) {
	rec := &fakeUsageRecorder{}
	fa := &fakeAnswerer{result: AnswerResult{Answer: "ok"}}
	h := NewHandler(fa, fakeCfg{enabled: "true"})
	h.SetUsageRecorder(rec)

	keyID := "44444444-4444-4444-4444-444444444444"
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"ask_kb","arguments":{"question":"hallo"}}}`
	req := newReq(testKBID, body)
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: testUserID, Username: "u", Role: "user"})
	ctx = auth.WithAPIKeyID(ctx, keyID)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req.WithContext(ctx))

	var resp rpcResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || resp.Error != nil {
		t.Fatalf("resp = %s err=%v", rr.Body.String(), err)
	}

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("usage events: got %d, want 1", len(events))
	}
	if events[0].Surface != usage.SurfaceMCP {
		t.Errorf("surface: got %q, want mcp", events[0].Surface)
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

// TestServeHTTP_HandshakesRecordNothing: initialize and tools/list are
// protocol handshakes, not turns, and must not be counted.
func TestServeHTTP_HandshakesRecordNothing(t *testing.T) {
	rec := &fakeUsageRecorder{}
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: "true"})
	h.SetUsageRecorder(rec)

	for _, method := range []string{"initialize", "tools/list"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"}`
		req := newReq(testKBID, body)
		ctx := auth.WithUser(req.Context(), &auth.Claims{ID: testUserID, Username: "u", Role: "user"})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req.WithContext(ctx))
	}

	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("usage events for handshake methods: got %d, want 0", got)
	}
}

// TestServeHTTP_UnknownToolRecordsNothing: a tools/call naming a tool that
// does not exist never reaches the RAG pipeline (runAskKB) and must not be
// counted — the guard for the Record call sitting after tool-name validation.
func TestServeHTTP_UnknownToolRecordsNothing(t *testing.T) {
	rec := &fakeUsageRecorder{}
	h := NewHandler(&fakeAnswerer{}, fakeCfg{enabled: "true"})
	h.SetUsageRecorder(rec)

	body := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"bogus","arguments":{}}}`
	req := newReq(testKBID, body)
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: testUserID, Username: "u", Role: "user"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req.WithContext(ctx))

	var resp rpcResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("error = %+v, want method-not-found for unknown tool", resp.Error)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("usage events for unknown tool: got %d, want 0", got)
	}
}
