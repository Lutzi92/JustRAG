package agents

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

// optsRecorder captures the SearchOptions a specialist built, so the test
// can assert the Input → SearchOptions forwarding rather than the search
// result.
type optsRecorder struct {
	got vector.SearchOptions
}

func (r *optsRecorder) Search(_ context.Context, _, _ string, _ int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	r.got = opts
	return &vector.SearchResult{Chunks: []vector.SearchChunk{chunk("hit", 0.9)}}, nil
}

// TestSpecialistsForwardForceBM25SimpleArm guards the retrieval hint the
// chat layer's tabular router sets: without the forwarding, a Supervisor
// turn on a spreadsheet KB silently loses the forced simple keyword arm
// (the standard path would still have it — the two paths would diverge).
func TestSpecialistsForwardForceBM25SimpleArm(t *testing.T) {
	t.Parallel()

	t.Run("retriever", func(t *testing.T) {
		t.Parallel()
		rec := &optsRecorder{}
		a := NewRetrieverAgent(rec, "")
		if _, err := a.Execute(context.Background(), Input{KbID: "kb", Query: "q", ForceBM25SimpleArm: true}); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !rec.got.ForceBM25SimpleArm {
			t.Fatal("RetrieverAgent dropped Input.ForceBM25SimpleArm")
		}
	})

	t.Run("enumerator", func(t *testing.T) {
		t.Parallel()
		rec := &optsRecorder{}
		a := NewEnumeratorAgent(rec, "", func(_, _ string) bool { return true })
		if _, err := a.Execute(context.Background(), Input{KbID: "kb", Query: "q", ForceBM25SimpleArm: true}); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !rec.got.ForceBM25SimpleArm {
			t.Fatal("EnumeratorAgent dropped Input.ForceBM25SimpleArm")
		}
	})

	t.Run("default stays off", func(t *testing.T) {
		t.Parallel()
		rec := &optsRecorder{}
		a := NewRetrieverAgent(rec, "")
		if _, err := a.Execute(context.Background(), Input{KbID: "kb", Query: "q"}); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if rec.got.ForceBM25SimpleArm {
			t.Fatal("ForceBM25SimpleArm must default to false")
		}
	})
}

// TestSpecialistsForwardRawQuery guards the rewrite ⊕ raw retrieval lane
// (Wave 1 Task 7): without the forwarding, a Supervisor turn silently
// loses the raw last-turn utterance the chat layer resolved (the standard
// PrepareChatContext path would still have it — the two paths would
// diverge). Mutation: drop the field from one specialist's SearchOptions
// literal — that specialist's subtest must fail.
func TestSpecialistsForwardRawQuery(t *testing.T) {
	t.Parallel()

	t.Run("retriever", func(t *testing.T) {
		t.Parallel()
		rec := &optsRecorder{}
		a := NewRetrieverAgent(rec, "")
		if _, err := a.Execute(context.Background(), Input{KbID: "kb", Query: "q", RawQuery: "und wann?"}); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if rec.got.RawQuery != "und wann?" {
			t.Fatalf("RetrieverAgent dropped Input.RawQuery, got %q", rec.got.RawQuery)
		}
	})

	t.Run("enumerator", func(t *testing.T) {
		t.Parallel()
		rec := &optsRecorder{}
		a := NewEnumeratorAgent(rec, "", func(_, _ string) bool { return true })
		if _, err := a.Execute(context.Background(), Input{KbID: "kb", Query: "q", RawQuery: "und wann?"}); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if rec.got.RawQuery != "und wann?" {
			t.Fatalf("EnumeratorAgent dropped Input.RawQuery, got %q", rec.got.RawQuery)
		}
	})

	t.Run("default stays empty", func(t *testing.T) {
		t.Parallel()
		rec := &optsRecorder{}
		a := NewRetrieverAgent(rec, "")
		if _, err := a.Execute(context.Background(), Input{KbID: "kb", Query: "q"}); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if rec.got.RawQuery != "" {
			t.Fatalf("RawQuery must default to empty, got %q", rec.got.RawQuery)
		}
	})
}
