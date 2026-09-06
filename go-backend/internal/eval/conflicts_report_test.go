package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

// --- conflictTracer integration --------------------------------------------

type conflictSearcherStub struct {
	results   map[string][]RetrievedChunk
	conflicts map[string][]chat.MessageConflict
}

func (s *conflictSearcherStub) Search(_ context.Context, q Question, _ int) ([]RetrievedChunk, error) {
	return s.results[q.ID], nil
}

func (s *conflictSearcherStub) ConflictsForQuestion(id string) []chat.MessageConflict {
	return s.conflicts[id]
}

func TestRunEval_AttachesConflicts(t *testing.T) {
	stub := &conflictSearcherStub{
		results: map[string][]RetrievedChunk{
			"q1": {{FileID: "f1", Score: 0.9}},
			"q2": {{FileID: "f2", Score: 0.7}},
		},
		conflicts: map[string][]chat.MessageConflict{
			"q1": {{
				Claim: "Patch-Status", SourceA: 1, SourceB: 2,
				Kind: "superseded", Newer: "b",
				FileA: "NEU WID-SEC-2026-0104", FileB: "UPDATE WID-SEC-2026-0104",
			}},
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
	if len(rep.Questions[0].Conflicts) != 1 {
		t.Fatalf("q1 conflicts not attached: %+v", rep.Questions[0].Conflicts)
	}
	if got := rep.Questions[0].Conflicts[0]; got.Kind != "superseded" || got.Newer != "b" || got.FileB != "UPDATE WID-SEC-2026-0104" {
		t.Fatalf("q1 conflict entry = %+v", got)
	}
	if rep.Questions[1].Conflicts != nil {
		t.Fatalf("q2 must carry no conflicts; got %+v", rep.Questions[1].Conflicts)
	}
}

func TestRunEval_LegacySearcherLeavesConflictsNil(t *testing.T) {
	// fixedSearcher does NOT implement conflictTracer.
	s := &fixedSearcher{chunks: []RetrievedChunk{{FileID: "f1", Score: 0.9}}}
	questions := []Question{{ID: "q1", Question: "?", KbID: "kb", MustCiteFileIDs: []string{"f1"}}}
	rep, err := RunEval(context.Background(), s, questions, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Questions[0].Conflicts != nil {
		t.Fatalf("Conflicts must stay nil for a legacy adapter; got %+v", rep.Questions[0].Conflicts)
	}
}

// --- report shape ----------------------------------------------------------

// The on-disk report must stay byte-stable for every run that surfaces no
// conflicts: the key is omitted entirely rather than serialised as null or
// [].
func TestQuestionReport_ConflictsOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(QuestionReport{Question: Question{ID: "q1"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "conflicts") {
		t.Fatalf("empty Conflicts must not serialise a key; got %s", b)
	}
	b, err = json.Marshal(QuestionReport{
		Question:  Question{ID: "q1"},
		Conflicts: []chat.MessageConflict{{Claim: "x", SourceA: 1, SourceB: 2, Kind: "contradiction", Newer: "unknown"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"conflicts":[{"claim":"x"`) {
		t.Fatalf("Conflicts must serialise as the bare wire array; got %s", b)
	}
}

// --- ConflictCounts --------------------------------------------------------

func TestConflictCounts(t *testing.T) {
	qs := []QuestionReport{
		{}, // no conflicts
		{Conflicts: []chat.MessageConflict{{Kind: "contradiction", Newer: "unknown"}}},
		{Conflicts: []chat.MessageConflict{{Kind: "superseded", Newer: "unknown"}}},
		{Conflicts: []chat.MessageConflict{
			{Kind: "contradiction", Newer: "unknown"},
			{Kind: "superseded", Newer: "a"},
		}},
		{Conflicts: []chat.MessageConflict{
			{Kind: "superseded", Newer: "b"},
			{Kind: "superseded", Newer: "a"},
		}},
		{Conflicts: []chat.MessageConflict{{Kind: "superseded"}}}, // empty newer counts as unknown
	}
	flagged, withNewer := ConflictCounts(qs)
	if flagged != 5 {
		t.Fatalf("flagged = %d, want 5 (every question with >= 1 entry)", flagged)
	}
	// Counted once per QUESTION, not per entry: the two-superseded row
	// contributes 1, not 2.
	if withNewer != 2 {
		t.Fatalf("withSupersededNewer = %d, want 2", withNewer)
	}
}

func TestWriteHumanSummary_ConflictSection(t *testing.T) {
	rep := Report{
		Questions: []QuestionReport{
			{Conflicts: []chat.MessageConflict{{Kind: "superseded", Newer: "b"}}},
			{},
			{},
			{},
		},
	}
	var buf bytes.Buffer
	if err := WriteHumanSummary(&buf, rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "questions_with_conflict     = 1/4 (0.250)") {
		t.Fatalf("conflict section missing or wrong:\n%s", out)
	}
	if !strings.Contains(out, "with_superseded_newer_known = 1") {
		t.Fatalf("newer-known line missing:\n%s", out)
	}

	// A run that surfaced nothing keeps the pre-Wave-5 text exactly.
	buf.Reset()
	if err := WriteHumanSummary(&buf, Report{Questions: []QuestionReport{{}, {}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Conflict surfacing") {
		t.Fatalf("no-conflict run must print no conflict section:\n%s", buf.String())
	}
}
