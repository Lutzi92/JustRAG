package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

func lcChunks(n int) []vector.SearchChunk {
	out := make([]vector.SearchChunk, n)
	for i := range n {
		out[i] = vector.SearchChunk{
			ID:       fmt.Sprintf("c%d", i+1),
			FileID:   fmt.Sprintf("f%d", i+1),
			FileName: fmt.Sprintf("doc%d.pdf", i+1),
			Content:  fmt.Sprintf("BODYPHRASE-%d raw chunk body number %d", i+1, i+1),
			Score:    1.0 - float64(i)/100.0,
		}
	}
	return out
}

func lcParams(mode string) LongContextParams {
	return LongContextParams{
		KbID:        "kb",
		Query:       "Fasse alle Befunde zusammen",
		Language:    "de",
		Mode:        mode,
		MaxTokens:   100_000,
		TopK:        200,
		GroupSize:   8,
		Concurrency: 6,
	}
}

// ---------------------------------------------------------------------------
// (a) groupChunks
// ---------------------------------------------------------------------------

func TestGroupChunksKeepsOrderAndShortTail(t *testing.T) {
	chunks := lcChunks(19)
	groups := groupChunks(chunks, 8)
	if len(groups) != 3 {
		t.Fatalf("len(groups) = %d, want 3", len(groups))
	}
	if len(groups[0]) != 8 || len(groups[1]) != 8 || len(groups[2]) != 3 {
		t.Fatalf("group sizes = %d/%d/%d, want 8/8/3", len(groups[0]), len(groups[1]), len(groups[2]))
	}
	// Order must be preserved so a group's chunk k maps to pool index
	// groupIndex*size+k — the [N] numbering depends on it.
	if groups[0][0].ID != "c1" || groups[1][0].ID != "c9" || groups[2][2].ID != "c19" {
		t.Fatalf("order broken: %q %q %q", groups[0][0].ID, groups[1][0].ID, groups[2][2].ID)
	}
}

func TestGroupChunksDegenerateSizes(t *testing.T) {
	if got := groupChunks(nil, 8); got != nil {
		t.Fatalf("nil chunks: want nil groups, got %#v", got)
	}
	// A non-positive size must not divide by zero / loop forever; it
	// normalises to one group per chunk-set default.
	groups := groupChunks(lcChunks(3), 0)
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("size 0: want one group of 3, got %d groups", len(groups))
	}
}

// ---------------------------------------------------------------------------
// (b) map_reduce assembly with an injected extractor
// ---------------------------------------------------------------------------

func TestConsumeLongContextMapReduceAssembly(t *testing.T) {
	chunks := lcChunks(4)
	extract := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _, _ string) ([]ai.LongContextFinding, error) {
		return []ai.LongContextFinding{
			{SourceIdx: 3, Claim: "Claim about three", Quote: "quote three"},
			{SourceIdx: 1, Claim: "Claim about one", Quote: "quote one"},
			{SourceIdx: 99, Claim: "out of range", Quote: ""},
			{SourceIdx: 0, Claim: "also out of range", Quote: ""},
		}, nil
	}
	p := lcParams(LongContextModeMapReduce)
	p.GroupSize = 8 // one group over the whole pool

	cc, err := consumeLongContextWith(context.Background(), nil, extract, p, chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Findings appear in ascending source order with their quotes.
	if !strings.Contains(cc.Context, "[1] Claim about one") {
		t.Fatalf("context missing [1] finding:\n%s", cc.Context)
	}
	if !strings.Contains(cc.Context, "[3] Claim about three") {
		t.Fatalf("context missing [3] finding:\n%s", cc.Context)
	}
	if !strings.Contains(cc.Context, "quote one") || !strings.Contains(cc.Context, "quote three") {
		t.Fatalf("context missing quotes:\n%s", cc.Context)
	}
	if strings.Contains(cc.Context, "out of range") {
		t.Fatalf("source_idx outside [1,len(pool)] must be dropped:\n%s", cc.Context)
	}
	if i1, i3 := strings.Index(cc.Context, "[1] Claim"), strings.Index(cc.Context, "[3] Claim"); i1 > i3 {
		t.Fatalf("findings not in ascending source order:\n%s", cc.Context)
	}

	// Sources keep buildChatSourcesAndContext's numbering over the full pool.
	wantSources, _ := buildChatSourcesAndContext(chunks)
	if len(cc.Sources) != len(wantSources) {
		t.Fatalf("len(Sources) = %d, want %d", len(cc.Sources), len(wantSources))
	}
	for i := range wantSources {
		if cc.Sources[i].Index != wantSources[i].Index || cc.Sources[i].ChunkID != wantSources[i].ChunkID {
			t.Fatalf("Sources[%d] = %+v, want %+v", i, cc.Sources[i], wantSources[i])
		}
	}

	// FinalChunks is the full pool (eval recall reads this).
	if len(cc.FinalChunks) != len(chunks) {
		t.Fatalf("len(FinalChunks) = %d, want %d", len(cc.FinalChunks), len(chunks))
	}

	// The synthesis instruction is present and raw chunk bodies are NOT.
	if !strings.Contains(cc.SystemPrompt, prompts.LongContextSynthesisSystem("de")) {
		t.Fatalf("system prompt missing synthesis instruction:\n%s", cc.SystemPrompt)
	}
	if strings.Contains(cc.SystemPrompt, "BODYPHRASE-2") {
		t.Fatalf("map_reduce system prompt must not carry raw chunk bodies:\n%s", cc.SystemPrompt)
	}
}

// ---------------------------------------------------------------------------
// (c) panic recovery — MUTATION: drop `defer safego.RecoverError(&res[i].err)`
// in consumeLongContextWith's map goroutine and this test panics the suite.
// ---------------------------------------------------------------------------

func TestConsumeLongContextMapPanicFallsBackToRawChunks(t *testing.T) {
	chunks := lcChunks(4)
	extract := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _, _ string) ([]ai.LongContextFinding, error) {
		panic("extractor exploded")
	}
	p := lcParams(LongContextModeMapReduce)
	p.GroupSize = 8

	cc, err := consumeLongContextWith(context.Background(), nil, extract, p, chunks)
	if err != nil {
		t.Fatalf("a panicking extractor must not fail the turn: %v", err)
	}
	// W3-R7: a failed group contributes its raw chunk text as fallback
	// findings so evidence is never silently dropped.
	for i := 1; i <= 4; i++ {
		if !strings.Contains(cc.Context, fmt.Sprintf("[%d] BODYPHRASE-%d", i, i)) {
			t.Fatalf("missing fallback finding for source %d:\n%s", i, cc.Context)
		}
	}
}

func TestConsumeLongContextMapErrorFallsBackToRawChunks(t *testing.T) {
	chunks := lcChunks(2)
	extract := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _, _ string) ([]ai.LongContextFinding, error) {
		return nil, errors.New("boom")
	}
	p := lcParams(LongContextModeMapReduce)
	cc, err := consumeLongContextWith(context.Background(), nil, extract, p, chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cc.Context, "[1] BODYPHRASE-1") || !strings.Contains(cc.Context, "[2] BODYPHRASE-2") {
		t.Fatalf("extractor error must degrade to raw fallback findings:\n%s", cc.Context)
	}
}

// ---------------------------------------------------------------------------
// (d) flat mode pins today's PrepareChatContext tail byte-for-byte
// ---------------------------------------------------------------------------

// wantFlatPrompt reproduces service.go's historical prompt tail literally.
// If assembleFlatLongContext ever drifts from it, this fails.
func wantFlatPrompt(chunks []vector.SearchChunk, kbSystemPrompt, lang, dateLine string, add flatAddenda) string {
	_, contextText := buildChatSourcesAndContext(chunks)
	var sb strings.Builder
	if kbSystemPrompt != "" {
		sb.WriteString(kbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPromptWithDate(lang, dateLine))
	switch {
	case add.Abstain:
		sb.WriteString(prompts.ChatAbstainNotice(lang))
	case IsLowConfidence(chunks):
		sb.WriteString(prompts.ChatLowConfidenceNotice(lang))
	}
	if add.Enumeration != "" {
		sb.WriteString(add.Enumeration)
	}
	if add.Recency != "" {
		sb.WriteString(add.Recency)
	}
	if add.Tabular != "" {
		sb.WriteString("\n\n")
		sb.WriteString(add.Tabular)
	}
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(contextText)
	return sb.String()
}

func TestAssembleFlatLongContextPinsServiceTail(t *testing.T) {
	chunks := lcChunks(3)
	p := lcParams(LongContextModeFlat)
	p.KbSystemPrompt = "KB PROMPT"
	p.CurrentDateLine = "Heute ist der 6. September 2026."

	cases := []struct {
		name string
		add  flatAddenda
	}{
		{"no addenda", flatAddenda{}},
		{"abstain", flatAddenda{Abstain: true}},
		{"enumeration", flatAddenda{Enumeration: "\n\nENUM ADDENDUM"}},
		{"recency", flatAddenda{Recency: "\n\nRECENCY ADDENDUM"}},
		{"tabular", flatAddenda{Tabular: "TABULAR ADDENDUM"}},
		{"all", flatAddenda{Abstain: true, Enumeration: "\n\nE", Recency: "\n\nR", Tabular: "T"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assembleFlatLongContext(chunks, p, tc.add)
			want := wantFlatPrompt(chunks, p.KbSystemPrompt, p.Language, p.CurrentDateLine, tc.add)
			if got.SystemPrompt != want {
				t.Fatalf("flat system prompt drifted from the service.go tail.\n got: %q\nwant: %q", got.SystemPrompt, want)
			}
			if got.Abstain != tc.add.Abstain {
				t.Fatalf("Abstain = %v, want %v", got.Abstain, tc.add.Abstain)
			}
		})
	}
}

func TestConsumeLongContextFlatKeepsRawBodies(t *testing.T) {
	chunks := lcChunks(3)
	p := lcParams(LongContextModeFlat)

	cc, err := consumeLongContextWith(context.Background(), nil, nil, p, chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cc.Context, "BODYPHRASE-2") {
		t.Fatalf("flat mode must carry raw chunk bodies:\n%s", cc.Context)
	}
	if !strings.Contains(cc.SystemPrompt, "BODYPHRASE-2") {
		t.Fatalf("flat mode system prompt must carry the CONTEXT block:\n%s", cc.SystemPrompt)
	}
	if len(cc.FinalChunks) != 3 {
		t.Fatalf("len(FinalChunks) = %d, want 3", len(cc.FinalChunks))
	}
}

func TestConsumeLongContextUnknownModeFallsBackToFlat(t *testing.T) {
	p := lcParams("wat")
	cc, err := consumeLongContextWith(context.Background(), nil, nil, p, lcChunks(2))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cc.Context, "BODYPHRASE-1") {
		t.Fatalf("unknown mode must degrade to flat:\n%s", cc.Context)
	}
}

// ---------------------------------------------------------------------------
// (e) concurrency bound
// ---------------------------------------------------------------------------

func TestConsumeLongContextMapRespectsConcurrency(t *testing.T) {
	chunks := lcChunks(12) // 12 chunks / GroupSize 1 = 12 groups
	var inFlight, peak atomic.Int64
	release := make(chan struct{})
	var once sync.Once

	extract := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _, _ string) ([]ai.LongContextFinding, error) {
		cur := inFlight.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		// Hold every goroutine until the test releases them, so the peak
		// reflects the semaphore rather than scheduling luck.
		once.Do(func() {
			go func() {
				time.Sleep(50 * time.Millisecond)
				close(release)
			}()
		})
		<-release
		inFlight.Add(-1)
		return nil, nil
	}

	p := lcParams(LongContextModeMapReduce)
	p.GroupSize = 1
	p.Concurrency = 2

	if _, err := consumeLongContextWith(context.Background(), nil, extract, p, chunks); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("peak in-flight extractions = %d, want <= 2 (Concurrency)", got)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("peak in-flight extractions = %d, want the semaphore to allow 2", got)
	}
}

func TestConsumeLongContextEmptyPoolErrors(t *testing.T) {
	if _, err := consumeLongContextWith(context.Background(), nil, nil, lcParams(LongContextModeFlat), nil); err == nil {
		t.Fatalf("an empty chunk pool must error so the dispatcher falls through")
	}
}
