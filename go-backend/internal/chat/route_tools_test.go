package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

// fakeToolDispatcher (declared in answer_tools_test.go) records the calls
// it receives and returns canned results; reused here so tests can assert
// that an allowed call is actually forwarded (not just that a disallowed
// one is refused).

func toolCatalog(names ...string) []ai.ChatTool {
	out := make([]ai.ChatTool, 0, len(names))
	for _, n := range names {
		out = append(out, ai.ChatTool{Type: "function", Function: ai.ChatToolFunction{Name: n}})
	}
	return out
}

// (a) restrict=false returns the same slices/dispatcher, unchanged.
func TestRestrictToolsForRoute_NoRestriction(t *testing.T) {
	inner := &fakeToolDispatcher{}
	catalog := toolCatalog("kb_search", "chunk_read", "calculator")

	disp, out := restrictToolsForRoute(inner, catalog, nil, false)

	if disp != ToolDispatcher(inner) {
		t.Fatalf("expected the same dispatcher back, got a different one")
	}
	if len(out) != len(catalog) || &out[0] != &catalog[0] {
		t.Fatalf("expected the same catalog slice back, got %#v", out)
	}
}

// (b) allow ["kb_search"] filters a 3-tool catalog to 1 and the wrapped
// dispatcher refuses chunk_read with an error naming the route, while
// forwarding kb_search to the fake inner.
func TestRestrictToolsForRoute_FiltersCatalogAndDispatch(t *testing.T) {
	inner := &fakeToolDispatcher{result: DispatchedToolResult{Text: "ok:kb_search"}}
	catalog := toolCatalog("kb_search", "chunk_read", "calculator")

	disp, out := restrictToolsForRoute(inner, catalog, []string{"kb_search"}, true)

	if len(out) != 1 || out[0].Function.Name != "kb_search" {
		t.Fatalf("expected catalog filtered to [kb_search], got %#v", out)
	}

	_, err := disp.Dispatch(context.Background(), "kb1", "chunk_read", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "not allowed for this route") {
		t.Fatalf("expected a route-allowlist refusal for chunk_read, got %v", err)
	}
	if len(inner.calls) != 0 {
		t.Fatalf("refused call must never reach the inner dispatcher, got %v", inner.calls)
	}

	res, err := disp.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("expected kb_search to be forwarded, got error %v", err)
	}
	if len(inner.calls) != 1 || inner.calls[0] != "kb_search" {
		t.Fatalf("expected kb_search forwarded to inner, got %v", inner.calls)
	}
	if res.Text != "ok:kb_search" {
		t.Fatalf("expected the inner's result to pass through, got %q", res.Text)
	}
}

// (c) wrapping a RestrictedDispatcher (agent allowlist kb_search,calculator)
// with a route allowlist (kb_search,chunk_read) yields a catalog of exactly
// kb_search — the intersection — and refuses BOTH calculator (blocked by the
// route wrapper) and chunk_read (blocked deeper by the agent wrapper).
func TestRestrictToolsForRoute_ComposesWithAgentRestriction(t *testing.T) {
	agentDisp := NewRestrictedDispatcher(NewMCPDispatcher(nil), []string{"kb_search", "calculator"}, false)
	// The catalog handed in is what the agent wrapper's own
	// AnswerToolCatalog would have projected: already filtered to its
	// allowlist. chunk_read never appears here even though the route
	// allowlist below names it.
	agentCatalog := toolCatalog("kb_search", "calculator")

	disp, out := restrictToolsForRoute(agentDisp, agentCatalog, []string{"kb_search", "chunk_read"}, true)

	if len(out) != 1 || out[0].Function.Name != "kb_search" {
		t.Fatalf("expected the intersection catalog [kb_search], got %#v", out)
	}

	// calculator is refused by the OUTER route wrapper (it's not in the
	// route allowlist) before the call ever reaches the inner agent
	// wrapper — asserting the exact error text is load-bearing here: the
	// inner NewMCPDispatcher(nil) also errors on every call (unknown
	// tool, nil registry), so a bare "err == nil" check would pass even
	// if the route guard were removed entirely and the call fell straight
	// through to the inner dispatcher.
	if _, err := disp.Dispatch(context.Background(), "kb1", "calculator", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "not allowed for this route") {
		t.Fatalf("expected calculator refused by the route wrapper (not allowed for this route), got %v", err)
	}
	// chunk_read passes the route wrapper (it IS in the route allowlist)
	// and is refused one layer down by the inner RestrictedDispatcher's
	// own agent allowlist — a different, equally specific error text.
	if _, err := disp.Dispatch(context.Background(), "kb1", "chunk_read", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "not allowed for this agent") {
		t.Fatalf("expected chunk_read refused by the inner agent wrapper (not allowed for this agent), got %v", err)
	}
}

// (d) an empty allowlist ([], ok=true) yields an empty catalog — and the
// answer loop must then be SKIPPED (useAnswerTools && len(catalog) > 0)
// rather than called with no tools.
func TestRestrictToolsForRoute_EmptyAllowlistEmptiesCatalog(t *testing.T) {
	inner := &fakeToolDispatcher{}
	catalog := toolCatalog("kb_search", "chunk_read")

	_, out := restrictToolsForRoute(inner, catalog, []string{}, true)
	if len(out) != 0 {
		t.Fatalf("expected an empty catalog, got %#v", out)
	}
}

func TestShouldRunAnswerToolsLoop(t *testing.T) {
	cases := []struct {
		name          string
		useAnswerTool bool
		catalog       []ai.ChatTool
		want          bool
	}{
		{"tools off", false, toolCatalog("kb_search"), false},
		{"tools on, non-empty catalog", true, toolCatalog("kb_search"), true},
		{"tools on, nil catalog", true, nil, false},
		{"tools on, empty (route-restricted) catalog", true, []ai.ChatTool{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRunAnswerToolsLoop(tc.useAnswerTool, tc.catalog); got != tc.want {
				t.Fatalf("shouldRunAnswerToolsLoop(%v, len=%d) = %v, want %v", tc.useAnswerTool, len(tc.catalog), got, tc.want)
			}
		})
	}
}

func TestAnswerToolsRouteDecision(t *testing.T) {
	byRouteWithGS := map[string][]string{"global_synthesis": {"kb_search"}, "lookup": {"kb_search"}}
	byRouteNoGS := map[string][]string{"lookup": {"kb_search"}}

	if got := answerToolsRouteDecision(byRouteWithGS, "lookup", true); got != "global_synthesis" {
		t.Fatalf("global_synthesis key present + global-synthesis turn: want %q, got %q", "global_synthesis", got)
	}
	if got := answerToolsRouteDecision(byRouteNoGS, "lookup", true); got != "lookup" {
		t.Fatalf("no global_synthesis key: want the query type %q, got %q", "lookup", got)
	}
	if got := answerToolsRouteDecision(byRouteWithGS, "lookup", false); got != "lookup" {
		t.Fatalf("non-global-synthesis turn must use the query type even when a global_synthesis key exists: got %q", got)
	}
}

// TestResolveAnswerToolsRoute covers the Task 7 fix-round-2 controller
// ruling: an unknown query type (queryType == "", e.g. a transform
// follow-up that skipped classification) must be treated as FULLY
// RESTRICTED once the document configures at least one route — never as
// "no restriction", which would hand that turn the unrestricted catalog on
// a KB whose every other route is locked down. An empty document leaves
// behaviour unchanged either way.
func TestResolveAnswerToolsRoute(t *testing.T) {
	nonEmpty := map[string][]string{"lookup": {"kb_search"}}
	empty := map[string][]string{}
	var nilMap map[string][]string

	cases := []struct {
		name           string
		byRoute        map[string][]string
		queryType      string
		globalSynth    bool
		wantOk         bool
		wantAllowLen   int
		wantDecision   string
		wantReasonNote bool // true: expect a non-empty Reason
	}{
		{
			name:           "unknown query type + non-empty map -> fully restricted",
			byRoute:        nonEmpty,
			queryType:      "",
			globalSynth:    false,
			wantOk:         true,
			wantAllowLen:   0,
			wantDecision:   "unknown",
			wantReasonNote: true,
		},
		{
			name:         "unknown query type + empty map -> unchanged (no restriction)",
			byRoute:      empty,
			queryType:    "",
			globalSynth:  false,
			wantOk:       false,
			wantAllowLen: 0,
			wantDecision: "",
		},
		{
			name:         "unknown query type + nil map -> unchanged (no restriction)",
			byRoute:      nilMap,
			queryType:    "",
			globalSynth:  false,
			wantOk:       false,
			wantAllowLen: 0,
			wantDecision: "",
		},
		{
			name:         "known query type delegates to Allowlist as before",
			byRoute:      nonEmpty,
			queryType:    "lookup",
			globalSynth:  false,
			wantOk:       true,
			wantAllowLen: 1,
			wantDecision: "lookup",
		},
		{
			name:         "known query type with no matching entry -> no restriction",
			byRoute:      nonEmpty,
			queryType:    "enumeration",
			globalSynth:  false,
			wantOk:       false,
			wantAllowLen: 0,
			wantDecision: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allow, ok, decision, reason := resolveAnswerToolsRoute(tc.byRoute, tc.queryType, tc.globalSynth)
			if ok != tc.wantOk {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOk)
			}
			if len(allow) != tc.wantAllowLen {
				t.Fatalf("len(allow) = %d, want %d (allow=%#v)", len(allow), tc.wantAllowLen, allow)
			}
			if decision != tc.wantDecision {
				t.Fatalf("decision = %q, want %q", decision, tc.wantDecision)
			}
			gotReason := reason != ""
			if gotReason != tc.wantReasonNote {
				t.Fatalf("reason non-empty = %v, want %v (reason=%q)", gotReason, tc.wantReasonNote, reason)
			}
		})
	}
}
