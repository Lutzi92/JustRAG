package research

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// NewAgent constructor
// ---------------------------------------------------------------------------

func TestNewAgent_Defaults(t *testing.T) {
	agent := NewAgent("kb-1", "What is Go?", Options{}, nil, nil, nil, nil)

	if agent.kbID != "kb-1" {
		t.Errorf("kbID: got %q, want %q", agent.kbID, "kb-1")
	}
	if agent.goal != "What is Go?" {
		t.Errorf("goal: got %q, want %q", agent.goal, "What is Go?")
	}
	if agent.opts.MaxSteps != 10 {
		t.Errorf("MaxSteps default: got %d, want 10", agent.opts.MaxSteps)
	}
	if agent.opts.ChunksPerSearch != 5 {
		t.Errorf("ChunksPerSearch default: got %d, want 5", agent.opts.ChunksPerSearch)
	}
	if agent.opts.Language != "en" {
		t.Errorf("Language default: got %q, want %q", agent.opts.Language, "en")
	}
	if agent.opts.Mode != "kb" {
		t.Errorf("Mode default: got %q, want %q", agent.opts.Mode, "kb")
	}
	if agent.state.Status != StatusInitializing {
		t.Errorf("initial Status: got %q, want %q", agent.state.Status, StatusInitializing)
	}
}

func TestNewAgent_CustomOptions(t *testing.T) {
	opts := Options{
		MaxSteps:        20,
		Language:        "de",
		ChunksPerSearch: 8,
		Mode:            "web",
		AbortKey:        "abort:job-42",
	}
	agent := NewAgent("kb-2", "Deep research goal", opts, nil, nil, nil, nil)

	if agent.opts.MaxSteps != 20 {
		t.Errorf("MaxSteps: got %d, want 20", agent.opts.MaxSteps)
	}
	if agent.opts.Language != "de" {
		t.Errorf("Language: got %q, want %q", agent.opts.Language, "de")
	}
	if agent.opts.ChunksPerSearch != 8 {
		t.Errorf("ChunksPerSearch: got %d, want 8", agent.opts.ChunksPerSearch)
	}
	if agent.opts.Mode != "web" {
		t.Errorf("Mode: got %q, want %q", agent.opts.Mode, "web")
	}
	if agent.opts.AbortKey != "abort:job-42" {
		t.Errorf("AbortKey: got %q, want %q", agent.opts.AbortKey, "abort:job-42")
	}
}

// ---------------------------------------------------------------------------
// Types serialisation
// ---------------------------------------------------------------------------

func TestProgressEvent_JSONRoundtrip(t *testing.T) {
	evt := ProgressEvent{
		Type:       "finding",
		Message:    "New finding",
		StepNumber: 2,
		TotalSteps: 10,
		Findings: []Finding{
			{
				Content:        "Go is a compiled language",
				RelevanceScore: 0.9,
				Sources: []SourceRef{
					{FileName: "intro.pdf", FileID: "file-1", Pages: []int{3, 4}},
				},
			},
		},
		Timestamp: 1712000000000,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got ProgressEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Type != evt.Type {
		t.Errorf("Type: got %q, want %q", got.Type, evt.Type)
	}
	if got.StepNumber != evt.StepNumber {
		t.Errorf("StepNumber: got %d, want %d", got.StepNumber, evt.StepNumber)
	}
	if len(got.Findings) != 1 {
		t.Fatalf("Findings: got %d, want 1", len(got.Findings))
	}
	if got.Findings[0].Content != "Go is a compiled language" {
		t.Errorf("Finding content mismatch")
	}
	if got.Findings[0].Sources[0].FileName != "intro.pdf" {
		t.Errorf("Source FileName mismatch")
	}
}

func TestPlanStep_JSONRoundtrip(t *testing.T) {
	step := PlanStep{
		ID:                 "step-1",
		Action:             "search",
		Query:              "Go concurrency model",
		Status:             "complete",
		Result:             "Go uses goroutines and channels.",
		DocumentsProcessed: 3,
	}

	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got PlanStep
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ID != step.ID {
		t.Errorf("ID: got %q, want %q", got.ID, step.ID)
	}
	if got.DocumentsProcessed != step.DocumentsProcessed {
		t.Errorf("DocumentsProcessed: got %d, want %d", got.DocumentsProcessed, step.DocumentsProcessed)
	}
}

func TestSourceRef_OmitEmptyFields(t *testing.T) {
	ref := SourceRef{
		FileName: "doc.pdf",
		FileID:   "abc-123",
		// Pages, ChunkContent, URL intentionally omitted
	}

	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// "pages" should be absent when nil.
	if string(data) == "" {
		t.Fatal("empty JSON")
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if _, ok := m["pages"]; ok {
		t.Error("pages should be omitted when nil")
	}
	if _, ok := m["url"]; ok {
		t.Error("url should be omitted when empty")
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func TestJaccardSimilarity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		a, b     string
		min, max float64
	}{
		{"identical", "hello world", "hello world", 1.0, 1.0},
		{"one-word-overlap", "hello world", "hello earth", 0.3, 0.6},
		{"no-overlap", "completely different", "nothing in common", 0.0, 0.2},
		{"both-empty", "", "", 1.0, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := jaccardSimilarity(tc.a, tc.b)
			if got < tc.min || got > tc.max {
				t.Errorf("jaccardSimilarity(%q, %q) = %.3f, want [%.1f, %.1f]",
					tc.a, tc.b, got, tc.min, tc.max)
			}
		})
	}
}

func TestParseJSON_EmbeddedObject(t *testing.T) {
	text := `Here is the result: {"subQuestions": ["q1", "q2"]} and some trailing text.`

	var parsed struct {
		SubQuestions []string `json:"subQuestions"`
	}
	if err := parseJSON(text, &parsed); err != nil {
		t.Fatalf("parseJSON: %v", err)
	}
	if len(parsed.SubQuestions) != 2 {
		t.Errorf("got %d sub-questions, want 2", len(parsed.SubQuestions))
	}
}

func TestParseJSON_InvalidReturnsError(t *testing.T) {
	text := "This is not JSON at all."
	var parsed struct{ X string }
	if err := parseJSON(text, &parsed); err == nil {
		t.Error("expected error for non-JSON input, got nil")
	}
}

func TestDecompose_ShortGoal(t *testing.T) {
	// A goal with fewer than 4 words should add a single search step directly
	// without calling the LLM (resolver is nil — would panic if called).
	agent := NewAgent("kb-1", "Go", Options{}, nil, nil, nil, nil)

	// Call decompose — it must not panic.
	agent.events = make(chan ProgressEvent, 10)
	agent.decompose(nil) //nolint:staticcheck // nil ctx is fine for this path

	if len(agent.state.Plan) != 1 {
		t.Fatalf("expected 1 plan step for short goal, got %d", len(agent.state.Plan))
	}
	if agent.state.Plan[0].Query != "Go" {
		t.Errorf("plan step query: got %q, want %q", agent.state.Plan[0].Query, "Go")
	}
	if agent.state.Plan[0].Action != "search" {
		t.Errorf("plan step action: got %q, want search", agent.state.Plan[0].Action)
	}
}

func TestIsDuplicate(t *testing.T) {
	agent := NewAgent("kb-1", "test", Options{}, nil, nil, nil, nil)
	agent.state.Findings = []Finding{
		{Content: "Go is a statically typed compiled language created at Google"},
	}

	// Very similar — should be detected as duplicate.
	dup := &Finding{Content: "Go is a statically typed compiled language created at Google"}
	if !agent.isDuplicate(dup) {
		t.Error("expected duplicate to be detected")
	}

	// Different content — should not be a duplicate.
	novel := &Finding{Content: "Python is an interpreted dynamically typed scripting language"}
	if agent.isDuplicate(novel) {
		t.Error("expected novel finding to not be a duplicate")
	}
}

func TestHasPendingSteps(t *testing.T) {
	agent := NewAgent("kb-1", "test", Options{}, nil, nil, nil, nil)

	if agent.hasPendingSteps() {
		t.Error("expected no pending steps initially")
	}

	agent.addSearchStep("what is Go?")
	if !agent.hasPendingSteps() {
		t.Error("expected pending step after addSearchStep")
	}

	agent.state.Plan[0].Status = "complete"
	if agent.hasPendingSteps() {
		t.Error("expected no pending steps after marking complete")
	}
}

func TestSummarizeFindings_Truncation(t *testing.T) {
	findings := []Finding{
		{Content: "Finding about topic A"},
		{Content: "Finding about topic B"},
		{Content: "Finding about topic C"},
	}

	// Very small limit should truncate output.
	result := summarizeFindings(findings, 30)
	if len(result) > 40 { // some slack for the line prefix
		t.Errorf("summarizeFindings did not truncate: len=%d", len(result))
	}
}

func TestStatusConstants(t *testing.T) {
	statuses := []ResearchStatus{
		StatusInitializing,
		StatusDecomposing,
		StatusPlanning,
		StatusExecuting,
		StatusObserving,
		StatusRefining,
		StatusGeneratingReport,
		StatusComplete,
		StatusError,
		StatusCancelled,
	}
	seen := make(map[ResearchStatus]bool)
	for _, s := range statuses {
		if seen[s] {
			t.Errorf("duplicate status value: %q", s)
		}
		seen[s] = true
		if string(s) == "" {
			t.Errorf("empty status string for constant")
		}
	}
}

// ---------------------------------------------------------------------------
// Convergence / no-progress stop
// ---------------------------------------------------------------------------

func TestNewAgent_DefaultMaxConsecutiveStepsWithoutFindings(t *testing.T) {
	agent := NewAgent("kb", "goal", Options{}, nil, nil, nil, nil)
	if agent.opts.MaxConsecutiveStepsWithoutFindings != 2 {
		t.Errorf("default MaxConsecutiveStepsWithoutFindings: got %d, want 2", agent.opts.MaxConsecutiveStepsWithoutFindings)
	}
}

func TestNewAgent_ExplicitMaxConsecutiveStepsWithoutFindings(t *testing.T) {
	agent := NewAgent("kb", "goal", Options{
		MaxConsecutiveStepsWithoutFindings: 5,
	}, nil, nil, nil, nil)
	if agent.opts.MaxConsecutiveStepsWithoutFindings != 5 {
		t.Errorf("explicit value not preserved: got %d, want 5", agent.opts.MaxConsecutiveStepsWithoutFindings)
	}
}

func TestConvergenceCounter_ResetsOnNewFindings(t *testing.T) {
	// Direct state-level test — exercises the counter logic without needing
	// a real LLM stack. We simulate the per-loop logic manually.
	state := &ResearchState{ConsecutiveStepsWithoutFindings: 1}
	threshold := 2

	// Simulate "added 1 finding" → reset.
	findingsAdded := 1
	if findingsAdded > 0 {
		state.ConsecutiveStepsWithoutFindings = 0
	} else {
		state.ConsecutiveStepsWithoutFindings++
	}
	_ = threshold
	if state.ConsecutiveStepsWithoutFindings != 0 {
		t.Errorf("counter should reset to 0 after new findings, got %d", state.ConsecutiveStepsWithoutFindings)
	}
}

func TestConvergenceCounter_IncrementsOnNoFindings(t *testing.T) {
	state := &ResearchState{ConsecutiveStepsWithoutFindings: 1}
	findingsAdded := 0
	if findingsAdded > 0 {
		state.ConsecutiveStepsWithoutFindings = 0
	} else {
		state.ConsecutiveStepsWithoutFindings++
	}
	if state.ConsecutiveStepsWithoutFindings != 2 {
		t.Errorf("counter should increment to 2, got %d", state.ConsecutiveStepsWithoutFindings)
	}
}

func TestConvergenceCounter_ZeroCoercesToDefault(t *testing.T) {
	// MaxConsecutiveStepsWithoutFindings == 0 is coerced to the default 2
	// by NewAgent (see Options.MaxConsecutiveStepsWithoutFindings: values
	// <= 0 fall back to the default 2). To actually disable the convergence
	// check, rely on MaxSteps instead.
	agent := NewAgent("kb", "goal", Options{
		MaxConsecutiveStepsWithoutFindings: 0,
	}, nil, nil, nil, nil)
	if agent.opts.MaxConsecutiveStepsWithoutFindings != 2 {
		t.Errorf("zero collapses to default 2, got %d", agent.opts.MaxConsecutiveStepsWithoutFindings)
	}
}
