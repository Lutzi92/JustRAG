package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/justrag/go-backend/internal/httpclient"
)

// jsonRPCRequest is the wire shape the MCP server expects (JSON-RPC 2.0).
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e jsonRPCError) Error() string {
	return fmt.Sprintf("mcp: server error %d: %s", e.Code, e.Message)
}

// Client is a thin JSON-RPC 2.0 over HTTP MCP client. One Client per
// remote server URL; lifetime owned by the Registry.
//
// The MCP spec defines methods like `initialize`, `tools/list`,
// `tools/call`. We hand-roll the subset the registry needs — full SDK
// integration is overkill for the v1 tool-call path.
type Client struct {
	httpClient *http.Client
	url        string
	headers    map[string]string
	id         atomic.Uint64
	// initialized is set after a successful `initialize` call so subsequent
	// operations don't re-handshake on every request.
	initialized atomic.Bool
}

// NewClient builds a Client targeting the given MCP server URL. The
// per-call timeout is enforced via the request context, not the
// http.Client's top-level Timeout — so a long-running tool can outlive
// the default if the orchestrator's context grants it.
func NewClient(url string, headers map[string]string) *Client {
	return &Client{
		httpClient: httpclient.New(0), // context-driven timeouts
		url:        url,
		headers:    headers,
	}
}

// Initialize performs the MCP `initialize` handshake. The response
// payload is currently ignored beyond the success/error check — the
// registry only cares that the server speaks MCP.
func (c *Client) Initialize(ctx context.Context) error {
	if c.initialized.Load() {
		return nil
	}
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "justrag-mcp-client",
			"version": "1.0.0",
		},
	})
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("mcp: initialize: %w", err)
	}
	c.initialized.Store(true)
	return nil
}

// ListTools returns the tools the server advertises. The registry uses
// this on every refresh to populate per-KB tool maps.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: tools/list: %w", err)
	}
	var resp struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("mcp: tools/list parse: %w", err)
	}
	out := make([]Tool, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		out = append(out, Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out, nil
}

// CallTool invokes the named tool on the remote server with the given
// JSON args. The MCP `tools/call` response shape is:
//
//	{"content": [{"type": "text", "text": "..."}], "isError": bool}
//
// We collapse the content array into a single Text field for the
// ToolResult — orchestrators consume the text directly, so the array
// shape is irrelevant beyond joining.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	if err := c.Initialize(ctx); err != nil {
		return ToolResult{}, err
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	params, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": json.RawMessage(args),
	})
	if err != nil {
		return ToolResult{}, fmt.Errorf("mcp: marshal call params: %w", err)
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return ToolResult{}, err
	}
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return ToolResult{}, fmt.Errorf("mcp: tools/call parse: %w", err)
	}
	var buf bytes.Buffer
	for i, c := range resp.Content {
		if c.Type != "text" || c.Text == "" {
			continue
		}
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(c.Text)
	}
	if resp.IsError {
		return ToolResult{Text: buf.String()}, fmt.Errorf("mcp: tool returned isError=true: %s", buf.String())
	}
	return ToolResult{Text: buf.String()}, nil
}

// call posts one JSON-RPC request and returns the result payload. Errors
// are normalized: HTTP-level failures, JSON-RPC-level errors, and parse
// errors all surface as Go errors. The caller adds the method name as
// context.
func (c *Client) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := c.id.Add(1)
	body, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(raw))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	var rpc jsonRPCResponse
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if rpc.Error != nil {
		return nil, *rpc.Error
	}
	return rpc.Result, nil
}

// dialTimeout is the default per-call upper bound applied at the
// registry layer. Individual tools can shorten via context but cannot
// extend.
const dialTimeout = 10 * time.Second
