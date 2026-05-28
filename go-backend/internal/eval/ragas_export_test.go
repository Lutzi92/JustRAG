package eval

import (
	"encoding/json"
	"testing"
)

func TestExportRAGAS_ShapeAndDeterminism(t *testing.T) {
	rep := Report{
		Questions: []QuestionReport{
			{
				Question:  Question{ID: "q1", Question: "what is X?", Language: "en"},
				Retrieved: []RetrievedChunk{{FileID: "f1", Score: 0.9}},
				Contents:  []string{"context one", "context two"},
				Judge: &JudgeMetrics{
					Answer: "X is Y.",
				},
			},
		},
	}

	got := ExportRAGAS(rep)

	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	row := got[0]
	if row["question"] != "what is X?" {
		t.Errorf("question: got %v, want \"what is X?\"", row["question"])
	}
	if row["answer"] != "X is Y." {
		t.Errorf("answer: got %v, want \"X is Y.\"", row["answer"])
	}
	contexts, ok := row["contexts"].([]string)
	if !ok {
		t.Fatalf("contexts: got %T, want []string", row["contexts"])
	}
	if len(contexts) != 2 || contexts[0] != "context one" || contexts[1] != "context two" {
		t.Errorf("contexts: got %v", contexts)
	}
	if row["ground_truth"] != "" {
		t.Errorf("ground_truth: got %v, want empty (golden_answer field not yet on Question)", row["ground_truth"])
	}

	// Determinism: same inputs → byte-identical JSON.
	b1, _ := json.Marshal(got)
	b2, _ := json.Marshal(ExportRAGAS(rep))
	if string(b1) != string(b2) {
		t.Error("ExportRAGAS not deterministic across calls with same inputs")
	}
}

func TestExportRAGAS_MissingJudgeYieldsEmptyAnswer(t *testing.T) {
	rep := Report{
		Questions: []QuestionReport{
			{
				Question: Question{ID: "q1", Question: "Q?", Language: "en"},
				Contents: []string{"c1"},
				// No Judge → no answer was generated
			},
		},
	}
	got := ExportRAGAS(rep)
	if got[0]["answer"] != "" {
		t.Errorf("missing judge: answer should be empty, got %v", got[0]["answer"])
	}
}

func TestExportRAGAS_NilContentsYieldsEmptySlice(t *testing.T) {
	rep := Report{
		Questions: []QuestionReport{
			{
				Question: Question{ID: "q1", Question: "Q?", Language: "en"},
				// Contents not set
			},
		},
	}
	got := ExportRAGAS(rep)
	contexts, ok := got[0]["contexts"].([]string)
	if !ok {
		t.Fatalf("contexts: got %T, want []string", got[0]["contexts"])
	}
	if len(contexts) != 0 {
		t.Errorf("nil contents: want empty slice, got %v", contexts)
	}
}
