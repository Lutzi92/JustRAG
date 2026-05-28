package ai

import (
	"context"
	"net/http"
	"testing"
)

// TestParseEvidentialityScores_ObjectForm: the prompt asks for
// {"scores": [...]} — the primary supported shape.
func TestParseEvidentialityScores_ObjectForm(t *testing.T) {
	t.Parallel()
	got, err := parseEvidentialityScores(`{"scores": [0.1, 0.9, 0.4]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []float64{0.1, 0.9, 0.4}
	if len(got) != len(want) {
		t.Fatalf("length: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: want %v, got %v", i, want[i], got[i])
		}
	}
}

// TestParseEvidentialityScores_BareArrayForm: defensive fallback
// — some fast-tier models drop the wrapper object even when the
// prompt asks for it.
func TestParseEvidentialityScores_BareArrayForm(t *testing.T) {
	t.Parallel()
	got, err := parseEvidentialityScores(`[0.2, 0.8]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != 0.2 || got[1] != 0.8 {
		t.Errorf("bare array parse failed: %v", got)
	}
}

// TestParseEvidentialityScores_ProseWrapping: mid-tier models
// frequently prepend an explanation. The parser must extract the
// JSON from the surrounding text rather than fail outright.
func TestParseEvidentialityScores_ProseWrapping(t *testing.T) {
	t.Parallel()
	got, err := parseEvidentialityScores(`Here is the scoring:
{"scores": [0.5, 0.7]}
That's my analysis.`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("prose-wrapped parse failed: %v", got)
	}
}

// TestParseEvidentialityScores_GarbageReturnsError: when neither
// shape can be recovered, the caller must see an error and the
// chat pipeline must fall through to keeping all chunks (the
// fail-open contract). Verified at the parser layer here; the
// caller-side keep behaviour is asserted in the chat-side tests.
func TestParseEvidentialityScores_GarbageReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := parseEvidentialityScores("nope nothing here"); err == nil {
		t.Error("garbage input: expected error, got nil")
	}
	if _, err := parseEvidentialityScores(""); err == nil {
		t.Error("empty input: expected error, got nil")
	}
}

// TestScoreEvidentiality_HappyPath: feeds the LLM a query + 3
// chunks; the fake server returns a valid scores object; we
// assert the map round-trips with correct keys and values.
func TestScoreEvidentiality_HappyPath(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(`{"scores":[0.9, 0.1, 0.5]}`, ""))
	})
	resolver := resolverForServer(srv, "gpt-4o")
	got, err := ScoreEvidentiality(
		context.Background(), resolver,
		"what is X?",
		[]string{"X is a thing.", "Unrelated context.", "X partially described."},
		"", "en", "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[int]float64{0: 0.9, 1: 0.1, 2: 0.5}
	if len(got) != len(want) {
		t.Fatalf("length: want %d, got %d", len(want), len(got))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("[%d]: want %v, got %v", k, v, got[k])
		}
	}
}

// TestScoreEvidentiality_ClampsOutOfRangeScores: a misbehaving
// classifier could emit 1.5 or -0.2. Both must be clamped to the
// [0, 1] band so the threshold comparison downstream stays
// well-defined.
func TestScoreEvidentiality_ClampsOutOfRangeScores(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(`{"scores":[1.5, -0.2, 0.5]}`, ""))
	})
	resolver := resolverForServer(srv, "gpt-4o")
	got, err := ScoreEvidentiality(
		context.Background(), resolver,
		"q",
		[]string{"a", "b", "c"},
		"", "en", "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0] != 1.0 {
		t.Errorf("[0] not clamped to 1: got %v", got[0])
	}
	if got[1] != 0.0 {
		t.Errorf("[1] not clamped to 0: got %v", got[1])
	}
	if got[2] != 0.5 {
		t.Errorf("[2] in-range value mutated: got %v", got[2])
	}
}

// TestScoreEvidentiality_DropsHallucinatedIndices: the parser
// could in principle return more scores than chunks (an
// over-confident model). The mapping must stop at the input
// boundary so a stray score can't tag a non-existent chunk.
func TestScoreEvidentiality_DropsHallucinatedIndices(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(`{"scores":[0.5, 0.5, 0.5, 0.5, 0.5]}`, ""))
	})
	resolver := resolverForServer(srv, "gpt-4o")
	got, err := ScoreEvidentiality(
		context.Background(), resolver,
		"q",
		[]string{"chunk-0", "chunk-1"}, // only 2 chunks
		"", "en", "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("score count: want 2, got %d (%v)", len(got), got)
	}
	for k := range got {
		if k >= 2 {
			t.Errorf("hallucinated index %d leaked into output", k)
		}
	}
}

// TestScoreEvidentiality_EmptyInputs: nil resolver, empty query,
// or empty chunks each short-circuit cleanly without invoking
// the LLM.
func TestScoreEvidentiality_EmptyInputs(t *testing.T) {
	t.Parallel()
	// nil resolver → error (programming bug, not data condition)
	if _, err := ScoreEvidentiality(context.Background(), nil, "q", []string{"c"}, "", "en", ""); err == nil {
		t.Error("nil resolver: expected error")
	}
	// empty query → no work + no error
	got, err := ScoreEvidentiality(context.Background(), &ConfigResolver{}, "", []string{"c"}, "", "en", "")
	if err != nil || got != nil {
		t.Errorf("empty query: want (nil, nil), got (%v, %v)", got, err)
	}
	// empty chunks → no work + no error
	got, err = ScoreEvidentiality(context.Background(), &ConfigResolver{}, "q", nil, "", "en", "")
	if err != nil || got != nil {
		t.Errorf("empty chunks: want (nil, nil), got (%v, %v)", got, err)
	}
}
