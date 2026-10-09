package adkbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/justrag/go-backend/internal/mcp"
)

const (
	RouteFound      = "found"
	RouteNoEvidence = "no_evidence"
	RouteNoFiles    = "no_files"
)

// ErrLibraryScopeUnsupported: retrieval over library files lands with the
// user-file-library "chat with my files" work.
var ErrLibraryScopeUnsupported = errors.New("adkbridge: library-file retrieval not available yet")

// RetrieveResult is the output of RetrieveNode.
type RetrieveResult struct {
	Query  string            `json:"query"`
	Text   string            `json:"text"`
	Chunks []mcp.ResultChunk `json:"chunks,omitempty"`
}

type retrieveNode struct {
	dispatch DispatchFunc
	topK     int
}

func (n *retrieveNode) retrieve(ctx context.Context, q string) (RetrieveResult, string, error) {
	sc, ok := ScopeFrom(ctx)
	if !ok {
		return RetrieveResult{}, "", ErrNoScope
	}
	if sc.KBID == "" {
		if len(sc.LibraryFileIDs) > 0 {
			return RetrieveResult{}, "", ErrLibraryScopeUnsupported
		}
		return RetrieveResult{Query: q}, RouteNoFiles, nil
	}
	// The node dispatches kb_search itself, bypassing dispatchTool, so it
	// enforces kb_search's role floor here. Fails closed on an empty or
	// unknown role. KB-less runs above touch no KB and need no KB role.
	if need := PolicyFor("kb_search").RequiresRole; !RoleAtLeast(sc.Role, need) {
		return RetrieveResult{}, "", fmt.Errorf("%w: kb_search requires role %s", ErrForbiddenTool, need)
	}
	args, _ := json.Marshal(map[string]any{"query": q, "top_k": n.topK, "kb_id": sc.KBID})
	res, err := n.dispatch(ctx, sc.KBID, "kb_search", args)
	if err != nil {
		return RetrieveResult{}, "", err
	}
	out := RetrieveResult{Query: q, Text: res.Text, Chunks: res.Chunks}
	if len(res.Chunks) == 0 {
		return out, RouteNoEvidence, nil
	}
	return out, RouteFound, nil
}

// RetrieveNode runs kb_search for the node input (the query) in the run's
// KB and routes found / no_evidence / no_files.
func RetrieveNode(name string, dispatch DispatchFunc, topK int) workflow.Node {
	n := &retrieveNode{dispatch: dispatch, topK: topK}
	return workflow.NewEmittingFunctionNode(name,
		func(ctx agent.Context, q string, emit func(*session.Event) error) (any, error) {
			res, route, err := n.retrieve(ctx, q)
			if err != nil {
				return nil, err
			}
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Routes = []string{route}
			if err := emit(ev); err != nil {
				return nil, err
			}
			return res, nil
		}, workflow.NodeConfig{})
}

// Action is one suggestion offered in a pause (plan §6a).
type Action struct {
	ID           string         `json:"id"`
	Tool         string         `json:"tool,omitempty"`
	FrontendTool string         `json:"frontendTool,omitempty"`
	Label        string         `json:"label"`
	SideEffect   SideEffect     `json:"sideEffect,omitempty"`
	RequiresRole string         `json:"requiresRole,omitempty"`
	Args         map[string]any `json:"args,omitempty"`
}

// VisibleActions keeps frontend actions and the server tools that are
// allowlisted and executable by the running user. Execution re-checks at
// dispatch; this only avoids offering buttons that would fail.
func VisibleActions(sc Scope, actions []Action, allowed []string) []Action {
	out := make([]Action, 0, len(actions))
	for _, a := range actions {
		if a.Tool == "" {
			out = append(out, a)
			continue
		}
		p := PolicyFor(a.Tool)
		if !slices.Contains(allowed, a.Tool) || !RoleAtLeast(sc.Role, p.RequiresRole) ||
			(privilegedInBridge(a.Tool) && !sc.AllowPrivileged) {
			continue
		}
		a.SideEffect, a.RequiresRole = p.SideEffect, p.RequiresRole
		out = append(out, a)
	}
	return out
}

// SuggestNode pauses the run and offers actions after a dead end.
func SuggestNode(name, message string, actions []Action, allowedTools []string) workflow.Node {
	return workflow.NewEmittingFunctionNode(name,
		func(ctx agent.Context, in RetrieveResult, emit func(*session.Event) error) (any, error) {
			sc, ok := ScopeFrom(ctx)
			if !ok {
				return nil, ErrNoScope
			}
			reason := RouteNoEvidence
			if sc.KBID == "" {
				reason = RouteNoFiles
			}
			req := session.RequestInput{
				InterruptID: name + "-" + uuid.NewString(),
				Message:     message,
				Payload: map[string]any{
					"reason":  reason,
					"query":   in.Query,
					"actions": VisibleActions(sc, actions, allowedTools),
				},
			}
			if err := emit(workflow.NewRequestInputEvent(ctx, req)); err != nil {
				return nil, err
			}
			return nil, workflow.ErrNodeInterrupted
		}, workflow.NodeConfig{})
}
