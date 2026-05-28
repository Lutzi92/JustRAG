package eval

import "testing"

func TestAggregateByOrchestrator_BucketsByLabel(t *testing.T) {
	reports := []QuestionReport{
		{Agent: &AgentTrace{Orchestrator: OrchestratorSupervisor},
			Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.8, ReciprocalRank: 1.0}},
		{Agent: &AgentTrace{Orchestrator: OrchestratorSupervisor},
			Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.6, ReciprocalRank: 0.5}},
		{Agent: &AgentTrace{Orchestrator: OrchestratorStandard},
			Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.9, ReciprocalRank: 1.0}},
	}
	got := AggregateByOrchestrator(reports, 10)
	if len(got) != 2 {
		t.Fatalf("want 2 buckets, got %d (%v)", len(got), got)
	}
	if got[OrchestratorSupervisor].Count != 2 {
		t.Fatalf("supervisor.Count = %d, want 2", got[OrchestratorSupervisor].Count)
	}
	if got[OrchestratorStandard].Count != 1 {
		t.Fatalf("standard.Count = %d, want 1", got[OrchestratorStandard].Count)
	}
	mean := got[OrchestratorSupervisor].MeanRecall
	if mean < 0.69 || mean > 0.71 {
		t.Fatalf("supervisor.MeanRecall = %f, want ~0.70", mean)
	}
}

func TestAggregateByOrchestrator_SkipsNilAgent(t *testing.T) {
	reports := []QuestionReport{
		{Agent: nil, Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.5}},
		{Agent: &AgentTrace{Orchestrator: OrchestratorSupervisor},
			Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.8}},
	}
	got := AggregateByOrchestrator(reports, 10)
	if len(got) != 1 {
		t.Fatalf("want 1 bucket (supervisor only), got %d", len(got))
	}
	if _, hasEmpty := got[""]; hasEmpty {
		t.Fatal("nil-Agent rows must not produce an empty-key bucket")
	}
}

func TestAggregateByOrchestrator_SkipsErrored(t *testing.T) {
	reports := []QuestionReport{
		{Agent: &AgentTrace{Orchestrator: OrchestratorSupervisor}, Error: "boom"},
		{Agent: &AgentTrace{Orchestrator: OrchestratorSupervisor},
			Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.8}},
	}
	got := AggregateByOrchestrator(reports, 10)
	if got[OrchestratorSupervisor].Count != 1 {
		t.Fatalf("errored row must be excluded; Count = %d, want 1", got[OrchestratorSupervisor].Count)
	}
}

func TestAggregateByOrchestrator_NilWhenEmpty(t *testing.T) {
	if got := AggregateByOrchestrator(nil, 10); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	reports := []QuestionReport{
		{Agent: nil, Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.5}},
	}
	if got := AggregateByOrchestrator(reports, 10); got != nil {
		t.Fatalf("expected nil when only nil-Agent rows, got %v", got)
	}
}
