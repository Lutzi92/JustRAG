package adkbridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/justrag/go-backend/internal/mcp"
)

func searchReturning(chunks []mcp.ResultChunk) DispatchFunc {
	return func(_ context.Context, _, _ string, _ json.RawMessage) (mcp.ToolResult, error) {
		return mcp.ToolResult{Text: "t", Chunks: chunks}, nil
	}
}

func runFlow(t *testing.T, sc Scope, dispatch DispatchFunc, q string) []*session.Event {
	t.Helper()
	ret := RetrieveNode("retrieve", dispatch, 8)
	sug := SuggestNode("suggest", "Nichts gefunden.", []Action{
		{ID: "web", Tool: "web_search", Label: "Web"},
		{ID: "conf", Tool: "confluence_import", Label: "Confluence"},
		{ID: "upload", FrontendTool: "upload_file", Label: "Upload"},
		{ID: "code", Tool: "code_exec", Label: "nope"},
	}, []string{"web_search", "confluence_import", "code_exec"})
	done := workflow.NewFunctionNode("done", func(_ agent.Context, r RetrieveResult) (string, error) { return "ok", nil }, workflow.NodeConfig{})
	a, err := workflowagent.New(workflowagent.Config{Name: "f", Edges: workflow.Concat(
		workflow.Chain(workflow.Start, ret),
		[]workflow.Edge{
			{From: ret, To: done, Route: workflow.StringRoute(RouteFound)},
			{From: ret, To: sug, Route: workflow.StringRoute(RouteNoEvidence)},
		})})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := runner.New(runner.Config{AppName: "t", Agent: a, SessionService: session.InMemoryService(), AutoCreateSession: true})
	var evs []*session.Event
	for ev, err := range r.Run(WithScope(context.Background(), sc), "u", "s", genai.NewContentFromText(q, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func requested(evs []*session.Event) *session.RequestInput {
	for _, ev := range evs {
		if ev.RequestedInput != nil {
			return ev.RequestedInput
		}
	}
	return nil
}

func actionIDs(t *testing.T, req *session.RequestInput) (map[string]any, []string) {
	t.Helper()
	if req == nil {
		t.Fatal("no pause on empty retrieval")
	}
	p := req.Payload.(map[string]any)
	ids := []string{}
	for _, a := range p["actions"].([]Action) {
		ids = append(ids, a.ID)
	}
	return p, ids
}

func TestRetrieveFoundDoesNotPause(t *testing.T) {
	evs := runFlow(t, Scope{UserID: "u", KBID: "kb", Role: "view"}, searchReturning([]mcp.ResultChunk{{ID: "c"}}), "q")
	if requested(evs) != nil {
		t.Fatal("paused despite evidence")
	}
}

func TestSuggestFiltersActionsByRoleAndAllowlist(t *testing.T) {
	// view role: no confluence_import (needs edit); AllowPrivileged shows code_exec.
	evs := runFlow(t, Scope{UserID: "u", KBID: "kb", Role: "view", AllowPrivileged: true}, searchReturning(nil), "q")
	p, ids := actionIDs(t, requested(evs))
	if p["reason"] != RouteNoEvidence || len(ids) != 3 || ids[0] != "web" || ids[1] != "upload" || ids[2] != "code" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestSuggestHidesPrivilegedToolsWithoutAllowPrivileged(t *testing.T) {
	evs := runFlow(t, Scope{UserID: "u", KBID: "kb", Role: "view"}, searchReturning(nil), "q")
	p, ids := actionIDs(t, requested(evs))
	if p["reason"] != RouteNoEvidence || len(ids) != 2 || ids[0] != "web" || ids[1] != "upload" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestRetrieveRejectsLibraryOnlyScope(t *testing.T) {
	n := &retrieveNode{dispatch: searchReturning(nil), topK: 8}
	_, _, err := n.retrieve(WithScope(context.Background(), Scope{UserID: "u", LibraryFileIDs: []string{"f"}}), "q")
	if err != ErrLibraryScopeUnsupported {
		t.Fatalf("err = %v", err)
	}
}

// RetrieveNode dispatches kb_search directly, so it carries kb_search's
// view-role floor itself and fails closed on a missing or unknown role.
func TestRetrieveRequiresViewRole(t *testing.T) {
	for _, role := range []string{"", "bogus"} {
		calls := 0
		n := &retrieveNode{dispatch: func(context.Context, string, string, json.RawMessage) (mcp.ToolResult, error) {
			calls++
			return mcp.ToolResult{}, nil
		}, topK: 8}
		_, _, err := n.retrieve(WithScope(context.Background(), Scope{UserID: "u", KBID: "kb", Role: role}), "q")
		if !errors.Is(err, ErrForbiddenTool) || calls != 0 {
			t.Fatalf("role %q: err=%v calls=%d", role, err, calls)
		}
	}
}
