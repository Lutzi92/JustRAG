package vector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGetBM25KBStatsTableName(t *testing.T) {
	cases := map[int]string{
		1536: "bm25_kb_stats",
		2560: "bm25_kb_stats_2560",
		4096: "bm25_kb_stats_4096",
		768:  "bm25_kb_stats_768",
	}
	for dim, want := range cases {
		if got := GetBM25KBStatsTableName(dim); got != want {
			t.Errorf("GetBM25KBStatsTableName(%d) = %q, want %q", dim, got, want)
		}
	}
}

func TestGetBM25TermStatsTableName(t *testing.T) {
	cases := map[int]string{
		1536: "bm25_term_stats",
		2560: "bm25_term_stats_2560",
		4096: "bm25_term_stats_4096",
		768:  "bm25_term_stats_768",
	}
	for dim, want := range cases {
		if got := GetBM25TermStatsTableName(dim); got != want {
			t.Errorf("GetBM25TermStatsTableName(%d) = %q, want %q", dim, got, want)
		}
	}
}

// TestValidVectorTable_BM25StatsTables locks in the allowlist extension: the
// new bm25_kb_stats[_<dim>] / bm25_term_stats[_<dim>] names produced above
// must be accepted, and any attempted SQL-injection suffix must still be
// rejected. Mutation: dropping the new alternation branch from the regex
// makes the accept assertions fail.
func TestValidVectorTable_BM25StatsTables(t *testing.T) {
	accept := []string{
		"bm25_kb_stats", "bm25_kb_stats_4096", "bm25_kb_stats_768",
		"bm25_term_stats", "bm25_term_stats_4096", "bm25_term_stats_768",
	}
	for _, name := range accept {
		if !validVectorTable.MatchString(name) {
			t.Errorf("validVectorTable should accept %q", name)
		}
	}
	reject := []string{
		`bm25_kb_stats_4096; DROP TABLE users`,
		"bm25_kb_stats_",
		"bm25_kb_statsx_4096",
		"bm25_term_statsx",
		"bm25_kb_stats_4096x",
	}
	for _, name := range reject {
		if validVectorTable.MatchString(name) {
			t.Errorf("validVectorTable should reject %q", name)
		}
	}
}

// TestBuildTermStatsRefreshSQL pins the ts_stat() literal built for each
// arm. Mutation (named per the brief): swap the column used for the
// "simple" arm (e.g. use vector_index instead of vector_index_simple) ->
// this test fails.
func TestBuildTermStatsRefreshSQL(t *testing.T) {
	kbID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	langSQL := buildTermStatsRefreshSQL(kbID, 768, "lang")
	wantLang := `ts_stat($$SELECT vector_index FROM "document_chunks_768" WHERE kb_id = '11111111-1111-1111-1111-111111111111'$$)`
	if !strings.Contains(langSQL, wantLang) {
		t.Errorf("lang arm SQL = %q, want to contain %q", langSQL, wantLang)
	}
	if strings.Contains(langSQL, "vector_index_simple") {
		t.Errorf("lang arm SQL must reference vector_index, not vector_index_simple: %q", langSQL)
	}

	simpleSQL := buildTermStatsRefreshSQL(kbID, 768, "simple")
	wantSimple := `ts_stat($$SELECT vector_index_simple FROM "document_chunks_768" WHERE kb_id = '11111111-1111-1111-1111-111111111111'$$)`
	if !strings.Contains(simpleSQL, wantSimple) {
		t.Errorf("simple arm SQL = %q, want to contain %q", simpleSQL, wantSimple)
	}

	// dim=1536 uses the bare document_chunks table name, mirroring
	// GetVectorTableName.
	bareSQL := buildTermStatsRefreshSQL(kbID, 1536, "lang")
	if !strings.Contains(bareSQL, `FROM "document_chunks" WHERE`) {
		t.Errorf("1536-dim SQL should use the bare table name, got %q", bareSQL)
	}
}

// TestSweep_ModeDisabled_SkipsRefreshEntirely (finding F1): when
// ModeEnabled is wired and reports false, Sweep must return (0, nil)
// without touching either pool. Uses a nil vectorDB/mainDB deliberately —
// if the mode gate did not short-circuit before the pool-nil check (or
// were bypassed), this would still trivially return 0 via that separate
// guard, so the assertion that matters is that ModeEnabled was actually
// invoked. Mutation: deleting the ModeEnabled check from Sweep still
// passes the refreshed==0 assertion (via the vectorDB nil guard) but fails
// the "was invoked" assertion.
func TestSweep_ModeDisabled_SkipsRefreshEntirely(t *testing.T) {
	called := false
	r := &BM25StatsRefresher{
		ModeEnabled: func(ctx context.Context) bool {
			called = true
			return false
		},
	}
	refreshed, err := r.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatalf("Sweep err = %v, want nil", err)
	}
	if refreshed != 0 {
		t.Fatalf("refreshed = %d, want 0", refreshed)
	}
	if !called {
		t.Fatal("ModeEnabled was never invoked — Sweep must check it before returning")
	}
}

// TestSweep_NilModeEnabled_DoesNotPanic locks in that a nil ModeEnabled
// (the common case — most callers don't wire a mode gate) behaves exactly
// like before: Sweep proceeds to its normal nil-pool short-circuit rather
// than panicking on a nil func call.
func TestSweep_NilModeEnabled_DoesNotPanic(t *testing.T) {
	r := &BM25StatsRefresher{}
	refreshed, err := r.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatalf("Sweep err = %v, want nil", err)
	}
	if refreshed != 0 {
		t.Fatalf("refreshed = %d, want 0 (no vectorDB configured)", refreshed)
	}
}

// TestRunBM25Sweep_CountsAttemptsNotSuccesses (finding F2): two stale
// targets, the first refresh fails, limit=1 -> exactly one ATTEMPT is
// made (not one success then a retry into the second target). Mutation:
// reverting the budget check to `if limit > 0 && successes >= limit`
// (counting only non-error refreshes) would let this loop attempt the
// second target too, since the first "succeeded" 0 times — that turns the
// attemptedKBs-length and calls-len assertions below to 2, failing this test.
func TestRunBM25Sweep_CountsAttemptsNotSuccesses(t *testing.T) {
	targets := []bm25SweepTarget{
		{kbID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), dim: 768},
		{kbID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), dim: 768},
	}
	var calls []uuid.UUID
	refreshFn := func(ctx context.Context, kbID uuid.UUID, dim int) error {
		calls = append(calls, kbID)
		return errors.New("boom: refresh always fails in this test")
	}

	attempted := runBM25Sweep(context.Background(), targets, 1, refreshFn)

	if attempted != 1 {
		t.Fatalf("attempted = %d, want 1", attempted)
	}
	if len(calls) != 1 {
		t.Fatalf("refreshFn called %d times, want exactly 1", len(calls))
	}
	if calls[0] != targets[0].kbID {
		t.Fatalf("wrong kb attempted first: got %s, want %s", calls[0], targets[0].kbID)
	}
}

// TestRunBM25Sweep_UnlimitedAttemptsEveryTarget locks in the limit=0
// ("unlimited") convention runBM25Sweep shares with Sweep.
func TestRunBM25Sweep_UnlimitedAttemptsEveryTarget(t *testing.T) {
	targets := []bm25SweepTarget{
		{kbID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), dim: 768},
		{kbID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), dim: 4096},
		{kbID: uuid.MustParse("33333333-3333-3333-3333-333333333333"), dim: 4096},
	}
	calls := 0
	refreshFn := func(ctx context.Context, kbID uuid.UUID, dim int) error {
		calls++
		return nil
	}
	attempted := runBM25Sweep(context.Background(), targets, 0, refreshFn)
	if attempted != len(targets) || calls != len(targets) {
		t.Fatalf("attempted=%d calls=%d, want %d each", attempted, calls, len(targets))
	}
}
