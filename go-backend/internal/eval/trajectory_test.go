package eval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

func ptrFloat(v float64) *float64 { return &v }

func TestCollectEmit_OnlyAppendsTrajectoryEvents(t *testing.T) {
	var events []chat.TrajectoryEvent
	emit := CollectEmit(&events)

	emit(map[string]any{"agentTrajectory": chat.TrajectoryEvent{Stage: "plan", Step: 1}})
	emit(map[string]any{"agenticHop": 2}) // legacy event — must be ignored
	emit(map[string]any{"sources": "ignored"})
	emit(map[string]any{"agentTrajectory": chat.TrajectoryEvent{Stage: "answer"}})

	if len(events) != 2 {
		t.Fatalf("collect: want 2 trajectory events, got %d (%+v)", len(events), events)
	}
	if events[0].Stage != "plan" || events[1].Stage != "answer" {
		t.Errorf("collect order/stages: got %+v", events)
	}
}

func TestAggregateTrajectory_MeansAndCounts(t *testing.T) {
	records := []TrajectoryRecord{
		{
			Mode:  "agentic",
			Score: &TrajectoryScore{DecompositionCoverage: ptrFloat(0.8), DecisionCorrectness: ptrFloat(1.0)},
		},
		{
			Mode:  "agentic",
			Score: &TrajectoryScore{DecompositionCoverage: ptrFloat(0.6), DecisionCorrectness: ptrFloat(0.5)},
		},
		{
			Mode: "agentic",
			// no Score → counted in Count, not in NumScored
		},
		{
			Mode:  "plan_execute",
			Score: &TrajectoryScore{DecisionCorrectness: ptrFloat(0.9)},
		},
	}

	agg := AggregateTrajectory(records)
	a, ok := agg["agentic"]
	if !ok {
		t.Fatalf("agentic aggregate missing")
	}
	if a.Count != 3 {
		t.Errorf("agentic Count = %d, want 3 (1 unscored counted)", a.Count)
	}
	if a.NumScored != 2 {
		t.Errorf("agentic NumScored = %d, want 2", a.NumScored)
	}
	if a.MeanDecompositionCoverage != 0.7 {
		t.Errorf("agentic MeanDecompositionCoverage = %v, want 0.7", a.MeanDecompositionCoverage)
	}
	if a.MeanDecisionCorrectness != 0.75 {
		t.Errorf("agentic MeanDecisionCorrectness = %v, want 0.75", a.MeanDecisionCorrectness)
	}

	p, ok := agg["plan_execute"]
	if !ok {
		t.Fatalf("plan_execute aggregate missing")
	}
	if p.Count != 1 || p.NumScored != 1 {
		t.Errorf("plan_execute Count/NumScored = %d/%d, want 1/1", p.Count, p.NumScored)
	}
}

func TestWriteTrajectoryJSONL_RoundTrip(t *testing.T) {
	records := []TrajectoryRecord{
		{
			QuestionID: "q1",
			Mode:       "agentic",
			Events: []chat.TrajectoryEvent{
				{Stage: "hop", Step: 1, Query: "starter"},
				{Stage: "answer", Decision: "early_stop_sufficient"},
			},
		},
		{
			QuestionID: "q1",
			Mode:       "plan_execute",
			Events:     []chat.TrajectoryEvent{{Stage: "plan", Queries: []string{"a", "b"}}},
		},
	}

	var buf bytes.Buffer
	if err := WriteTrajectoryJSONL(&buf, records); err != nil {
		t.Fatalf("write: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(records) {
		t.Fatalf("line count: got %d, want %d", len(lines), len(records))
	}
	for i, ln := range lines {
		var got TrajectoryRecord
		if err := json.Unmarshal([]byte(ln), &got); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", i, err, ln)
		}
		if got.QuestionID != records[i].QuestionID || got.Mode != records[i].Mode {
			t.Errorf("line %d round-trip mismatch: got %+v, want %+v", i, got, records[i])
		}
	}
}
