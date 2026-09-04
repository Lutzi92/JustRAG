package chat

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/justrag/go-backend/internal/tabular"
)

// stubSiteConfigNoAICalls disables factcheck and citation validation so
// runPostResponseTasks does not spend a real LLM/embedding call against a
// nil ai.ConfigResolver during these tests — every other boolean gate in
// the package already defaults to false.
type stubSiteConfigNoAICalls struct{}

func (stubSiteConfigNoAICalls) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	switch key {
	case "factcheck_in_chat", "citation_validation_enabled":
		v := "false"
		return &v, nil
	default:
		return nil, nil
	}
}

// fakeTabularQueryLogger records every InsertQueryLog call. err, when set,
// is returned to the caller (never propagated further by
// runPostResponseTasks — the point of this fake is exercising that path).
type fakeTabularQueryLogger struct {
	mu      sync.Mutex
	entries []tabular.QueryLogEntry
	err     error
}

func (f *fakeTabularQueryLogger) InsertQueryLog(_ context.Context, e tabular.QueryLogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return f.err
}

func (f *fakeTabularQueryLogger) calls() []tabular.QueryLogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tabular.QueryLogEntry(nil), f.entries...)
}

func newTabularTestHandler(logger TabularQueryLogger) *Handler {
	return NewHandler(newMockStore(), nil, nil,
		WithSiteConfigReader(stubSiteConfigNoAICalls{}),
		WithTabularQueryLog(logger),
	)
}

func TestRunPostResponseTasks_TabularTrace_InsertsQueryLog(t *testing.T) {
	logger := &fakeTabularQueryLogger{}
	h := newTabularTestHandler(logger)

	trace := &TabularTrace{
		Fired:    true,
		Outcome:  "fired_ok",
		SQL:      "SELECT count(*) FROM tabular.t1",
		RowCount: 3,
		Repairs:  1,
		Question: "how many rows?",
	}

	const aiMsgID = "ai-msg-123"
	h.runPostResponseTasks(context.Background(), "how many rows?", "there are 3 rows", "context", "kb-1", "en", aiMsgID, nil, nil, trace)

	calls := logger.calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 InsertQueryLog call, got %d", len(calls))
	}
	got := calls[0]
	if got.MessageID != aiMsgID {
		t.Errorf("MessageID = %q, want %q", got.MessageID, aiMsgID)
	}
	if got.KBID != "kb-1" {
		t.Errorf("KBID = %q, want kb-1", got.KBID)
	}
	if got.Outcome != "fired_ok" {
		t.Errorf("Outcome = %q, want fired_ok", got.Outcome)
	}
	if got.SQL != trace.SQL {
		t.Errorf("SQL = %q, want %q", got.SQL, trace.SQL)
	}
	if got.RowCount != trace.RowCount {
		t.Errorf("RowCount = %d, want %d", got.RowCount, trace.RowCount)
	}
	if got.Question != trace.Question {
		t.Errorf("Question = %q, want %q", got.Question, trace.Question)
	}
}

// TestRunPostResponseTasks_TabularTrace_EmptyMessageID is the mutation guard
// called out in the task brief: passing "" as the AI message id must be
// visible on the logged row (i.e. the call must thread the actual aiMsgID
// parameter through, not a hardcoded/forgotten value).
func TestRunPostResponseTasks_TabularTrace_EmptyMessageID(t *testing.T) {
	logger := &fakeTabularQueryLogger{}
	h := newTabularTestHandler(logger)

	trace := &TabularTrace{Fired: true, Outcome: "fired_ok", Question: "q"}
	h.runPostResponseTasks(context.Background(), "q", "a", "context", "kb-1", "en", "", nil, nil, trace)

	calls := logger.calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 InsertQueryLog call, got %d", len(calls))
	}
	if calls[0].MessageID != "" {
		t.Errorf("MessageID = %q, want empty string to round-trip", calls[0].MessageID)
	}
}

// TestRunPostResponseTasks_SkippedDisabled_NoQueryLogCall and
// TestRunPostResponseTasks_SkippedNoTables_NoQueryLogCall are the R59
// storage-growth fix: with the router attached to every KB, these two
// outcomes fire on essentially every chat turn deployment-wide (no router
// wired at all / a KB with no tabular data), so logging them would grow
// tabular_query_log unboundedly for zero analytical value. Every other
// skipped_* reason only occurs on a KB that DOES have tables and stays
// logged (see TestRunPostResponseTasks_TabularTrace_InsertsQueryLog and the
// package's other skipped_* outcomes, which are not filtered).
func TestRunPostResponseTasks_SkippedDisabled_NoQueryLogCall(t *testing.T) {
	logger := &fakeTabularQueryLogger{}
	h := newTabularTestHandler(logger)

	trace := &TabularTrace{Fired: false, Outcome: "skipped_disabled", Question: "q"}
	h.runPostResponseTasks(context.Background(), "q", "a", "context", "kb-1", "en", "ai-msg-1", nil, nil, trace)

	if calls := logger.calls(); len(calls) != 0 {
		t.Fatalf("want 0 InsertQueryLog calls for skipped_disabled, got %d", len(calls))
	}
}

func TestRunPostResponseTasks_SkippedNoTables_NoQueryLogCall(t *testing.T) {
	logger := &fakeTabularQueryLogger{}
	h := newTabularTestHandler(logger)

	trace := &TabularTrace{Fired: false, Outcome: "skipped_no_tables", Question: "q"}
	h.runPostResponseTasks(context.Background(), "q", "a", "context", "kb-1", "en", "ai-msg-1", nil, nil, trace)

	if calls := logger.calls(); len(calls) != 0 {
		t.Fatalf("want 0 InsertQueryLog calls for skipped_no_tables, got %d", len(calls))
	}
}

func TestRunPostResponseTasks_NilTrace_NoQueryLogCall(t *testing.T) {
	logger := &fakeTabularQueryLogger{}
	h := newTabularTestHandler(logger)

	h.runPostResponseTasks(context.Background(), "q", "a", "context", "kb-1", "en", "ai-msg-1", nil, nil, nil)

	if calls := logger.calls(); len(calls) != 0 {
		t.Fatalf("want 0 InsertQueryLog calls for a nil trace, got %d", len(calls))
	}
}

func TestRunPostResponseTasks_LoggerError_DoesNotPanicOrFailTurn(t *testing.T) {
	logger := &fakeTabularQueryLogger{err: errors.New("insert failed")}
	h := newTabularTestHandler(logger)

	trace := &TabularTrace{Fired: true, Outcome: "sql_error", Question: "q"}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("runPostResponseTasks panicked on a logger error: %v", r)
		}
	}()

	followUps, verification, refined := h.runPostResponseTasks(context.Background(), "q", "a", "context", "kb-1", "en", "ai-msg-1", nil, nil, trace)
	_ = followUps
	_ = verification
	_ = refined

	if calls := logger.calls(); len(calls) != 1 {
		t.Fatalf("want 1 InsertQueryLog attempt despite the error, got %d", len(calls))
	}
}

func TestRunPostResponseTasks_NilQueryLogger_Noop(t *testing.T) {
	h := NewHandler(newMockStore(), nil, nil, WithSiteConfigReader(stubSiteConfigNoAICalls{}))
	trace := &TabularTrace{Fired: true, Outcome: "fired_ok", Question: "q"}

	// Must not panic on a nil h.tabularQueryLog even with a non-nil trace.
	h.runPostResponseTasks(context.Background(), "q", "a", "context", "kb-1", "en", "ai-msg-1", nil, nil, trace)
}
