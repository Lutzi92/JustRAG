package adkbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolutils"
	"google.golang.org/genai"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/mcp"
)

// DispatchFunc executes one tool call for kbID.
type DispatchFunc func(ctx context.Context, kbID, name string, args json.RawMessage) (mcp.ToolResult, error)

// ToolSpec describes one tool exposed to an ADK agent.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Policy      ToolPolicy
}

// ErrForbiddenTool wraps every policy refusal at dispatch.
var ErrForbiddenTool = errors.New("adkbridge: tool forbidden by policy")

type dispatchTool struct {
	spec     ToolSpec
	dispatch DispatchFunc
}

// NewTool wraps a dispatcher-backed tool as an ADK function tool.
func NewTool(spec ToolSpec, dispatch DispatchFunc) tool.Tool {
	// A zero Policy would fail open (no approval, no role floor).
	if spec.Policy == (ToolPolicy{}) {
		spec.Policy = PolicyFor(spec.Name)
	}
	return &dispatchTool{spec: spec, dispatch: dispatch}
}

// RegistryTools exposes the named tools of reg for kbID, each with
// PolicyFor(name). Unknown names are an error.
func RegistryTools(reg *mcp.Registry, kbID string, names []string) ([]tool.Tool, error) {
	out := make([]tool.Tool, 0, len(names))
	for _, n := range names {
		t, ok := reg.Get(kbID, n)
		if !ok {
			return nil, fmt.Errorf("adkbridge: unknown tool %q", n)
		}
		out = append(out, NewTool(ToolSpec{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, Policy: PolicyFor(n)},
			func(ctx context.Context, kbID, name string, args json.RawMessage) (mcp.ToolResult, error) {
				return reg.Dispatch(ctx, kbID, name, args)
			}))
	}
	return out, nil
}

func (t *dispatchTool) Name() string        { return t.spec.Name }
func (t *dispatchTool) Description() string { return t.spec.Description }
func (t *dispatchTool) IsLongRunning() bool { return false }

// ProcessRequest packs the declaration into the LLM request.
func (t *dispatchTool) ProcessRequest(_ agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, t)
}

// Declaration is what the model sees.
func (t *dispatchTool) Declaration() *genai.FunctionDeclaration {
	d := &genai.FunctionDeclaration{Name: t.spec.Name, Description: t.spec.Description}
	if len(t.spec.InputSchema) > 0 {
		d.ParametersJsonSchema = t.spec.InputSchema
	}
	return d
}

// Run executes the tool: the scope, role and privilege checks first (they
// need no args, and a call that can never run must not ask the user for
// approval), then approval (pauses the run), then dispatch. Policy refusals
// and dispatch errors are returned to the model as results so it can
// explain or recover.
func (t *dispatchTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	if _, err := t.checkPolicy(ctx); err != nil {
		return map[string]any{"error": modelVisibleError(ctx, t.spec.Name, err)}, nil
	}
	if t.spec.Policy.Approval != ApprovalNever {
		if c := ctx.ToolConfirmation(); c != nil {
			if !c.Confirmed {
				return nil, fmt.Errorf("tool %q: %w", t.spec.Name, tool.ErrConfirmationRejected)
			}
		} else {
			if err := ctx.RequestConfirmation(fmt.Sprintf("Approve calling %s?", t.spec.Name), args); err != nil {
				return nil, err
			}
			ctx.Actions().SkipSummarization = true
			return nil, fmt.Errorf("tool %q: %w", t.spec.Name, tool.ErrConfirmationRequired)
		}
	}
	m, _ := args.(map[string]any)
	return t.result(ctx, m), nil
}

// result dispatches and shapes the tool result the model sees.
func (t *dispatchTool) result(ctx context.Context, m map[string]any) map[string]any {
	res, err := t.dispatchChecked(ctx, m)
	if err != nil {
		return map[string]any{"error": modelVisibleError(ctx, t.spec.Name, err)}
	}
	out := map[string]any{"result": res.Text}
	if len(res.Chunks) > 0 {
		out["chunks"] = res.Chunks
	}
	if len(res.Structured) > 0 {
		out["structured"] = res.Structured
	}
	return out
}

// modelVisibleError returns the error text the model (and, through the
// AG-UI stream, the client) may see. Policy refusals and unknown tools let
// the model explain or recover; any other dispatch error can carry DB,
// provider or network detail, so it is logged and replaced by "tool failed".
// mcp schema-validation failures have no sentinel and are opaque too.
func modelVisibleError(ctx context.Context, name string, err error) string {
	if errors.Is(err, ErrForbiddenTool) || errors.Is(err, ErrNoScope) || errors.Is(err, mcp.ErrUnknownTool) {
		return err.Error()
	}
	logctx.From(ctx).Error("adkbridge: tool dispatch failed", "tool", name, "error", err)
	return "tool failed"
}

// checkPolicy enforces scope, role and privilege for the running user.
func (t *dispatchTool) checkPolicy(ctx context.Context) (Scope, error) {
	return checkScopePolicy(ctx, t.spec.Name, t.spec.Policy)
}

// checkScopePolicy is the scope, role and privilege check shared by the
// model-driven tool path and ExecuteAction.
func checkScopePolicy(ctx context.Context, name string, pol ToolPolicy) (Scope, error) {
	sc, ok := ScopeFrom(ctx)
	if !ok {
		return Scope{}, ErrNoScope
	}
	if !RoleAtLeast(sc.Role, pol.RequiresRole) {
		return Scope{}, fmt.Errorf("%w: %s requires role %s", ErrForbiddenTool, name, pol.RequiresRole)
	}
	if privilegedInBridge(name) && !sc.AllowPrivileged {
		return Scope{}, fmt.Errorf("%w: %s is privileged", ErrForbiddenTool, name)
	}
	return sc, nil
}

// dispatchChecked re-runs checkPolicy (the scope may differ on a resumed
// run), overwrites the injected ids, then dispatches. Split out so tests
// can call it directly.
func (t *dispatchTool) dispatchChecked(ctx context.Context, args map[string]any) (mcp.ToolResult, error) {
	sc, err := t.checkPolicy(ctx)
	if err != nil {
		return mcp.ToolResult{}, err
	}
	if args == nil {
		args = map[string]any{}
	}
	// Never trust ids from the model: a prompt-injected document could
	// otherwise point a tool at another KB or chat.
	injectScopeIDs(args, sc)
	raw, err := json.Marshal(args)
	if err != nil {
		return mcp.ToolResult{}, fmt.Errorf("adkbridge: marshal args for %q: %w", t.spec.Name, err)
	}
	return t.dispatch(ctx, sc.KBID, t.spec.Name, raw)
}

// injectScopeIDs overwrites kb_id and chat_id with the scope's values; ids
// from the model or client are never trusted.
func injectScopeIDs(args map[string]any, sc Scope) {
	args["kb_id"] = sc.KBID
	if sc.ChatID != "" {
		args["chat_id"] = sc.ChatID
	} else {
		delete(args, "chat_id")
	}
}
