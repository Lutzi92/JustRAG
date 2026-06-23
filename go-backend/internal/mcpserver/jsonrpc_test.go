package mcpserver

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteResult(t *testing.T) {
	rec := httptest.NewRecorder()
	writeResult(rec, json.RawMessage(`1`), map[string]string{"hello": "world"})

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", resp.JSONRPC)
	}
	if string(resp.ID) != "1" {
		t.Errorf("id = %s, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("error = %+v, want nil", resp.Error)
	}
	var result map[string]string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["hello"] != "world" {
		t.Errorf("result = %v", result)
	}
}

func TestWriteRPCError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRPCError(rec, json.RawMessage(`"abc"`), codeMethodNotFound, "no such method")

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("error = nil, want non-nil")
	}
	if resp.Error.Code != codeMethodNotFound {
		t.Errorf("code = %d, want %d", resp.Error.Code, codeMethodNotFound)
	}
	if resp.Error.Message != "no such method" {
		t.Errorf("message = %q", resp.Error.Message)
	}
	if string(resp.ID) != `"abc"` {
		t.Errorf("id = %s", resp.ID)
	}
}

func TestWriteRPCError_NullID(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRPCError(rec, nil, codeParse, "parse error")

	// JSON-RPC 2.0: when the id cannot be determined, the response MUST
	// carry "id": null — not omit the field.
	if !strings.Contains(rec.Body.String(), `"id":null`) {
		t.Errorf("body = %s, want it to contain \"id\":null", rec.Body.String())
	}
}
