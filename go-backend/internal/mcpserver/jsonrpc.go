// Package mcpserver exposes a JustRAG knowledge base as a Model Context
// Protocol (MCP) server: external agents (Claude, Cursor, Copilot) reach a
// KB through the single `ask_kb` tool over JSON-RPC 2.0 (Streamable HTTP).
//
// This is the server counterpart to internal/mcp (which is the MCP *client*).
package mcpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// JSON-RPC 2.0 error codes (subset we emit).
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// rpcRequest is one inbound JSON-RPC 2.0 request. ID is kept raw so we can
// echo it back verbatim (it may be a number, string, or null).
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is one outbound JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		writeRPCError(w, id, codeInternal, "failed to marshal result")
		return
	}
	writeJSON(w, rpcResponse{JSONRPC: "2.0", ID: id, Result: raw})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeJSON(w, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("mcpserver: encode response", "error", err)
	}
}
