package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/vector"
)

// fakeDecisionRecorder captures the arguments of the last Record call so
// tests can assert recordAgentDecision forwards teamID/agentID faithfully
// (Phase 2 AP-B4 follow-up: team/agent id telemetry).
//
// done signals Record's completion — Fix round 2 (Task 2, W7-R3): under the
// full-module test run (`go test ./... -count=1`, host loaded by other
// packages' eval/race suites) a polling-based wait timed out even though
// Record was eventually called, because recordAgentDecision dispatches it
// via safego.GoCtx on a DETACHED goroutine (fire-and-forget by design, see
// recordAgentDecision's doc comment) that can sit unscheduled for longer
// than a short poll bound under host contention. done is deliberately left
// as its nil zero value by every existing `&fakeDecisionRecorder{}` call
// site (no constructor needed) — waitChan lazily allocates it under the
// same mutex Record uses, so construction stays backward compatible.
type fakeDecisionRecorder struct {
	mu         sync.Mutex
	called     bool
	closed     bool // guards double-close of done when Record fires more than once
	done       chan struct{}
	kbID       string
	mode       string
	outcome    string
	hops       int
	rounds     int
	latencyMs  int
	toolCalls  []ToolCallRecord
	teamID     *string
	agentID    *string
	policyRule *int
}

func (f *fakeDecisionRecorder) Record(ctx context.Context, kbID, mode, outcome string, hops, rounds, latencyMs int, toolCalls []ToolCallRecord, teamID, agentID *string, policyRule *int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.kbID = kbID
	f.mode = mode
	f.outcome = outcome
	f.hops = hops
	f.rounds = rounds
	f.latencyMs = latencyMs
	f.toolCalls = toolCalls
	f.teamID = teamID
	f.agentID = agentID
	f.policyRule = policyRule
	if f.done == nil {
		f.done = make(chan struct{})
	}
	if !f.closed {
		f.closed = true
		close(f.done)
	}
}

// waitChan lazily creates (if needed) and returns the channel Record closes.
// Safe to call before OR after Record: if Record already ran and closed it,
// the returned channel is already closed and a receive on it proceeds
// immediately; if Record hasn't run yet, Record finds this same channel
// (not nil, since waitChan created and stored it) and closes it in place.
func (f *fakeDecisionRecorder) waitChan() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done == nil {
		f.done = make(chan struct{})
	}
	return f.done
}

// recordSnapshot is a lock-free copy of the fields recorded by
// fakeDecisionRecorder — snapshot() returns one instead of copying the
// struct (which embeds a sync.Mutex) directly.
type recordSnapshot struct {
	called     bool
	kbID       string
	mode       string
	outcome    string
	hops       int
	rounds     int
	latencyMs  int
	toolCalls  []ToolCallRecord
	teamID     *string
	agentID    *string
	policyRule *int
}

func (f *fakeDecisionRecorder) snapshot() recordSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return recordSnapshot{
		called:     f.called,
		kbID:       f.kbID,
		mode:       f.mode,
		outcome:    f.outcome,
		hops:       f.hops,
		rounds:     f.rounds,
		latencyMs:  f.latencyMs,
		toolCalls:  f.toolCalls,
		teamID:     f.teamID,
		agentID:    f.agentID,
		policyRule: f.policyRule,
	}
}

// waitForRecord polls the fake until Record has been called or the test
// times out — recordAgentDecision dispatches via safego.GoCtx in a
// goroutine, so the assertion can't happen synchronously.
func waitForRecord(t *testing.T, f *fakeDecisionRecorder) recordSnapshot {
	t.Helper()
	// Fix round 2 (Task 2, W7-R3): channel-based wait, not a bounded poll
	// on a shared counter. recordAgentDecision's Record call runs on a
	// detached fire-and-forget goroutine (safego.GoCtx); a tight poll loop
	// both burns CPU that goroutine needs to get scheduled AND, under a
	// loaded host (concurrent -race suites elsewhere in a full `go test
	// ./...` run), can simply run out its short deadline before the
	// goroutine is ever scheduled — that is what timed out here, not a
	// production ordering bug (verified: recordStandardPathDecision is
	// still called synchronously, before the response is written, on
	// every path that reaches it). A channel select parks this goroutine
	// without spinning and wakes immediately on close, and 15s is
	// generous enough to survive realistic host contention without
	// masking a genuine regression (a real "never called" bug still fails,
	// just after a longer wait).
	select {
	case <-f.waitChan():
		return f.snapshot()
	case <-time.After(15 * time.Second):
		t.Fatal("decisionRecorder.Record was never called")
		return recordSnapshot{}
	}
}

func TestRecordAgentDecision_ForwardsTeamAndAgentID(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	teamID := "team-123"
	h.recordAgentDecision(context.Background(), "kb-1", "team", "answered", 2, 0, 42, &teamID, nil, nil)

	snap := waitForRecord(t, fake)
	if snap.teamID == nil || *snap.teamID != teamID {
		t.Errorf("teamID = %v, want %q", snap.teamID, teamID)
	}
	if snap.agentID != nil {
		t.Errorf("agentID = %v, want nil", snap.agentID)
	}
	if snap.kbID != "kb-1" || snap.mode != "team" || snap.outcome != "answered" {
		t.Errorf("unexpected forwarded core fields: %+v", snap)
	}
}

func TestRecordAgentDecision_StandardPathForwardsNilIDs(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	h.recordAgentDecision(context.Background(), "kb-1", "crag", "answered", 0, 0, 10, nil, nil, nil)

	snap := waitForRecord(t, fake)
	if snap.teamID != nil {
		t.Errorf("teamID = %v, want nil", snap.teamID)
	}
	if snap.agentID != nil {
		t.Errorf("agentID = %v, want nil", snap.agentID)
	}
}

// TestRecordAgentDecision_ForwardsAgentID covers the Task-4 gap the fake
// recorder's fields already supported but no test exercised: an explicit
// single-agent pick (as opposed to a team) should forward agentID and leave
// teamID nil.
func TestRecordAgentDecision_ForwardsAgentID(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	agentID := "agent-456"
	h.recordAgentDecision(context.Background(), "kb-1", "team", "answered", 0, 0, 42, nil, &agentID, nil)

	snap := waitForRecord(t, fake)
	if snap.agentID == nil || *snap.agentID != agentID {
		t.Errorf("agentID = %v, want %q", snap.agentID, agentID)
	}
	if snap.teamID != nil {
		t.Errorf("teamID = %v, want nil", snap.teamID)
	}
}

// TestAttributionIDs covers the gating bug fixed alongside this test: a
// resolved team/agent selection (teamSel != nil) must NOT be attributed to
// the message/decision row unless the team actually answered the turn
// (willRunTeam). Comparison turns (runCompare wins) and Enhance follow-ups
// (willRunTeam forced false) both resolve teamSel but must attribute nil,
// nil — contradicting that would wrongly tag those rows with the team that
// was merely picked, not run.
func TestAttributionIDs(t *testing.T) {
	team := &teamSelection{team: &agentteams.TeamForChat{Team: agentteams.TeamRecord{ID: "team-1"}}}
	agent := &teamSelection{agent: &agentteams.AgentRecord{ID: "agent-1"}}

	cases := []struct {
		name        string
		willRunTeam bool
		teamSel     *teamSelection
		wantTeamID  *string
		wantAgentID *string
	}{
		{"resolved but not run (comparison/Enhance) -> nil,nil", false, team, nil, nil},
		{"run + team selected -> teamID", true, team, ptr("team-1"), nil},
		{"run + single agent selected -> agentID", true, agent, nil, ptr("agent-1")},
		{"run + nil selection -> nil,nil", true, nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTeamID, gotAgentID := attributionIDs(tc.willRunTeam, tc.teamSel)
			if !strPtrEqual(gotTeamID, tc.wantTeamID) {
				t.Errorf("teamID = %v, want %v", strPtrVal(gotTeamID), strPtrVal(tc.wantTeamID))
			}
			if !strPtrEqual(gotAgentID, tc.wantAgentID) {
				t.Errorf("agentID = %v, want %v", strPtrVal(gotAgentID), strPtrVal(tc.wantAgentID))
			}
		})
	}
}

func ptr(s string) *string { return &s }

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func strPtrVal(a *string) string {
	if a == nil {
		return "<nil>"
	}
	return *a
}

// W6-R6: the orchestrator-policy rule index reaches the recorder, and stays
// nil when the flag ladder decided. Without the plumbing the first arm's
// snapshot would carry nil and the test fails.
func TestRecordAgentDecision_ForwardsPolicyRule(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	rule := 2
	h.recordAgentDecision(context.Background(), "kb-1", "supervisor", "answered", 0, 0, 42, nil, nil, &rule)

	snap := waitForRecord(t, fake)
	if snap.policyRule == nil || *snap.policyRule != rule {
		t.Fatalf("policyRule = %v, want %d", snap.policyRule, rule)
	}
}

// Rule 0 is an ordinary rule: it must survive as a pointer to 0, not collapse
// into "no rule".
func TestRecordAgentDecision_ForwardsPolicyRuleZero(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	rule := 0
	h.recordAgentDecision(context.Background(), "kb-1", "agentic", "answered", 0, 0, 7, nil, nil, &rule)

	snap := waitForRecord(t, fake)
	if snap.policyRule == nil {
		t.Fatal("policyRule = nil, want a pointer to 0")
	}
	if *snap.policyRule != 0 {
		t.Fatalf("policyRule = %d, want 0", *snap.policyRule)
	}
}

func TestRecordAgentDecision_NilPolicyRuleStaysNil(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	h.recordAgentDecision(context.Background(), "kb-1", "crag", "answered", 0, 0, 10, nil, nil, nil)

	snap := waitForRecord(t, fake)
	if snap.policyRule != nil {
		t.Fatalf("policyRule = %v, want nil", snap.policyRule)
	}
}

// W7-R3: the non-streaming JSON path (writeJSONResponse) must record the
// same agent_decisions row the streaming standard path does, through the
// shared recordStandardPathDecision helper so the two cannot drift. These
// tests exercise the helper directly (the way both writers call it) rather
// than driving the whole handler, since writeJSONResponse's other
// dependencies (AI completion, post-response tasks) aren't faked here.
func TestRecordStandardPathDecision_DefaultsToCragAndAnswered(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	rule := 3
	p := chatResponseParams{
		kbID:          "kb-1",
		chatStartTime: time.Now(),
		policyRule:    &rule,
		// agentMode left empty -> defaults to "crag"
	}
	h.recordStandardPathDecision(context.Background(), p, nil)

	snap := waitForRecord(t, fake)
	if snap.kbID != "kb-1" {
		t.Errorf("kbID = %q, want kb-1", snap.kbID)
	}
	if snap.mode != "crag" {
		t.Errorf("mode = %q, want crag", snap.mode)
	}
	if snap.outcome != "answered" {
		t.Errorf("outcome = %q, want answered", snap.outcome)
	}
	if snap.policyRule == nil || *snap.policyRule != rule {
		t.Errorf("policyRule = %v, want %d", snap.policyRule, rule)
	}
	if snap.teamID != nil || snap.agentID != nil {
		t.Errorf("teamID/agentID = %v/%v, want nil/nil", snap.teamID, snap.agentID)
	}
}

// A transform follow-up (handleTransformFollowUp) sets agentMode to
// "transform_followup" — the recorded mode must carry it through unchanged,
// not fall back to "crag".
func TestRecordStandardPathDecision_ForwardsTransformMode(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	p := chatResponseParams{
		kbID:          "kb-2",
		chatStartTime: time.Now(),
		agentMode:     "transform_followup",
	}
	h.recordStandardPathDecision(context.Background(), p, nil)

	snap := waitForRecord(t, fake)
	if snap.mode != "transform_followup" {
		t.Errorf("mode = %q, want transform_followup", snap.mode)
	}
	if snap.outcome != "answered" {
		t.Errorf("outcome = %q, want answered", snap.outcome)
	}
	if snap.policyRule != nil {
		t.Errorf("policyRule = %v, want nil", snap.policyRule)
	}
}

// A non-empty bufferedTrajectory carrying a final answer-stage event
// overrides the "answered" default (mirrors agentOutcomeFromEvents'
// existing contract).
func TestRecordStandardPathDecision_OutcomeFromEvents(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	p := chatResponseParams{
		kbID:          "kb-3",
		chatStartTime: time.Now(),
	}
	events := []map[string]any{
		{"agentTrajectory": TrajectoryEvent{Stage: "answer", Decision: "abstained"}},
	}
	h.recordStandardPathDecision(context.Background(), p, events)

	snap := waitForRecord(t, fake)
	if snap.outcome != "abstained" {
		t.Errorf("outcome = %q, want abstained", snap.outcome)
	}
}

// TestSendMessage_NonStreamingStandardPath_RecordsAgentDecision is the fix
// for the Task-2 review's blocking finding: the three
// TestRecordStandardPathDecision_* cases above call the extracted helper
// directly, so they stay green even if writeJSONResponse's call to it were
// deleted — they verify the helper's logic, not that the non-streaming
// handler actually invokes it. This test drives the real handler,
// SendMessage with stream=false (the query param SendMessage reads —
// no "?stream=true" on the request), all the way to writeJSONResponse,
// adapting the wiringConfigResolver/wiringSearcher-style harness from
// comparison_team_wiring_test.go (an httptest server standing in for the
// AI provider, and a Searcher fake keyed by query string) plus
// fakeDecisionRecorder. Mutation-tested: temporarily removing the
// recordStandardPathDecision call at http_send_helpers.go's writeJSONResponse
// makes this test fail with "decisionRecorder.Record was never called";
// restored before committing — see the fix-round-1 report.
func TestSendMessage_NonStreamingStandardPath_RecordsAgentDecision(t *testing.T) {
	aiResolver := wiringConfigResolver(t)

	searcher := wiringSearcher{byQuery: map[string][]vector.SearchChunk{
		"hello": {{ID: "c1", FileID: "f1", FileName: "f1.md", Content: "some content"}},
	}}

	fake := &fakeDecisionRecorder{}
	// citation_validation_enabled defaults to true and factcheck_in_chat
	// defaults to true too (FactcheckEnabled's zero-reader fallback is
	// true, but a real reader with the key absent also reads true) — both
	// would fire extra LLM calls against the same canned httptest
	// response, which is harmless for THIS test's assertion but is exactly
	// the "aren't faked" complexity the original report cited, so turn
	// them off explicitly the way wiringSiteConfig does for factcheck.
	cfg := &fakeSiteConfigReader{values: map[string]*string{
		"citation_validation_enabled": strPtr("false"),
		"factcheck_in_chat":           strPtr("false"),
	}}

	h := NewHandler(newMockStore(), aiResolver, searcher,
		WithSiteConfigReader(cfg),
		WithDecisionRecorder(fake),
	)

	body := `{"message": "hello"}`
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectUser(r, "user1")
	r.SetPathValue("id", "kb1")
	// No "?stream=true" — SendMessage reads streamMode from the query
	// string, not the body, so this is what routes to writeJSONResponse.

	w := httptest.NewRecorder()
	h.SendMessage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from the non-streaming standard path, got %d: %s", w.Code, w.Body.String())
	}

	snap := waitForRecord(t, fake)
	if snap.mode != "crag" {
		t.Errorf("mode = %q, want crag (standard path, no agentMode override)", snap.mode)
	}
	if snap.outcome != "answered" {
		t.Errorf("outcome = %q, want answered (no orchestrator trajectory on the standard path)", snap.outcome)
	}
	if snap.teamID != nil || snap.agentID != nil {
		t.Errorf("teamID/agentID = %v/%v, want nil/nil", snap.teamID, snap.agentID)
	}
}
