package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// conflictSources builds a numbered source list spanning n distinct files.
func conflictSources(n int) []ChatSource {
	out := make([]ChatSource, n)
	for i := range out {
		out[i] = ChatSource{
			Index:    i + 1,
			FileID:   "f" + string(rune('a'+i)),
			FileName: string(rune('a'+i)) + ".md",
			Content:  "body " + string(rune('a'+i)),
			Score:    1 - float64(i)/100,
		}
	}
	return out
}

func onConfig() ConflictConfig {
	return ConflictConfig{Enabled: true, MaxChunks: 12, Timeout: 2 * time.Second}
}

// nolint:unparam // the fn shape is dictated by detectSourceConflictsFn
func stubDetect(calls *int, findings *ai.ConflictFindings, err error) detectSourceConflictsFn {
	return func(_ context.Context, _ *ai.ConfigResolver, _, _ string, _ []ai.ConflictSource, _, _ string) (*ai.ConflictFindings, error) {
		*calls++
		return findings, err
	}
}

func TestDetectConflicts_DisabledMakesNoCall(t *testing.T) {
	calls := 0
	got := detectConflictsWith(context.Background(),
		stubDetect(&calls, &ai.ConflictFindings{}, nil), nil,
		ConflictInput{Sources: conflictSources(3), Config: ConflictConfig{Enabled: false}})
	if got != nil {
		t.Errorf("report: got %+v, want nil", got)
	}
	if calls != 0 {
		t.Errorf("LLM calls: got %d, want 0 when the flag is off", calls)
	}
}

// The ≥ 2-distinct-files gate. Chunks from ONE file are a document talking
// to itself; calling the detector for them buys nothing and costs a
// fast-tier call on the most common shape of small-KB turn.
func TestDetectConflicts_SingleFileMakesNoCall(t *testing.T) {
	sources := []ChatSource{
		{Index: 1, FileID: "f1", FileName: "only.md", Content: "a", Score: 0.9},
		{Index: 2, FileID: "f1", FileName: "only.md", Content: "b", Score: 0.8},
		{Index: 3, FileID: "", FileName: "no-id.md", Content: "c", Score: 0.7},
	}
	calls := 0
	got := detectConflictsWith(context.Background(),
		stubDetect(&calls, &ai.ConflictFindings{}, nil), nil,
		ConflictInput{Sources: sources, Config: onConfig()})
	if got != nil {
		t.Errorf("report: got %+v, want nil", got)
	}
	if calls != 0 {
		t.Errorf("LLM calls: got %d, want 0 for a single-file set", calls)
	}
}

// The gate must short-circuit BEFORE any downstream work — no date lookup,
// no LLM call — with MaxChunks ≥ len(sources), i.e. where the cap itself
// changes nothing. Pins that the single distinct-files check is placed
// ahead of conflictSourceDates and not merely ahead of the model call.
func TestDetectConflicts_GateShortCircuitsBeforeDateLookup(t *testing.T) {
	sources := []ChatSource{
		{Index: 1, FileID: "f1", FileName: "only.md", Content: "a", Score: 0.9},
		{Index: 2, FileID: "f1", FileName: "only.md", Content: "b", Score: 0.8},
	}
	lookup := &fakeFileDates{rows: map[string]FileDates{}}
	calls := 0
	got := detectConflictsWith(context.Background(),
		stubDetect(&calls, &ai.ConflictFindings{}, nil), nil,
		ConflictInput{
			Sources:   sources,
			Config:    ConflictConfig{Enabled: true, MaxChunks: 30, Timeout: time.Second},
			FileDates: lookup,
		})
	if got != nil || calls != 0 {
		t.Errorf("report=%+v calls=%d; want nil/0", got, calls)
	}
	if lookup.calls != 0 {
		t.Errorf("date lookup calls: got %d, want 0 — the gate must return before any downstream work", lookup.calls)
	}
}

// The cap can turn a multi-file set into a single-file one, and it is the
// capped list the detector actually sees.
func TestDetectConflicts_CapCollapsingToOneFileMakesNoCall(t *testing.T) {
	sources := []ChatSource{
		{Index: 1, FileID: "f1", FileName: "same.md", Content: "a", Score: 0.99},
		{Index: 2, FileID: "f1", FileName: "same.md", Content: "b", Score: 0.98},
		{Index: 3, FileID: "f2", FileName: "other.md", Content: "c", Score: 0.10},
	}
	calls := 0
	got := detectConflictsWith(context.Background(),
		stubDetect(&calls, &ai.ConflictFindings{}, nil), nil,
		ConflictInput{Sources: sources, Config: ConflictConfig{Enabled: true, MaxChunks: 2, Timeout: time.Second}})
	if got != nil || calls != 0 {
		t.Errorf("report=%+v calls=%d; want nil/0 when the cap leaves one file", got, calls)
	}
}

func TestDetectConflicts_ResolvesFileNamesAndEmitsTrajectory(t *testing.T) {
	calls := 0
	var events []map[string]any
	got := detectConflictsWith(context.Background(),
		stubDetect(&calls, &ai.ConflictFindings{Conflicts: []ai.Conflict{
			{Claim: "Beitragshöhe", SourceA: 1, SourceB: 3, Kind: "superseded", Newer: "b"},
			{Claim: "ghost", SourceA: 1, SourceB: 99, Kind: "contradiction", Newer: "unknown"},
		}}, nil), nil,
		ConflictInput{
			Sources: conflictSources(3),
			Config:  onConfig(),
			Emit:    func(e map[string]any) { events = append(events, e) },
		})
	if calls != 1 {
		t.Fatalf("LLM calls: got %d, want 1", calls)
	}
	if got == nil || len(got.Conflicts) != 1 {
		t.Fatalf("report: got %+v, want exactly one conflict", got)
	}
	c := got.Conflicts[0]
	if c.FileA != "a.md" || c.FileB != "c.md" {
		t.Errorf("file names: got %q/%q, want a.md/c.md", c.FileA, c.FileB)
	}
	if !hasTrajectoryStage(events, "conflict_surfacing", "found") {
		t.Errorf("missing conflict_surfacing{found} trajectory event: %+v", events)
	}
}

func TestDetectConflicts_TimeoutIsFailSoft(t *testing.T) {
	var events []map[string]any
	detect := func(ctx context.Context, _ *ai.ConfigResolver, _, _ string, _ []ai.ConflictSource, _, _ string) (*ai.ConflictFindings, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	got := detectConflictsWith(context.Background(), detect, nil, ConflictInput{
		Sources: conflictSources(3),
		Config:  ConflictConfig{Enabled: true, MaxChunks: 12, Timeout: 30 * time.Millisecond},
		Emit:    func(e map[string]any) { events = append(events, e) },
	})
	if got != nil {
		t.Errorf("report: got %+v, want nil on timeout", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the answer waited %v on a timing-out detector", elapsed)
	}
	if !hasTrajectoryStage(events, "conflict_surfacing", "timeout") {
		t.Errorf("missing conflict_surfacing{timeout} trajectory event: %+v", events)
	}
}

func TestDetectConflicts_ErrorIsFailSoft(t *testing.T) {
	var events []map[string]any
	calls := 0
	got := detectConflictsWith(context.Background(),
		stubDetect(&calls, nil, errors.New("boom")), nil,
		ConflictInput{
			Sources: conflictSources(3),
			Config:  onConfig(),
			Emit:    func(e map[string]any) { events = append(events, e) },
		})
	if got != nil {
		t.Errorf("report: got %+v, want nil on error", got)
	}
	if !hasTrajectoryStage(events, "conflict_surfacing", "error") {
		t.Errorf("missing conflict_surfacing{error} trajectory event: %+v", events)
	}
}

// The cap picks by score but presents in citation order, so the numbering
// the model reads matches the numbering the answer prompt uses.
func TestPickConflictSources_CapsByScoreKeepsCitationOrder(t *testing.T) {
	sources := []ChatSource{
		{Index: 1, FileID: "f1", Score: 0.1},
		{Index: 2, FileID: "f2", Score: 0.9},
		{Index: 3, FileID: "f3", Score: 0.5},
		{Index: 4, FileID: "f4", Score: 0.8},
	}
	got := pickConflictSources(sources, 2)
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	if got[0].Index != 2 || got[1].Index != 4 {
		t.Errorf("picked %d,%d; want the two top-scoring sources 2,4 in citation order", got[0].Index, got[1].Index)
	}
}

func TestRenderConflictDateLine(t *testing.T) {
	published := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	if got := renderConflictDateLine("de", FileDates{CreatedAt: created, PublishedAt: &published}); got != "2026-05-09" {
		t.Errorf("published_at should win: got %q", got)
	}
	if got := renderConflictDateLine("de", FileDates{CreatedAt: created}); got != "2026-01-02" {
		t.Errorf("created_at fallback: got %q", got)
	}
	if got := renderConflictDateLine("de", FileDates{}); got != "unbekannt" {
		t.Errorf("de unknown: got %q", got)
	}
	if got := renderConflictDateLine("en", FileDates{}); got != "unknown" {
		t.Errorf("en unknown: got %q", got)
	}
}

func TestConflictAddendumText(t *testing.T) {
	if got := ConflictAddendumText("de", nil); got != "" {
		t.Errorf("nil report: got %q, want empty", got)
	}
	if got := ConflictAddendumText("de", &ConflictReport{}); got != "" {
		t.Errorf("empty report: got %q, want empty", got)
	}

	report := &ConflictReport{Conflicts: []MessageConflict{
		{Claim: "Beitragshöhe", SourceA: 1, SourceB: 3, Kind: "superseded", Newer: "b", FileA: "alt.md", FileB: "neu.md"},
	}}
	de := ConflictAddendumText("de", report)
	for _, want := range []string{"alt.md", "neu.md", "[1]", "[3]", "neuer ist [3] neu.md", "Beitragshöhe"} {
		if !strings.Contains(de, want) {
			t.Errorf("de addendum missing %q:\n%s", want, de)
		}
	}
	en := ConflictAddendumText("en", report)
	for _, want := range []string{"alt.md", "neu.md", "the newer one is [3] neu.md", "state the disagreement"} {
		if !strings.Contains(strings.ToLower(en), strings.ToLower(want)) {
			t.Errorf("en addendum missing %q:\n%s", want, en)
		}
	}
}

// The addendum must reach the answer system prompt through the SAME flat
// assembler both the standard path and OrchLongContext flat mode use.
func TestAssembleFlatFromParts_CarriesConflictAddendum(t *testing.T) {
	chunks := []vector.SearchChunk{{ID: "1", FileID: "f1", FileName: "a.md", Content: "x", Score: 0.9}}
	sources, contextText := buildChatSourcesAndContext(chunks)
	out := assembleFlatFromParts(chunks, sources, contextText,
		LongContextParams{Language: "de"},
		flatAddenda{Conflicts: "\n\nWIDERSPRÜCHLICHE QUELLEN: marker"})
	if !strings.Contains(out.SystemPrompt, "WIDERSPRÜCHLICHE QUELLEN: marker") {
		t.Fatalf("system prompt is missing the conflict addendum:\n%s", out.SystemPrompt)
	}
	if idx, ctxIdx := strings.Index(out.SystemPrompt, "WIDERSPRÜCHLICHE"), strings.Index(out.SystemPrompt, "\n\nCONTEXT:\n"); idx > ctxIdx {
		t.Errorf("conflict addendum must precede the CONTEXT block (at %d vs %d)", idx, ctxIdx)
	}
}

func TestConflictsForWire(t *testing.T) {
	if got := ConflictsForWire(nil); got != nil {
		t.Errorf("nil report: got %+v, want nil", got)
	}
	if got := ConflictsForWire(&ConflictReport{}); got != nil {
		t.Errorf("empty report: got %+v, want nil", got)
	}
	r := &ConflictReport{Conflicts: []MessageConflict{{Claim: "c"}}}
	if got := ConflictsForWire(r); len(got) != 1 {
		t.Errorf("populated report: got %+v, want 1 entry", got)
	}
}

// hasTrajectoryStage reports whether any emitted event is the unified
// agentTrajectory envelope for the given stage + reason.
func hasTrajectoryStage(events []map[string]any, stage, reason string) bool {
	for _, e := range events {
		evt, ok := e["agentTrajectory"].(TrajectoryEvent)
		if !ok {
			continue
		}
		if evt.Stage == stage && evt.Reason == reason {
			return true
		}
	}
	return false
}
