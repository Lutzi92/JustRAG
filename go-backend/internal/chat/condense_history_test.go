package chat

// Tests for CondenseFromHistory (Wave 2 Task 2), factored out of
// CondenseFollowUp's post-load behaviour so the eval replay (Task 3) can
// drive it from a fixture's conversation history without a DB-backed chat.
// Same rules as CondenseFollowUp: fewer than 2 history entries -> message
// unchanged; last 6 entries kept; each entry's content capped at 500 bytes;
// a nil resolver or an LLM error also leave the message unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
)

// condenseTestServer builds an httptest server that answers
// "/chat/completions" with a fixed condensed-question string, and returns
// the request counter so tests can assert the endpoint was (not) hit.
func condenseTestServer(t *testing.T, content string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func condenseTestResolver(srv *httptest.Server) *ai.ConfigResolver {
	store := &chatTestConfigStore{baseURL: srv.URL, model: "fake-model"}
	return ai.NewConfigResolver(store)
}

// TestCondenseFromHistory_ShortHistoryUnchanged locks in the `len(history) <
// 2` early return: a single history entry must not reach the LLM at all.
// Mutation: dropping the `len(history) < 2` guard would let this call
// through to ai.CondenseQuestion (history has 1 entry, so CondenseQuestion's
// own `len(history) == 0` guard does not save it), which this test's call
// counter would catch.
func TestCondenseFromHistory_ShortHistoryUnchanged(t *testing.T) {
	t.Parallel()
	srv, calls := condenseTestServer(t, "should never be returned")
	resolver := condenseTestResolver(srv)

	history := []ai.ChatHistoryEntry{{Role: "user", Content: "hi"}}
	got, err := CondenseFromHistory(context.Background(), resolver, history, "follow up", "kb-1", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "follow up" {
		t.Errorf("want unchanged message, got %q", got)
	}
	if *calls != 0 {
		t.Errorf("expected no LLM call for history < 2, got %d calls", *calls)
	}
}

// TestCondenseFromHistory_EmptyHistoryUnchanged is the zero-length variant
// of the same guard.
func TestCondenseFromHistory_EmptyHistoryUnchanged(t *testing.T) {
	t.Parallel()
	srv, calls := condenseTestServer(t, "should never be returned")
	resolver := condenseTestResolver(srv)

	got, err := CondenseFromHistory(context.Background(), resolver, nil, "follow up", "kb-1", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "follow up" {
		t.Errorf("want unchanged message, got %q", got)
	}
	if *calls != 0 {
		t.Errorf("expected no LLM call for empty history, got %d calls", *calls)
	}
}

// TestCondenseFromHistory_NilResolverUnchanged is the eval-replay case: a
// caller with no configured resolver must get the message back unchanged
// rather than panic, even with a history long enough to otherwise condense.
func TestCondenseFromHistory_NilResolverUnchanged(t *testing.T) {
	t.Parallel()
	history := []ai.ChatHistoryEntry{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
	}
	got, err := CondenseFromHistory(context.Background(), nil, history, "follow up", "kb-1", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "follow up" {
		t.Errorf("want unchanged message, got %q", got)
	}
}

// TestCondenseFromHistory_LLMSuccess verifies the happy path wires the
// windowed history through to ai.CondenseQuestion and returns its result.
func TestCondenseFromHistory_LLMSuccess(t *testing.T) {
	t.Parallel()
	srv, calls := condenseTestServer(t, "Wann fand der Workshop statt?")
	resolver := condenseTestResolver(srv)

	history := []ai.ChatHistoryEntry{
		{Role: "user", Content: "Wer leitet den Workshop?"},
		{Role: "assistant", Content: "Dr. Müller leitet den Workshop."},
	}
	got, err := CondenseFromHistory(context.Background(), resolver, history, "und wann?", "kb-1", "de")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Wann fand der Workshop statt?" {
		t.Errorf("unexpected result: %q", got)
	}
	if *calls != 1 {
		t.Errorf("expected exactly 1 LLM call, got %d", *calls)
	}
}

// TestCondenseFromHistory_LLMErrorUnchanged locks in the fail-open path: an
// LLM error must leave the original message intact rather than surface the
// error to the caller. A short context deadline keeps the retry loop's
// exponential backoff from slowing the test down (the loop checks ctx.Done
// before each retry sleep and returns immediately once the deadline is
// past).
func TestCondenseFromHistory_LLMErrorUnchanged(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	resolver := condenseTestResolver(srv)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	history := []ai.ChatHistoryEntry{
		{Role: "user", Content: "Wer leitet den Workshop?"},
		{Role: "assistant", Content: "Dr. Müller leitet den Workshop."},
	}
	got, err := CondenseFromHistory(ctx, resolver, history, "und wann?", "kb-1", "de")
	if err != nil {
		t.Fatalf("expected fail-open (nil error), got %v", err)
	}
	if got != "und wann?" {
		t.Errorf("want unchanged message on LLM error, got %q", got)
	}
}

// TestHistoryWindow_CapsToLastSix locks in the "keep the last 6" rule.
// Mutation: changing 6 to 5 drops the window to 5 entries and fails this
// assertion.
func TestHistoryWindow_CapsToLastSix(t *testing.T) {
	t.Parallel()
	history := make([]ai.ChatHistoryEntry, 0, 8)
	for i := 0; i < 8; i++ {
		history = append(history, ai.ChatHistoryEntry{
			Role:    "user",
			Content: fmt.Sprintf("turn-%d", i),
		})
	}

	got := historyWindow(history)
	if len(got) != 6 {
		t.Fatalf("want 6 entries, got %d", len(got))
	}
	// The window must be the LAST 6, oldest of the kept turns first.
	if got[0].Content != "turn-2" || got[5].Content != "turn-7" {
		t.Errorf("unexpected window contents: first=%q last=%q", got[0].Content, got[5].Content)
	}
}

// TestHistoryWindow_ShortHistoryPassesThrough ensures fewer than 6 entries
// are returned as-is (no padding, no truncation of the slice itself).
func TestHistoryWindow_ShortHistoryPassesThrough(t *testing.T) {
	t.Parallel()
	history := []ai.ChatHistoryEntry{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	}
	got := historyWindow(history)
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
}

// TestHistoryWindow_TruncatesContentTo500Bytes locks in the per-entry
// content cap.
func TestHistoryWindow_TruncatesContentTo500Bytes(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 600)
	history := []ai.ChatHistoryEntry{
		{Role: "user", Content: "short"},
		{Role: "assistant", Content: long},
	}
	got := historyWindow(history)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].Content != "short" {
		t.Errorf("short content must not be altered, got %q", got[0].Content)
	}
	if len(got[1].Content) != 500 {
		t.Errorf("want content capped at 500 bytes, got %d bytes", len(got[1].Content))
	}
	if got[1].Content != long[:500] {
		t.Errorf("capped content does not match expected prefix")
	}
}

// TestHistoryWindow_TruncationBoundary pins the cap to exactly 500 bytes at
// the boundary (501 bytes truncated, 500 bytes untouched). Mutation: loosening
// the comparison to `> 501` would leave the 501-byte case uncapped, which
// only a boundary-exact test — not TestHistoryWindow_TruncatesContentTo500Bytes's
// 600-byte input — can catch.
func TestHistoryWindow_TruncationBoundary(t *testing.T) {
	t.Parallel()
	exactly500 := strings.Repeat("y", 500)
	exactly501 := strings.Repeat("y", 501)
	history := []ai.ChatHistoryEntry{
		{Role: "user", Content: exactly500},
		{Role: "assistant", Content: exactly501},
	}
	got := historyWindow(history)
	if got[0].Content != exactly500 {
		t.Errorf("500-byte content must pass through untouched, got %d bytes", len(got[0].Content))
	}
	if len(got[1].Content) != 500 {
		t.Errorf("501-byte content must be capped to 500 bytes, got %d bytes", len(got[1].Content))
	}
}

// TestHistoryWindow_RolePreserved ensures the window doesn't drop or alter
// the Role field while capping Content.
func TestHistoryWindow_RolePreserved(t *testing.T) {
	t.Parallel()
	history := []ai.ChatHistoryEntry{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	got := historyWindow(history)
	if got[0].Role != "user" || got[1].Role != "assistant" {
		t.Errorf("roles not preserved: %+v", got)
	}
}

// TestRawQueryForRetrieval_TableTest moved here from condense_raw_test.go
// alongside the CondenseFromHistory rename work (Wave 2 Task 2): it covers
// the exported RawQueryForRetrieval helper CondenseFollowUp's callers use
// to decide whether the raw utterance is worth forwarding as an extra
// retrieval query.
func TestRawQueryForRetrieval_TableTest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		enabled        bool
		raw, condensed string
		want           string
	}{
		{"disabled", false, "und der?", "Wer leitet den Workshop?", ""},
		{"identical", true, " Wer leitet den Workshop? ", "Wer leitet den Workshop?", ""},
		{"empty raw", true, "", "x", ""},
		{"condensed", true, "und wann?", "Wann fand der Workshop statt?", "und wann?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := RawQueryForRetrieval(c.enabled, c.raw, c.condensed); got != c.want {
				t.Errorf("%s: want %q, got %q", c.name, c.want, got)
			}
		})
	}
}
