package adkbridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/mcp"
)

func offered() []Action {
	return []Action{
		{ID: "web", Tool: "web_search", Label: "Web", Args: map[string]any{"query": "server query"}},
		{ID: "conf", Tool: "confluence_import", Label: "Confluence"},
		{ID: "upload", Tool: "library_add_to_kb", FrontendTool: "upload_file", Label: "Upload"},
	}
}

func capture(got *map[string]any) DispatchFunc {
	return func(_ context.Context, _, _ string, args json.RawMessage) (mcp.ToolResult, error) {
		_ = json.Unmarshal(args, got)
		return mcp.ToolResult{Text: "ok"}, nil
	}
}

func TestExecuteActionResolvesByIDAndServerArgsWin(t *testing.T) {
	var got map[string]any
	ctx := WithScope(context.Background(), Scope{UserID: "u", KBID: "kb", ChatID: "c", Role: "view"})
	_, err := ExecuteAction(ctx, offered(), ActionChoice{ActionID: "web", Args: map[string]any{"query": "client", "kb_id": "evil"}},
		map[string]DispatchFunc{"web_search": capture(&got)})
	if err != nil {
		t.Fatal(err)
	}
	if got["query"] != "server query" || got["kb_id"] != "kb" || got["chat_id"] != "c" {
		t.Fatalf("args = %v", got)
	}
}

func TestForgedActionIsRefused(t *testing.T) {
	ctx := WithScope(context.Background(), Scope{UserID: "u", KBID: "kb", Role: "edit"})
	n := 0
	d := func(context.Context, string, string, json.RawMessage) (mcp.ToolResult, error) {
		n++
		return mcp.ToolResult{}, nil
	}
	if _, err := ExecuteAction(ctx, offered(), ActionChoice{ActionID: "code"}, map[string]DispatchFunc{"code_exec": d}); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("err = %v", err)
	}
	// offered but no dispatcher
	if _, err := ExecuteAction(ctx, offered(), ActionChoice{ActionID: "web"}, map[string]DispatchFunc{}); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("missing dispatcher err = %v", err)
	}
	// offered but role too low
	ctx = WithScope(context.Background(), Scope{UserID: "u", KBID: "kb", Role: "view"})
	if _, err := ExecuteAction(ctx, offered(), ActionChoice{ActionID: "conf", Args: map[string]any{"spaceKey": "X"}},
		map[string]DispatchFunc{"confluence_import": d}); !errors.Is(err, ErrForbiddenTool) {
		t.Fatalf("err = %v", err)
	}
	// no scope
	if _, err := ExecuteAction(context.Background(), offered(), ActionChoice{ActionID: "web"},
		map[string]DispatchFunc{"web_search": d}); !errors.Is(err, ErrNoScope) {
		t.Fatalf("no scope err = %v", err)
	}
	if n != 0 {
		t.Fatal("dispatched a refused action")
	}
}

func TestExecuteActionDispatchErrorIsOpaque(t *testing.T) {
	ctx := WithScope(context.Background(), Scope{UserID: "u", KBID: "kb", Role: "edit"})
	d := func(context.Context, string, string, json.RawMessage) (mcp.ToolResult, error) {
		return mcp.ToolResult{}, errors.New("pq: password=secret")
	}
	_, err := ExecuteAction(ctx, offered(), ActionChoice{ActionID: "web"}, map[string]DispatchFunc{"web_search": d})
	if err == nil || err.Error() != "tool failed" {
		t.Fatalf("err = %v", err)
	}
}

func TestArgsWhitelist(t *testing.T) {
	var got map[string]any
	ctx := WithScope(context.Background(), Scope{UserID: "u", KBID: "kb", Role: "edit"})
	_, err := ExecuteAction(ctx, offered(), ActionChoice{ActionID: "upload", Args: map[string]any{"userFileIds": []any{"f1"}, "sql": "drop"}},
		map[string]DispatchFunc{"library_add_to_kb": capture(&got)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["sql"]; ok || got["userFileIds"] == nil {
		t.Fatalf("args = %v", got)
	}
}

func TestDecodeChoice(t *testing.T) {
	if _, ok := DecodeChoice(map[string]any{"cancelled": true}); ok {
		t.Fatal("cancelled must not decode as a choice")
	}
	c, ok := DecodeChoice(map[string]any{"actionId": "web", "args": map[string]any{"a": 1.0}})
	if !ok || c.ActionID != "web" || c.Args["a"] != 1.0 {
		t.Fatalf("got %+v %v", c, ok)
	}
	if _, ok := DecodeChoice("garbage"); ok {
		t.Fatal("malformed payload decoded")
	}
	if _, ok := DecodeChoice(map[string]any{"args": map[string]any{}}); ok {
		t.Fatal("missing actionId decoded")
	}
}

func TestServerArgsOverrideWhitelistedClientArgs(t *testing.T) {
	var got map[string]any
	ctx := WithScope(context.Background(), Scope{UserID: "u", KBID: "kb", Role: "edit"})
	acts := []Action{{ID: "conf", Tool: "confluence_import", Args: map[string]any{"spaceKey": "SERVER"}}}
	_, err := ExecuteAction(ctx, acts, ActionChoice{ActionID: "conf", Args: map[string]any{"spaceKey": "CLIENT", "rootPageId": "7"}},
		map[string]DispatchFunc{"confluence_import": capture(&got)})
	if err != nil {
		t.Fatal(err)
	}
	if got["spaceKey"] != "SERVER" || got["rootPageId"] != "7" {
		t.Fatalf("args = %v", got)
	}
}

func TestDecodeChoiceRejectsNonMapArgs(t *testing.T) {
	if _, ok := DecodeChoice(map[string]any{"actionId": "web", "args": "x"}); ok {
		t.Fatal("non-map args decoded")
	}
}
