package eval

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

const floatEpsilon = 1e-9

func floatsNearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < floatEpsilon
}

func TestWriteJSONReport_RoundTrip(t *testing.T) {
	rep := Report{
		GeneratedAt: time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		GoldenPath:  "example.jsonl",
		K:           10,
		Questions: []QuestionReport{
			{
				Question:  Question{ID: "q1", Question: "why?", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}},
				Metrics:   PerQuestionMetrics{K: 10, RecallAtK: 1, PrecisionAtK: 0.1, ReciprocalRank: 1, NDCGAtK: 1},
				Retrieved: []RetrievedChunk{{FileID: "f1", Score: 0.9}},
				LatencyMs: 12,
			},
		},
		Aggregate: AggregateMetrics{K: 10, Count: 1, MeanRecall: 1, MeanPrecision: 0.1, MRR: 1, MeanNDCG: 1},
	}

	var buf bytes.Buffer
	if err := WriteJSONReport(&buf, rep); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.K != 10 {
		t.Errorf("K: got %d, want 10", back.K)
	}
	if !back.GeneratedAt.Equal(rep.GeneratedAt) {
		t.Errorf("GeneratedAt: got %v, want %v", back.GeneratedAt, rep.GeneratedAt)
	}
	if back.GoldenPath != "example.jsonl" {
		t.Errorf("GoldenPath: got %q, want %q", back.GoldenPath, "example.jsonl")
	}
	if got, want := len(back.Questions), 1; got != want {
		t.Fatalf("len(Questions): got %d, want %d", got, want)
	}

	q := back.Questions[0]
	if q.Question.ID != "q1" {
		t.Errorf("Questions[0].Question.ID: got %q, want %q", q.Question.ID, "q1")
	}
	if q.Question.Question != "why?" {
		t.Errorf("Questions[0].Question.Question: got %q, want %q", q.Question.Question, "why?")
	}
	if q.Question.KbID != "k" {
		t.Errorf("Questions[0].Question.KbID: got %q, want %q", q.Question.KbID, "k")
	}
	if q.Question.Language != "en" {
		t.Errorf("Questions[0].Question.Language: got %q, want %q", q.Question.Language, "en")
	}
	if got, want := q.Question.MustCiteFileIDs, []string{"f1"}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Questions[0].Question.MustCiteFileIDs: got %v, want %v", got, want)
	}
	if q.LatencyMs != 12 {
		t.Errorf("Questions[0].LatencyMs: got %d, want 12", q.LatencyMs)
	}
	if got, want := len(q.Retrieved), 1; got != want {
		t.Fatalf("len(Questions[0].Retrieved): got %d, want %d", got, want)
	}
	if q.Retrieved[0].FileID != "f1" {
		t.Errorf("Retrieved[0].FileID: got %q, want %q", q.Retrieved[0].FileID, "f1")
	}
	if !floatsNearlyEqual(q.Retrieved[0].Score, 0.9) {
		t.Errorf("Retrieved[0].Score: got %v, want ~0.9", q.Retrieved[0].Score)
	}
	if q.Metrics.K != 10 {
		t.Errorf("Questions[0].Metrics.K: got %d, want 10", q.Metrics.K)
	}
	if !floatsNearlyEqual(q.Metrics.RecallAtK, 1) {
		t.Errorf("Questions[0].Metrics.RecallAtK: got %v, want ~1", q.Metrics.RecallAtK)
	}
	if !floatsNearlyEqual(q.Metrics.PrecisionAtK, 0.1) {
		t.Errorf("Questions[0].Metrics.PrecisionAtK: got %v, want ~0.1", q.Metrics.PrecisionAtK)
	}
	if !floatsNearlyEqual(q.Metrics.ReciprocalRank, 1) {
		t.Errorf("Questions[0].Metrics.ReciprocalRank: got %v, want ~1", q.Metrics.ReciprocalRank)
	}
	if !floatsNearlyEqual(q.Metrics.NDCGAtK, 1) {
		t.Errorf("Questions[0].Metrics.NDCGAtK: got %v, want ~1", q.Metrics.NDCGAtK)
	}

	if back.Aggregate.K != 10 {
		t.Errorf("Aggregate.K: got %d, want 10", back.Aggregate.K)
	}
	if back.Aggregate.Count != 1 {
		t.Errorf("Aggregate.Count: got %d, want 1", back.Aggregate.Count)
	}
	if !floatsNearlyEqual(back.Aggregate.MeanRecall, 1) {
		t.Errorf("Aggregate.MeanRecall: got %v, want ~1", back.Aggregate.MeanRecall)
	}
	if !floatsNearlyEqual(back.Aggregate.MeanPrecision, 0.1) {
		t.Errorf("Aggregate.MeanPrecision: got %v, want ~0.1", back.Aggregate.MeanPrecision)
	}
	if !floatsNearlyEqual(back.Aggregate.MRR, 1) {
		t.Errorf("Aggregate.MRR: got %v, want ~1", back.Aggregate.MRR)
	}
	if !floatsNearlyEqual(back.Aggregate.MeanNDCG, 1) {
		t.Errorf("Aggregate.MeanNDCG: got %v, want ~1", back.Aggregate.MeanNDCG)
	}
}

func TestWriteHumanSummary_IncludesJudgeMetricsWhenPresent(t *testing.T) {
	f, r, p := 0.82, 0.71, 0.9
	rep := Report{
		K: 10,
		Aggregate: AggregateMetrics{
			K:                    10,
			Count:                5,
			MeanRecall:           0.7,
			MeanPrecision:        0.2,
			MRR:                  0.6,
			MeanNDCG:             0.55,
			MeanFaithfulness:     &f,
			MeanAnswerRelevance:  &r,
			MeanContextPrecision: &p,
			JudgedCount:          5,
		},
	}
	var buf bytes.Buffer
	if err := WriteHumanSummary(&buf, rep); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	s := buf.String()
	for _, needle := range []string{"mean_faithfulness", "0.820", "mean_answer_relevance", "0.710", "mean_context_precision", "0.900", "judged_count=5"} {
		if !strings.Contains(s, needle) {
			t.Errorf("summary missing %q\nGot:\n%s", needle, s)
		}
	}
}

func TestWriteHumanSummary_OmitsJudgeSectionWhenAbsent(t *testing.T) {
	rep := Report{K: 10, Aggregate: AggregateMetrics{K: 10, Count: 1}}
	var buf bytes.Buffer
	if err := WriteHumanSummary(&buf, rep); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	s := buf.String()
	if strings.Contains(s, "mean_faithfulness") {
		t.Errorf("expected no judge section, got:\n%s", s)
	}
}

func TestWriteHumanSummary_IncludesKeyNumbers(t *testing.T) {
	rep := Report{
		K:         10,
		Aggregate: AggregateMetrics{K: 10, Count: 3, MeanRecall: 0.72, MeanPrecision: 0.18, MRR: 0.55, MeanNDCG: 0.61},
		Errors:    1,
	}
	var buf bytes.Buffer
	if err := WriteHumanSummary(&buf, rep); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	s := buf.String()
	for _, needle := range []string{"k=10", "0.720", "0.180", "0.550", "0.610", "errors      = 1"} {
		if !strings.Contains(s, needle) {
			t.Errorf("summary missing %q\nGot:\n%s", needle, s)
		}
	}
}

func TestWriteHumanSummary_IncludesRouteBreakdownWhenPresent(t *testing.T) {
	rep := Report{
		GeneratedAt: time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC),
		GoldenPath:  "example.jsonl",
		K:           5,
		Questions: []QuestionReport{
			{Question: Question{ID: "q1", QueryType: "lookup"}, Metrics: PerQuestionMetrics{K: 5, RecallAtK: 1.0}},
			{Question: Question{ID: "q2", QueryType: "enumeration"}, Metrics: PerQuestionMetrics{K: 5, RecallAtK: 0.5}},
		},
		Aggregate: AggregateMetrics{K: 5, Count: 2, MeanRecall: 0.75},
		RouteAggregates: map[string]AggregateMetrics{
			"lookup":      {K: 5, Count: 1, MeanRecall: 1.0},
			"enumeration": {K: 5, Count: 1, MeanRecall: 0.5},
		},
	}
	var buf bytes.Buffer
	if err := WriteHumanSummary(&buf, rep); err != nil {
		t.Fatalf("WriteHumanSummary: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Per route:") {
		t.Errorf("expected 'Per route:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "lookup") || !strings.Contains(out, "enumeration") {
		t.Errorf("expected route names in output, got:\n%s", out)
	}
	if !strings.Contains(out, "mean_recall=1.000") {
		t.Errorf("expected lookup mean_recall=1.000, got:\n%s", out)
	}
}

func TestWriteHumanSummary_OmitsRouteBreakdownWhenAbsent(t *testing.T) {
	rep := Report{
		K:         5,
		Questions: []QuestionReport{{Question: Question{ID: "q1"}, Metrics: PerQuestionMetrics{K: 5, RecallAtK: 1.0}}},
		Aggregate: AggregateMetrics{K: 5, Count: 1, MeanRecall: 1.0},
		// RouteAggregates intentionally nil.
	}
	var buf bytes.Buffer
	if err := WriteHumanSummary(&buf, rep); err != nil {
		t.Fatalf("WriteHumanSummary: %v", err)
	}
	if strings.Contains(buf.String(), "Per route:") {
		t.Errorf("expected no 'Per route:' section when RouteAggregates is nil, got:\n%s", buf.String())
	}
}

func TestWriteHumanSummary_IncludesOrchestratorBlockWhenPresent(t *testing.T) {
	rep := Report{
		K:         10,
		Aggregate: AggregateMetrics{K: 10, Count: 2, MeanRecall: 0.9, MRR: 0.9, MeanNDCG: 0.9},
		OrchestratorAggregates: map[string]AggregateMetrics{
			OrchestratorSupervisor: {Count: 1, MeanRecall: 0.91, MRR: 0.94, MeanNDCG: 0.93},
			OrchestratorStandard:   {Count: 1, MeanRecall: 0.89, MRR: 0.88, MeanNDCG: 0.90},
		},
	}
	var buf bytes.Buffer
	if err := WriteHumanSummary(&buf, rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Orchestrators:") {
		t.Fatalf("missing 'Orchestrators:' block in output:\n%s", out)
	}
	if !strings.Contains(out, OrchestratorSupervisor) || !strings.Contains(out, OrchestratorStandard) {
		t.Fatalf("orchestrator names missing:\n%s", out)
	}
}

func TestWriteHumanSummary_OmitsOrchestratorBlockWhenAbsent(t *testing.T) {
	rep := Report{
		K:         10,
		Aggregate: AggregateMetrics{K: 10, Count: 1, MeanRecall: 1.0},
	}
	var buf bytes.Buffer
	if err := WriteHumanSummary(&buf, rep); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Orchestrators:") {
		t.Errorf("'Orchestrators:' must be absent when OrchestratorAggregates is nil:\n%s", buf.String())
	}
}
