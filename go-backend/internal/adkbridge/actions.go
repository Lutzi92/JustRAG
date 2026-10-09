package adkbridge

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/justrag/go-backend/internal/mcp"
)

// ErrUnknownAction is returned when a chosen action id is not in the
// server-side offered list, or has no dispatcher.
var ErrUnknownAction = errors.New("adkbridge: action not offered")

// ActionChoice is the user's decision, decoded from the resume payload
// {"actionId": "...", "args": {...}}.
type ActionChoice struct {
	ActionID string
	Args     map[string]any
}

// allowedArgs whitelists the client-supplied args per tool; everything else
// is dropped. Server-defined Action.Args always win on top.
var allowedArgs = map[string][]string{
	"library_add_to_kb": {"userFileIds"},
	"confluence_import": {"spaceKey", "rootPageId"},
	"web_search":        {},
}

// DecodeChoice reads a resume payload. It returns false for a cancelled
// choice and for a malformed payload.
func DecodeChoice(payload any) (ActionChoice, bool) {
	m, ok := payload.(map[string]any)
	if !ok {
		return ActionChoice{}, false
	}
	if c, _ := m["cancelled"].(bool); c {
		return ActionChoice{}, false
	}
	id, _ := m["actionId"].(string)
	if id == "" {
		return ActionChoice{}, false
	}
	var args map[string]any
	if raw, present := m["args"]; present {
		var ok bool
		if args, ok = raw.(map[string]any); !ok {
			return ActionChoice{}, false
		}
	}
	return ActionChoice{ActionID: id, Args: args}, true
}

// ExecuteAction runs the suggestion the user chose. The action is resolved by
// id from the server-side offered list (a tool name from the client is never
// trusted), client args are whitelisted and overridden by the action's own
// args, and the same scope/role/privilege checks as a model-driven call
// apply. No approval is requested: the explicit choice is the approval.
func ExecuteAction(ctx context.Context, offered []Action, choice ActionChoice, dispatch map[string]DispatchFunc) (mcp.ToolResult, error) {
	var act *Action
	for i := range offered {
		if offered[i].ID == choice.ActionID {
			act = &offered[i]
			break
		}
	}
	if act == nil || act.Tool == "" {
		return mcp.ToolResult{}, ErrUnknownAction
	}
	fn := dispatch[act.Tool]
	if fn == nil {
		return mcp.ToolResult{}, ErrUnknownAction
	}
	sc, err := checkScopePolicy(ctx, act.Tool, PolicyFor(act.Tool))
	if err != nil {
		return mcp.ToolResult{}, err
	}
	args := map[string]any{}
	for _, k := range allowedArgs[act.Tool] {
		if v, ok := choice.Args[k]; ok {
			args[k] = v
		}
	}
	for k, v := range act.Args {
		args[k] = v
	}
	injectScopeIDs(args, sc)
	raw, err := json.Marshal(args)
	if err != nil {
		return mcp.ToolResult{}, errors.New("tool failed")
	}
	res, err := fn(ctx, sc.KBID, act.Tool, raw)
	if err != nil {
		if msg := modelVisibleError(ctx, act.Tool, err); msg != "tool failed" {
			return mcp.ToolResult{}, err
		}
		return mcp.ToolResult{}, errors.New("tool failed")
	}
	return res, nil
}
