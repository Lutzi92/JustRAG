package chat

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/agentteams"
)

// fakeDecisionRecorder captures the arguments of the last Record call so
// tests can assert recordAgentDecision forwards teamID/agentID faithfully
// (Phase 2 AP-B4 follow-up: team/agent id telemetry).
type fakeDecisionRecorder struct {
	mu        sync.Mutex
	called    bool
	kbID      string
	mode      string
	outcome   string
	hops      int
	rounds    int
	latencyMs int
	toolCalls []ToolCallRecord
	teamID    *string
	agentID   *string
}

func (f *fakeDecisionRecorder) Record(ctx context.Context, kbID, mode, outcome string, hops, rounds, latencyMs int, toolCalls []ToolCallRecord, teamID, agentID *string) {
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
}

// recordSnapshot is a lock-free copy of the fields recorded by
// fakeDecisionRecorder — snapshot() returns one instead of copying the
// struct (which embeds a sync.Mutex) directly.
type recordSnapshot struct {
	called    bool
	kbID      string
	mode      string
	outcome   string
	hops      int
	rounds    int
	latencyMs int
	toolCalls []ToolCallRecord
	teamID    *string
	agentID   *string
}

func (f *fakeDecisionRecorder) snapshot() recordSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return recordSnapshot{
		called:    f.called,
		kbID:      f.kbID,
		mode:      f.mode,
		outcome:   f.outcome,
		hops:      f.hops,
		rounds:    f.rounds,
		latencyMs: f.latencyMs,
		toolCalls: f.toolCalls,
		teamID:    f.teamID,
		agentID:   f.agentID,
	}
}

// waitForRecord polls the fake until Record has been called or the test
// times out — recordAgentDecision dispatches via safego.GoCtx in a
// goroutine, so the assertion can't happen synchronously.
func waitForRecord(t *testing.T, f *fakeDecisionRecorder) recordSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap := f.snapshot(); snap.called {
			return snap
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("decisionRecorder.Record was never called")
	return recordSnapshot{}
}

func TestRecordAgentDecision_ForwardsTeamAndAgentID(t *testing.T) {
	fake := &fakeDecisionRecorder{}
	h := NewHandler(nil, nil, nil, WithDecisionRecorder(fake))

	teamID := "team-123"
	h.recordAgentDecision(context.Background(), "kb-1", "team", "answered", 2, 0, 42, &teamID, nil)

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

	h.recordAgentDecision(context.Background(), "kb-1", "crag", "answered", 0, 0, 10, nil, nil)

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
	h.recordAgentDecision(context.Background(), "kb-1", "team", "answered", 0, 0, 42, nil, &agentID)

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
