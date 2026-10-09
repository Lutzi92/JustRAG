//go:build integration

package adkbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// suggestionWorkflow is a stand-in for the "nothing found → offer
// actions" flow from plan §6a:
//
//	Start → retrieve ─found──────→ answer
//	                 └no_evidence→ suggest (pauses) → act
//
// It is built fresh by each "process", so the test can prove the pause
// survives a restart: nothing but Postgres carries state between them.
func suggestionWorkflow(t *testing.T, hits map[string]string, acted *[]string) agent.Agent {
	t.Helper()
	retrieve := workflow.NewEmittingFunctionNode("retrieve",
		func(ctx agent.Context, q string, emit func(*session.Event) error) (any, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			if hit, ok := hits[q]; ok {
				ev.Routes = []string{"found"}
				if err := emit(ev); err != nil {
					return nil, err
				}
				return hit, nil
			}
			ev.Routes = []string{"no_evidence"}
			if err := emit(ev); err != nil {
				return nil, err
			}
			return q, nil
		}, workflow.NodeConfig{})

	answer := workflow.NewFunctionNode("answer",
		func(_ agent.Context, hit any) (string, error) { return fmt.Sprintf("Antwort: %v", hit), nil },
		workflow.NodeConfig{})

	suggest := workflow.NewEmittingFunctionNode("suggest",
		func(ctx agent.Context, q any, emit func(*session.Event) error) (any, error) {
			req := session.RequestInput{
				InterruptID: "suggest-" + uuid.NewString(),
				Message:     "Dazu habe ich in der Wissensbasis nichts gefunden.",
				Payload: map[string]any{
					"reason": "no_evidence",
					"query":  q,
					"actions": []map[string]any{
						{"id": "web", "tool": "web_search", "sideEffect": "external_read"},
						{"id": "conf", "tool": "confluence_import", "sideEffect": "kb_write", "requiresRole": "edit"},
					},
				},
			}
			if err := emit(workflow.NewRequestInputEvent(ctx, req)); err != nil {
				return nil, err
			}
			return nil, workflow.ErrNodeInterrupted
		}, workflow.NodeConfig{})

	act := workflow.NewFunctionNode("act",
		func(_ agent.Context, choice map[string]any) (string, error) {
			id, _ := choice["actionId"].(string)
			*acted = append(*acted, id)
			return "Aktion ausgeführt: " + id, nil
		}, workflow.NodeConfig{})

	edges := workflow.Concat(
		workflow.Chain(workflow.Start, retrieve),
		[]workflow.Edge{
			{From: retrieve, To: answer, Route: workflow.StringRoute("found")},
			{From: retrieve, To: suggest, Route: workflow.StringRoute("no_evidence")},
		},
		workflow.Chain(suggest, act),
	)
	a, err := workflowagent.New(workflowagent.Config{Name: "suggest_flow", Description: "suggestion workflow", Edges: edges})
	if err != nil {
		t.Fatalf("workflowagent.New: %v", err)
	}
	return a
}

func newWorkflowRunner(t *testing.T, a agent.Agent, svc session.Service) *runner.Runner {
	t.Helper()
	r, err := runner.New(runner.Config{AppName: "justrag", Agent: a, SessionService: svc, AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func collect(t *testing.T, r *runner.Runner, sid string, msg *genai.Content) []*session.Event {
	t.Helper()
	var evs []*session.Event
	for ev, err := range r.Run(context.Background(), "u1", sid, msg, agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func outputs(evs []*session.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Output != nil {
			fmt.Fprintf(&b, "%v\n", ev.Output)
		}
	}
	return b.String()
}

func TestWorkflowSuggestionPauseSurvivesRestart(t *testing.T) {
	pool := isolatedPool(t, "0082_adk_sessions.sql")
	var acted []string

	// Process 1: the question finds nothing, the workflow pauses.
	r1 := newWorkflowRunner(t, suggestionWorkflow(t, nil, &acted), NewPGSessionService(pool))
	evs := collect(t, r1, "s1", genai.NewContentFromText("Wie hoch ist das Budget 2027?", genai.RoleUser))
	var req *session.RequestInput
	for _, ev := range evs {
		if ev.RequestedInput != nil {
			req = ev.RequestedInput
		}
	}
	if req == nil {
		t.Fatalf("workflow did not pause; outputs:\n%s", outputs(evs))
	}
	payload, _ := req.Payload.(map[string]any)
	if payload["reason"] != "no_evidence" || len(payload["actions"].([]map[string]any)) != 2 {
		t.Fatalf("unexpected payload: %#v", req.Payload)
	}
	if len(acted) != 0 {
		t.Fatal("action ran before the user chose")
	}

	// Process 2: brand-new agent, runner and service objects on the same DB.
	r2 := newWorkflowRunner(t, suggestionWorkflow(t, nil, &acted), NewPGSessionService(pool))
	resume := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID:       req.InterruptID,
		Name:     workflow.WorkflowInputFunctionCallName,
		Response: map[string]any{"payload": map[string]any{"actionId": "web"}},
	}}}}
	evs = collect(t, r2, "s1", resume)
	if len(acted) != 1 || acted[0] != "web" {
		t.Fatalf("acted = %v; outputs:\n%s", acted, outputs(evs))
	}
	if !strings.Contains(outputs(evs), "Aktion ausgeführt: web") {
		t.Fatalf("missing act output:\n%s", outputs(evs))
	}
}

func TestWorkflowFoundBranchDoesNotPause(t *testing.T) {
	pool := isolatedPool(t, "0082_adk_sessions.sql")
	var acted []string
	r := newWorkflowRunner(t, suggestionWorkflow(t, map[string]string{"Mensa?": "11 Uhr"}, &acted), NewPGSessionService(pool))
	evs := collect(t, r, "s1", genai.NewContentFromText("Mensa?", genai.RoleUser))
	for _, ev := range evs {
		if ev.RequestedInput != nil {
			t.Fatal("found branch must not pause")
		}
	}
	if !strings.Contains(outputs(evs), "Antwort: 11 Uhr") {
		t.Fatalf("outputs:\n%s", outputs(evs))
	}
}
