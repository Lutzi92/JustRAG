package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- test doubles -----------------------------------------------------------

type fixedSearcher struct{ chunks []RetrievedChunk }

func (f *fixedSearcher) Search(_ context.Context, _ Question, k int) ([]RetrievedChunk, error) {
	if k < len(f.chunks) {
		return f.chunks[:k], nil
	}
	return f.chunks, nil
}

type errorOnIDSearcher struct {
	failOnID string
	chunks   []RetrievedChunk
}

func (e *errorOnIDSearcher) Search(_ context.Context, q Question, k int) ([]RetrievedChunk, error) {
	if q.ID == e.failOnID {
		return nil, errors.New("stub search failure")
	}
	if k < len(e.chunks) {
		return e.chunks[:k], nil
	}
	return e.chunks, nil
}

// --- tests ------------------------------------------------------------------

func TestRunEval_HappyPath(t *testing.T) {
	qs := []Question{
		{ID: "q1", Question: "why?", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}},
	}
	searcher := &fixedSearcher{chunks: []RetrievedChunk{
		{FileID: "f1", Score: 0.9},
		{FileID: "fx", Score: 0.4},
	}}
	rep, err := RunEval(context.Background(), searcher, qs, 10, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rep.Questions) != 1 {
		t.Fatalf("expected 1 question report, got %d", len(rep.Questions))
	}
	qr := rep.Questions[0]
	if qr.Error != "" {
		t.Fatalf("unexpected per-question error: %s", qr.Error)
	}
	if qr.Metrics.RecallAtK != 1.0 {
		t.Fatalf("expected recall 1.0, got %v", qr.Metrics.RecallAtK)
	}
	if qr.Metrics.ReciprocalRank != 1.0 {
		t.Fatalf("expected rr 1.0, got %v", qr.Metrics.ReciprocalRank)
	}
	if rep.Aggregate.Count != 1 {
		t.Fatalf("expected aggregate count 1, got %d", rep.Aggregate.Count)
	}
	if rep.Errors != 0 {
		t.Fatalf("expected 0 errors, got %d", rep.Errors)
	}
}

func TestRunEval_MatchByFileName(t *testing.T) {
	// Golden authored by name only — a chunk whose FileName matches the
	// label scores 1.0 even though its FileID is some unrelated UUID. This
	// is what makes goldens survive a re-ingest that regenerates UUIDs.
	qs := []Question{
		{
			ID:                "q1",
			Question:          "why?",
			KbID:              "k",
			Language:          "en",
			MustCiteFileNames: []string{"Foo.md"},
		},
	}
	searcher := &fixedSearcher{chunks: []RetrievedChunk{
		{FileID: "new-uuid-after-reupload", FileName: "Foo.md", Score: 0.9},
		{FileID: "other-uuid", FileName: "Bar.md", Score: 0.4},
	}}
	rep, err := RunEval(context.Background(), searcher, qs, 10, 1)
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	got := rep.Questions[0].Metrics
	if got.RecallAtK != 1.0 {
		t.Errorf("recall by name: want 1.0, got %v", got.RecallAtK)
	}
	if got.ReciprocalRank != 1.0 {
		t.Errorf("rr by name: want 1.0, got %v", got.ReciprocalRank)
	}
}

func TestRunEval_MatchByEitherIdOrName(t *testing.T) {
	// Golden authored with both lists: two distinct files, one by id and
	// the other by name. Retrieved set has the right chunk for each
	// independently — recall should be 1.0 (2/2).
	qs := []Question{
		{
			ID:                "q1",
			Question:          "q",
			KbID:              "k",
			Language:          "en",
			MustCiteFileIDs:   []string{"file-uuid-A"},
			MustCiteFileNames: []string{"B.md"},
		},
	}
	searcher := &fixedSearcher{chunks: []RetrievedChunk{
		{FileID: "file-uuid-A", FileName: "A.md", Score: 0.9},
		{FileID: "unrelated", FileName: "B.md", Score: 0.6},
	}}
	rep, err := RunEval(context.Background(), searcher, qs, 10, 1)
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if got := rep.Questions[0].Metrics.RecallAtK; got != 1.0 {
		t.Errorf("union match: want recall 1.0, got %v", got)
	}
}

func TestRunEval_SearchErrorCapturedNotFatal(t *testing.T) {
	qs := []Question{
		{ID: "q1", Question: "q", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}},
		{ID: "q2", Question: "q", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f2"}},
	}
	searcher := &errorOnIDSearcher{failOnID: "q1", chunks: []RetrievedChunk{{FileID: "f2", Score: 0.8}}}
	rep, err := RunEval(context.Background(), searcher, qs, 10, 1)
	if err != nil {
		t.Fatalf("RunEval should not return error for per-question failures, got %v", err)
	}
	if rep.Errors != 1 {
		t.Fatalf("expected 1 error, got %d", rep.Errors)
	}
	if len(rep.Questions) < 2 {
		t.Fatalf("expected at least 2 question reports, got %d", len(rep.Questions))
	}
	if rep.Questions[0].Error == "" {
		t.Fatal("expected q1 to have captured error")
	}
	if rep.Questions[1].Error != "" {
		t.Fatalf("expected q2 to succeed, got error %q", rep.Questions[1].Error)
	}
	if rep.Aggregate.Count != 1 {
		t.Fatalf("aggregate count should exclude errored row, got %d", rep.Aggregate.Count)
	}
}

func TestRunEval_TruncatesToK(t *testing.T) {
	qs := []Question{{ID: "q1", Question: "q", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f5"}}}
	// 20 hits where only position 15 is relevant; k=10 → recall must be 0.
	chunks := make([]RetrievedChunk, 20)
	for i := range chunks {
		chunks[i] = RetrievedChunk{FileID: "fx", Score: float64(20 - i)}
	}
	chunks[14] = RetrievedChunk{FileID: "f5", Score: 0.1}
	searcher := &fixedSearcher{chunks: chunks}
	rep, err := RunEval(context.Background(), searcher, qs, 10, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rep.Questions) == 0 {
		t.Fatalf("expected at least 1 question report, got empty slice: %+v", rep)
	}
	if rep.Questions[0].Metrics.RecallAtK != 0.0 {
		t.Fatalf("expected recall 0 (relevant beyond k=10), got %v", rep.Questions[0].Metrics.RecallAtK)
	}
}

func TestRunEvalWithJudge_OptInProducesJudgeMetrics(t *testing.T) {
	qs := []Question{{ID: "q1", Question: "why?", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}}}
	searcher := &fixedSearcher{chunks: []RetrievedChunk{{FileID: "f1", Score: 0.9}}}

	answerCompleter := &fakeCompleter{returnAnswer: "an answer"}
	judgeCompleter := &scriptedCompleter{responses: []string{
		`{"claims":[{"text":"c1","supported":true}]}`,
		`{"score":5,"reasoning":"perfect"}`,
		`{"relevant":[true]}`,
	}}

	cfg := JudgeConfig{
		Enabled:         true,
		Judge:           NewJudge(judgeCompleter),
		AnswerCompleter: answerCompleter,
		ContentLoader: func(_ context.Context, chunks []RetrievedChunk) ([]string, []string, error) {
			return []string{"content"}, []string{"file.pdf"}, nil
		},
	}
	rep, err := RunEvalWithJudge(context.Background(), searcher, qs, 10, 1, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	qr := rep.Questions[0]
	if qr.Judge == nil {
		t.Fatal("expected judge metrics")
	}
	if qr.Judge.Faithfulness == nil || *qr.Judge.Faithfulness != 1.0 {
		t.Errorf("faithfulness unexpected: %+v", qr.Judge.Faithfulness)
	}
	if qr.Judge.AnswerRelevance == nil || *qr.Judge.AnswerRelevance != 1.0 {
		t.Errorf("relevance unexpected: %+v", qr.Judge.AnswerRelevance)
	}
	if qr.Judge.Answer != "an answer" {
		t.Errorf("answer unexpected: %q", qr.Judge.Answer)
	}
	if rep.Aggregate.JudgedCount != 1 {
		t.Errorf("expected judged count 1, got %d", rep.Aggregate.JudgedCount)
	}
}

func TestRunEvalWithJudge_DisabledMatchesPhase21(t *testing.T) {
	qs := []Question{{ID: "q1", Question: "q", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}}}
	searcher := &fixedSearcher{chunks: []RetrievedChunk{{FileID: "f1", Score: 0.9}}}

	rep, err := RunEvalWithJudge(context.Background(), searcher, qs, 10, 1, JudgeConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Questions[0].Judge != nil {
		t.Errorf("expected no judge metrics when disabled, got %+v", rep.Questions[0].Judge)
	}
}

func TestRunEvalWithJudge_AnswerErrorCaptured(t *testing.T) {
	qs := []Question{{ID: "q1", Question: "q", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}}}
	searcher := &fixedSearcher{chunks: []RetrievedChunk{{FileID: "f1", Score: 0.9}}}

	cfg := JudgeConfig{
		Enabled:         true,
		Judge:           NewJudge(&scriptedCompleter{}),
		AnswerCompleter: &fakeCompleter{returnErr: errorsNew("llm down")},
		ContentLoader: func(_ context.Context, _ []RetrievedChunk) ([]string, []string, error) {
			return []string{"c"}, []string{"f"}, nil
		},
	}
	rep, err := RunEvalWithJudge(context.Background(), searcher, qs, 10, 1, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	qr := rep.Questions[0]
	if qr.Judge == nil || len(qr.Judge.JudgeErrors) != 1 {
		t.Fatalf("expected one captured judge error, got %+v", qr.Judge)
	}
	if !strings.Contains(qr.Judge.JudgeErrors[0], "answer") {
		t.Errorf("expected error to mention answer, got %q", qr.Judge.JudgeErrors[0])
	}
}

// --- agentTracer integration -----------------------------------------------

type traceSearcherStub struct {
	results map[string][]RetrievedChunk
	traces  map[string]*AgentTrace
}

func (s *traceSearcherStub) Search(_ context.Context, q Question, _ int) ([]RetrievedChunk, error) {
	return s.results[q.ID], nil
}

func (s *traceSearcherStub) AgentTraceForQuestion(id string) *AgentTrace {
	return s.traces[id]
}

func TestRunEval_AttachesAgentTrace(t *testing.T) {
	stub := &traceSearcherStub{
		results: map[string][]RetrievedChunk{
			"q1": {{FileID: "f1", Score: 0.9}},
			"q2": {{FileID: "f2", Score: 0.7}},
		},
		traces: map[string]*AgentTrace{
			"q1": {Orchestrator: OrchestratorSupervisor, DispatchReason: "complex_reasoning_supervisor_gate"},
			"q2": {Orchestrator: OrchestratorStandard, DispatchReason: "fallback_query_type_lookup"},
		},
	}
	questions := []Question{
		{ID: "q1", Question: "?", KbID: "kb", MustCiteFileIDs: []string{"f1"}},
		{ID: "q2", Question: "?", KbID: "kb", MustCiteFileIDs: []string{"f2"}},
	}
	rep, err := RunEval(context.Background(), stub, questions, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Questions[0].Agent == nil || rep.Questions[0].Agent.Orchestrator != OrchestratorSupervisor {
		t.Fatalf("q1 trace not attached: %+v", rep.Questions[0].Agent)
	}
	if rep.Questions[1].Agent == nil || rep.Questions[1].Agent.Orchestrator != OrchestratorStandard {
		t.Fatalf("q2 trace not attached: %+v", rep.Questions[1].Agent)
	}
	if rep.OrchestratorAggregates[OrchestratorSupervisor].Count != 1 {
		t.Fatalf("OrchestratorAggregates[supervisor].Count = %d, want 1; full map = %+v",
			rep.OrchestratorAggregates[OrchestratorSupervisor].Count, rep.OrchestratorAggregates)
	}
	if rep.OrchestratorAggregates[OrchestratorStandard].Count != 1 {
		t.Fatalf("OrchestratorAggregates[standard].Count = %d, want 1", rep.OrchestratorAggregates[OrchestratorStandard].Count)
	}
}

func TestRunEval_LegacySearcherLeavesAgentNil(t *testing.T) {
	// fixedSearcher does NOT implement agentTracer — make sure RunEval
	// doesn't populate Agent or OrchestratorAggregates in that case.
	s := &fixedSearcher{chunks: []RetrievedChunk{{FileID: "f1", Score: 0.9}}}
	questions := []Question{{ID: "q1", Question: "?", KbID: "kb", MustCiteFileIDs: []string{"f1"}}}
	rep, err := RunEval(context.Background(), s, questions, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Questions[0].Agent != nil {
		t.Fatalf("Agent must stay nil for legacy adapter; got %+v", rep.Questions[0].Agent)
	}
	if rep.OrchestratorAggregates != nil {
		t.Fatalf("OrchestratorAggregates must be nil for legacy adapter; got %+v", rep.OrchestratorAggregates)
	}
}
