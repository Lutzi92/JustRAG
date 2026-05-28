package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubHandler is a tiny ToolHandler that records the args it received
// and returns a configurable result/error. Used by the dispatch tests
// to exercise validation + telemetry without wiring real tools.
type stubHandler struct {
	gotArgs json.RawMessage
	res     ToolResult
	err     error
}

func (s *stubHandler) Invoke(_ context.Context, args json.RawMessage) (ToolResult, error) {
	s.gotArgs = args
	return s.res, s.err
}

func TestRegistry_DispatchUnknownTool(t *testing.T) {
	r := NewRegistry()
	_, err := r.Dispatch(context.Background(), "kb1", "no_such_tool", json.RawMessage(`{}`))
	if err == nil || !errors.Is(err, ErrUnknownTool) {
		t.Errorf("dispatch unknown tool: want ErrUnknownTool, got %v", err)
	}
}

func TestRegistry_DispatchSchemaValidationRejectsMissingRequired(t *testing.T) {
	h := &stubHandler{}
	r := NewRegistry()
	r.RegisterBuiltin(Tool{
		Name:        "demo",
		InputSchema: json.RawMessage(`{"type":"object","required":["query"]}`),
		Handler:     h,
	})
	_, err := r.Dispatch(context.Background(), "kb1", "demo", json.RawMessage(`{"other":"x"}`))
	if err == nil {
		t.Fatalf("expected schema_error, got nil")
	}
	if !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("error should mention missing field, got %v", err)
	}
	if h.gotArgs != nil {
		t.Errorf("handler should NOT have been invoked when validation fails; got args=%s", string(h.gotArgs))
	}
}

func TestRegistry_DispatchSchemaValidationAcceptsValidArgs(t *testing.T) {
	want := ToolResult{Text: "hello"}
	h := &stubHandler{res: want}
	r := NewRegistry()
	r.RegisterBuiltin(Tool{
		Name:        "demo",
		InputSchema: json.RawMessage(`{"type":"object","required":["query"]}`),
		Handler:     h,
	})
	got, err := r.Dispatch(context.Background(), "kb1", "demo", json.RawMessage(`{"query":"hi"}`))
	if err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}
	if got.Text != want.Text {
		t.Errorf("result text = %q, want %q", got.Text, want.Text)
	}
}

func TestRegistry_BuiltinShadowsRemoteName(t *testing.T) {
	r := NewRegistry()
	bh := &stubHandler{res: ToolResult{Text: "builtin"}}
	r.RegisterBuiltin(Tool{Name: "kb_search", Handler: bh})

	// Simulate a remote server advertising kb_search by injecting it
	// directly into the per-KB map — Configure() would log+skip but we
	// want to verify the resolution rule works either way.
	r.mu.Lock()
	r.perKB["kb1"] = map[string]Tool{
		"kb_search": {
			Name:    "kb_search",
			Origin:  "remote:bad",
			Handler: &stubHandler{res: ToolResult{Text: "remote"}},
		},
	}
	r.mu.Unlock()

	got, err := r.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got.Text != "builtin" {
		t.Errorf("built-in must shadow remote tool of the same name; got %q", got.Text)
	}
}

// TestRegistry_DispatchHonorsContextDeadline simulates a hung server by
// blocking the handler until the parent context's timeout elapses. The
// dispatch layer's per-call timeout would otherwise let the call run for
// 10s; using a 50ms parent context makes the test fast.
func TestRegistry_DispatchHonorsContextDeadline(t *testing.T) {
	hung := ToolHandlerFunc(func(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
		<-ctx.Done()
		return ToolResult{}, ctx.Err()
	})
	r := NewRegistry()
	r.RegisterBuiltin(Tool{Name: "hang", Handler: hung})

	parentCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Dispatch(parentCtx, "kb1", "hang", json.RawMessage(`{}`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout/cancel error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("dispatch did not respect parent deadline: ran for %v", elapsed)
	}
}

func TestClient_CallToolHTTPSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result []byte
		switch req.Method {
		case "initialize":
			result = []byte(`{}`)
		case "tools/call":
			result = []byte(`{"content":[{"type":"text","text":"ok-from-server"}]}`)
		default:
			result = []byte(`null`)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + string(result) + `}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	got, err := c.CallTool(context.Background(), "anything", json.RawMessage(`{"q":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got.Text != "ok-from-server" {
		t.Errorf("Text = %q, want %q", got.Text, "ok-from-server")
	}
}
