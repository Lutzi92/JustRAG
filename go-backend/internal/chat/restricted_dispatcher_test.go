package chat

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRestrictedDispatcherBlocksAtDispatchTime(t *testing.T) {
	// nil inner registry: Dispatch would fail with ErrUnknownTool anyway,
	// but the allowlist check must fire FIRST with a distinct error so a
	// prompt-injected call to a hidden-but-registered tool is provably
	// blocked by policy, not by absence.
	d := NewRestrictedDispatcher(NewMCPDispatcher(nil), []string{"kb_search"}, false)

	_, err := d.Dispatch(context.Background(), "kb1", "code_exec", json.RawMessage(`{}`))
	if err == nil || err.Error() != `tool "code_exec" is not allowed for this agent` {
		t.Fatalf("privileged tool must be blocked by policy, got %v", err)
	}
	_, err = d.Dispatch(context.Background(), "kb1", "calculator", json.RawMessage(`{}`))
	if err == nil || err.Error() != `tool "calculator" is not allowed for this agent` {
		t.Fatalf("non-allowlisted tool must be blocked, got %v", err)
	}
}

func TestRestrictedDispatcherAllowsPrivilegedWhenFlagged(t *testing.T) {
	d := NewRestrictedDispatcher(NewMCPDispatcher(nil), []string{"sql_query"}, true)
	_, err := d.Dispatch(context.Background(), "kb1", "sql_query", json.RawMessage(`{}`))
	// Passes the policy layer; fails deeper with ErrUnknownTool (nil registry).
	if err == nil || err.Error() == `tool "sql_query" is not allowed for this agent` {
		t.Fatalf("flagged privileged tool must pass the policy layer, got %v", err)
	}
}
