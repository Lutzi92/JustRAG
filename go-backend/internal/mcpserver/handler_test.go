package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
