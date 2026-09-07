package chat

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/chatpolicy"
)

// TestChatOrchestratorPolicy_DefaultIsEmpty — every state that means "the
// operator never authored a policy" must read as nil, because a non-nil
// policy would change routing (W6-R10: the ladder is byte-identical when the
// policy is empty).
func TestChatOrchestratorPolicy_DefaultIsEmpty(t *testing.T) {
	readers := map[string]SiteConfigReader{
		"nil reader":      nil,
		"missing key":     &fakeSiteConfigReader{values: map[string]*string{}},
		"empty value":     &fakeSiteConfigReader{values: map[string]*string{"chat_orchestrator_policy": strPtr("")}},
		"whitespace only": &fakeSiteConfigReader{values: map[string]*string{"chat_orchestrator_policy": strPtr("   ")}},
		"empty array":     &fakeSiteConfigReader{values: map[string]*string{"chat_orchestrator_policy": strPtr("[]")}},
	}
	for name, r := range readers {
		if got := ChatOrchestratorPolicy(context.Background(), r); len(got) != 0 {
			t.Errorf("%s: got %v, want empty", name, got)
		}
	}
}

// TestChatOrchestratorPolicy_Parses — a well-formed stored document reaches
// the caller intact.
func TestChatOrchestratorPolicy_Parses(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_orchestrator_policy": strPtr(
			`[{"when":{"query_type":["enumeration"]},"orchestrator":"supervisor","mode":"force"}]`),
	}}
	p := ChatOrchestratorPolicy(context.Background(), r)
	if len(p) != 1 {
		t.Fatalf("len = %d, want 1", len(p))
	}
	if p[0].Orchestrator != "supervisor" || p[0].Mode != chatpolicy.ModeForce {
		t.Fatalf("rule = %+v", p[0])
	}
	d := chatpolicy.Decide(p, chatpolicy.Signals{QueryType: "enumeration"}, nil)
	if !d.Matched || !d.Applied || d.Orchestrator != "supervisor" {
		t.Fatalf("Decide = %+v", d)
	}
}

// TestChatOrchestratorPolicy_InvalidFallsBackToEmpty is the defence-in-depth
// half of W6-R14: the save path validates, but a value written straight into
// the table (psql, a restored dump) must degrade to "no policy" rather than
// panic or route somewhere arbitrary.
func TestChatOrchestratorPolicy_InvalidFallsBackToEmpty(t *testing.T) {
	for _, raw := range []string{
		`{"orchestrator":"standard"}`,
		`[{"when":{},"orchestrator":"nope","mode":"force"}]`,
		`[{"when":{},"orchestrator":"standard","mode":"maybe"}]`,
		`[{`,
		`not json`,
	} {
		r := &fakeSiteConfigReader{values: map[string]*string{"chat_orchestrator_policy": strPtr(raw)}}
		got := ChatOrchestratorPolicy(context.Background(), r)
		if len(got) != 0 {
			t.Errorf("value %q: got %v, want empty", raw, got)
		}
	}
}

// TestChatAnswerToolsByRoute_DefaultIsEmpty — mirror of the policy default.
func TestChatAnswerToolsByRoute_DefaultIsEmpty(t *testing.T) {
	readers := map[string]SiteConfigReader{
		"nil reader":      nil,
		"missing key":     &fakeSiteConfigReader{values: map[string]*string{}},
		"empty value":     &fakeSiteConfigReader{values: map[string]*string{"chat_answer_tools_by_route": strPtr("")}},
		"whitespace only": &fakeSiteConfigReader{values: map[string]*string{"chat_answer_tools_by_route": strPtr("  ")}},
		"empty object":    &fakeSiteConfigReader{values: map[string]*string{"chat_answer_tools_by_route": strPtr("{}")}},
	}
	for name, r := range readers {
		m := ChatAnswerToolsByRoute(context.Background(), r)
		if _, ok := m.Allowlist("lookup", false); ok {
			t.Errorf("%s: got a restriction %v, want none", name, m)
		}
	}
}

func TestChatAnswerToolsByRoute_Parses(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_answer_tools_by_route": strPtr(`{"lookup":["kb_search","chunk_read"],"global_synthesis":[]}`),
	}}
	m := ChatAnswerToolsByRoute(context.Background(), r)
	allow, ok := m.Allowlist("lookup", false)
	if !ok || len(allow) != 2 {
		t.Fatalf("lookup allowlist = (%v, %v)", allow, ok)
	}
	allow, ok = m.Allowlist("complex_reasoning", true)
	if !ok || len(allow) != 0 {
		t.Fatalf("global-synthesis allowlist = (%v, %v), want an empty restriction", allow, ok)
	}
}

// TestChatAnswerToolsByRoute_InvalidFallsBackToEmpty — an unparseable stored
// value must not silently strip the answer-tool catalog down to nothing.
func TestChatAnswerToolsByRoute_InvalidFallsBackToEmpty(t *testing.T) {
	for _, raw := range []string{
		`["kb_search"]`,
		`{"lookup":["kb_serch"]}`,
		`{"smalltalk":["kb_search"]}`,
		`{"lookup":`,
		`nonsense`,
	} {
		r := &fakeSiteConfigReader{values: map[string]*string{"chat_answer_tools_by_route": strPtr(raw)}}
		m := ChatAnswerToolsByRoute(context.Background(), r)
		if _, ok := m.Allowlist("lookup", false); ok {
			t.Errorf("value %q: got a restriction %v, want none", raw, m)
		}
	}
}
