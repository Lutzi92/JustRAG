package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/splitter"
)

func libCfg(fulltext, longctx string) *fakeSiteConfigReader {
	v := map[string]*string{}
	if fulltext != "" {
		v["chat_library_fulltext_max_tokens"] = strPtr(fulltext)
	}
	if longctx != "" {
		v["chat_longcontext_max_tokens"] = strPtr(longctx)
	}
	return &fakeSiteConfigReader{values: v}
}

func twoFiles() []LibraryFile {
	return []LibraryFile{
		{UserFileID: "uf-1", Name: "a.pdf", Parsed: &parser.ParseResult{
			Text:  "alpha one alpha two",
			Pages: []parser.PageText{{PageNumber: 1, Text: "alpha one"}, {PageNumber: 2, Text: "alpha two"}},
		}},
		{UserFileID: "uf-2", Name: "b.txt", Parsed: &parser.ParseResult{Text: "beta text"}},
	}
}

func TestBuildLibraryContext_FullText(t *testing.T) {
	cc, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("", ""),
		LibraryContextParams{Files: twoFiles(), Query: "q", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Sources) != 3 {
		t.Fatalf("sources = %d, want 3", len(cc.Sources))
	}
	wantIDs := []string{"uf-1", "uf-1", "uf-2"}
	for i, s := range cc.Sources {
		if s.Index != i+1 || s.UserFileID != wantIDs[i] || s.FileID != "" {
			t.Errorf("source %d = %+v", i, s)
		}
	}
	if len(cc.Sources[0].Pages) != 1 || cc.Sources[0].Pages[0] != 1 || cc.Sources[1].Pages[0] != 2 {
		t.Errorf("pages: %v %v", cc.Sources[0].Pages, cc.Sources[1].Pages)
	}
	for _, want := range []string{"[1] [Source: a.pdf, p. 1]", "[2] [Source: a.pdf, p. 2]", "[3] [Source: b.txt", "alpha two", "beta text", "CONTEXT:"} {
		if !strings.Contains(cc.SystemPrompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	for _, c := range cc.FinalChunks {
		if c.FileID != "" || c.ID != "" {
			t.Errorf("chunk leaks ids: %+v", c)
		}
	}
	b, _ := json.Marshal(cc.Sources[0])
	if !strings.Contains(string(b), `"userFileId":"uf-1"`) {
		t.Errorf("json: %s", b)
	}
}

func bigText(words int) string { return strings.Repeat("lorem ipsum dolor ", words) }

func TestBuildLibraryContext_MapReduce(t *testing.T) {
	files := []LibraryFile{{UserFileID: "uf-1", Name: "big.txt", Parsed: &parser.ParseResult{Text: bigText(1500)}}}
	var calls atomic.Int32
	extract := func(_ context.Context, _ *ai.ConfigResolver, _, _, kbID, _, _ string) ([]ai.LongContextFinding, error) {
		calls.Add(1)
		if kbID != "" {
			t.Errorf("kbID = %q", kbID)
		}
		return []ai.LongContextFinding{{SourceIdx: 1, Claim: "FINDING-CLAIM", Quote: "lorem"}}, nil
	}
	cc, err := buildLibraryContextWith(context.Background(), nil, extract, libCfg("4000", "100000"),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 {
		t.Fatal("extractor not called: map_reduce path not taken")
	}
	if !strings.Contains(cc.SystemPrompt, "FINDING-CLAIM") || !strings.Contains(cc.SystemPrompt, "FINDINGS") {
		t.Errorf("findings missing from prompt")
	}
	if len(cc.Sources) < 2 {
		t.Fatalf("want split chunks as sources, got %d", len(cc.Sources))
	}
	for _, s := range cc.Sources {
		if s.UserFileID != "uf-1" || s.FileID != "" {
			t.Errorf("source = %+v", s)
		}
	}
}

func TestBuildLibraryContext_TooLarge(t *testing.T) {
	files := []LibraryFile{{UserFileID: "uf-1", Name: "big.txt", Parsed: &parser.ParseResult{Text: bigText(4000)}}}
	_, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("4000", "10000"),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	var tl *ErrLibraryTooLarge
	if !errors.As(err, &tl) {
		t.Fatalf("err = %v", err)
	}
	if tl.Max != 10000 || tl.Tokens <= 10000 {
		t.Errorf("tl = %+v", tl)
	}
	want := "selected files are too large for one chat turn ("
	if !strings.HasPrefix(tl.Error(), want) || !strings.HasSuffix(tl.Error(), ", maximum 10000)") {
		t.Errorf("msg = %q", tl.Error())
	}
}

func TestBuildLibraryContext_SplitsOversizedPage(t *testing.T) {
	files := []LibraryFile{{UserFileID: "uf-1", Name: "p.pdf", Parsed: &parser.ParseResult{
		Pages: []parser.PageText{{PageNumber: 7, Text: bigText(1500)}},
	}}}
	cc, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("100000", ""),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Sources) < 2 {
		t.Fatalf("sources = %d, want split", len(cc.Sources))
	}
	for _, s := range cc.Sources {
		if len(s.Pages) != 1 || s.Pages[0] != 7 {
			t.Errorf("pages = %v", s.Pages)
		}
	}
}

func TestBuildLibraryContext_Empty(t *testing.T) {
	_, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("", ""),
		LibraryContextParams{Files: []LibraryFile{{UserFileID: "x", Name: "e", Parsed: &parser.ParseResult{}}}})
	if err == nil {
		t.Fatal("want error for no text")
	}
}

func TestBuildLibraryContext_OwnerSurvivesMapEmptyReorder(t *testing.T) {
	files := []LibraryFile{
		{UserFileID: "uf-A", Name: "a.txt", Parsed: &parser.ParseResult{Text: bigText(800)}},
		{UserFileID: "uf-B", Name: "b.txt", Parsed: &parser.ParseResult{Text: bigText(800)}},
		{UserFileID: "uf-C", Name: "c.txt", Parsed: &parser.ParseResult{Text: bigText(800)}},
	}
	owner := map[string]string{"a.txt": "uf-A", "b.txt": "uf-B", "c.txt": "uf-C"}
	empty := func(context.Context, *ai.ConfigResolver, string, string, string, string, string) ([]ai.LongContextFinding, error) {
		return nil, nil
	}
	check := func(name string, cc *ChatContext, err error) {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(cc.Sources) < 3 {
			t.Fatalf("%s: sources = %d", name, len(cc.Sources))
		}
		for _, s := range cc.Sources {
			if s.UserFileID != owner[s.FileName] || s.FileID != "" {
				t.Errorf("%s: source %+v mismatched owner", name, s)
			}
		}
	}
	cc, err := buildLibraryContextWith(context.Background(), nil, empty, libCfg("4000", "100000"),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	check("map_empty", cc, err)
	cc, err = buildLibraryContextWith(context.Background(), nil, nil, libCfg("100000", ""),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	check("fulltext", cc, err)
	ext := func(context.Context, *ai.ConfigResolver, string, string, string, string, string) ([]ai.LongContextFinding, error) {
		return []ai.LongContextFinding{{SourceIdx: 1, Claim: "c", Quote: "q"}}, nil
	}
	cc, err = buildLibraryContextWith(context.Background(), nil, ext, libCfg("4000", "100000"),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	check("map_reduce", cc, err)
}

func TestBuildLibraryContext_NoTextSentinelAndSkippedEvent(t *testing.T) {
	_, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("", ""),
		LibraryContextParams{Files: []LibraryFile{{UserFileID: "x", Name: "e", Parsed: &parser.ParseResult{}}}})
	if !errors.Is(err, ErrLibraryNoText) {
		t.Fatalf("err = %v", err)
	}
	var got []map[string]any
	files := append(twoFiles(), LibraryFile{UserFileID: "uf-3", Name: "empty.txt", Parsed: &parser.ParseResult{}})
	if _, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("", ""),
		LibraryContextParams{Files: files, Language: "en", Emit: func(m map[string]any) { got = append(got, m) }}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["stage"] != "library_files_skipped" {
		t.Errorf("events = %v", got)
	}
}

func TestChatLibraryFulltextMaxTokens(t *testing.T) {
	ctx := context.Background()
	cases := map[string]int{"": 60000, "abc": 60000, "3999": 60000, "4000": 4000, " 200000 ": 200000, "200001": 60000}
	for in, want := range cases {
		r := &fakeSiteConfigReader{values: map[string]*string{"chat_library_fulltext_max_tokens": strPtr(in)}}
		if got := ChatLibraryFulltextMaxTokens(ctx, r); got != want {
			t.Errorf("%q = %d, want %d", in, got, want)
		}
	}
	if got := ChatLibraryFulltextMaxTokens(ctx, nil); got != 60000 {
		t.Errorf("nil = %d", got)
	}
}

func TestBuildLibraryContext_TooLargePreCheckSkipsTokenizer(t *testing.T) {
	oldCount, oldSplit := libraryCountTokens, librarySplit
	t.Cleanup(func() { libraryCountTokens, librarySplit = oldCount, oldSplit })
	var calls atomic.Int32
	libraryCountTokens = func(s string) int { calls.Add(1); return oldCount(s) }
	librarySplit = func(s string, c splitter.Config) []string { calls.Add(1); return oldSplit(s, c) }

	files := []LibraryFile{{UserFileID: "uf-1", Name: "big.txt", Parsed: &parser.ParseResult{Text: bigText(4000)}}}
	_, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("4000", "10000"),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	var tl *ErrLibraryTooLarge
	if !errors.As(err, &tl) || !tl.AtLeast || tl.Tokens != 12000 {
		t.Fatalf("err = %v (%+v)", err, tl)
	}
	if !strings.Contains(tl.Error(), "(at least 12000 tokens, maximum 10000)") {
		t.Errorf("msg = %q", tl.Error())
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("pre-check rejection tokenized/split %d times, want 0", n)
	}
}

func TestBuildLibraryContext_TooLargeExactPathBelowBound(t *testing.T) {
	// Long compound words: one letter run each (bound 3000 <= 10000) but many
	// BPE tokens each, so only the exact count rejects.
	text := strings.Repeat("Donaudampfschifffahrtsgesellschaftskapitaensmuetze ", 3000)
	if b := textTokenLowerBound(text); b != 3000 {
		t.Fatalf("bound = %d", b)
	}
	files := []LibraryFile{{UserFileID: "uf-1", Name: "w.txt", Parsed: &parser.ParseResult{Text: text}}}
	_, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("4000", "10000"),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	var tl *ErrLibraryTooLarge
	if !errors.As(err, &tl) || tl.AtLeast || tl.Tokens <= 10000 {
		t.Fatalf("err = %v (%+v)", err, tl)
	}
}

// With the full-text budget above the long-context one there is no
// map_reduce tier, and the pre-check must not reject what full text accepts.
func TestBuildLibraryContext_FulltextBudgetAboveLongContext(t *testing.T) {
	files := []LibraryFile{{UserFileID: "uf-1", Name: "big.txt", Parsed: &parser.ParseResult{Text: bigText(4000)}}}
	if b := libraryTokenLowerBound(files); b <= 10000 {
		t.Fatalf("fixture bound %d must exceed the long-context budget", b)
	}
	cc, err := buildLibraryContextWith(context.Background(), nil, nil, libCfg("200000", "10000"),
		LibraryContextParams{Files: files, Query: "q", Language: "en"})
	if err != nil {
		t.Fatalf("full text must accept it: %v", err)
	}
	if !strings.Contains(cc.SystemPrompt, "lorem ipsum") {
		t.Fatal("not the full-text tier")
	}
	huge := []LibraryFile{{UserFileID: "uf-1", Name: "huge.txt", Parsed: &parser.ParseResult{Text: bigText(70000)}}}
	_, err = buildLibraryContextWith(context.Background(), nil, nil, libCfg("200000", "10000"),
		LibraryContextParams{Files: huge, Query: "q", Language: "en"})
	var tl *ErrLibraryTooLarge
	if !errors.As(err, &tl) || tl.Max != 200000 {
		t.Fatalf("err = %v (%+v), want too large against the 200000 limit", err, tl)
	}
}

func TestTextTokenLowerBound_NeverExceedsCL100K(t *testing.T) {
	if !libraryBoundSound() {
		t.Fatal("cl100k tokenizer unavailable: the pre-check would be disabled")
	}
	cases := []string{
		"", "   ", "a", "Hallo Welt!", "it's we're they'll I'd", "abc123def4567",
		"1234567890 12 3", "Straße Größe Übermaß", "été café",
		"日本語のテキスト、漢字かな交じり。", "x=1;y=22;z=333", "----====>>>> ....",
		"\n\n\t  word\r\nword  \n", "ÄÖÜäöüß 1.000,50 € (Stand: 2026-10-08)",
		"https://example.org/path?q=1&r=two#frag", "<|endoftext|> special",
		strings.Repeat("lorem ipsum dolor ", 50), "Ⅻ ½ ²³ ٣٤٥", "a1b2c3 _x_ y'z",
	}
	for _, c := range cases {
		if b, n := textTokenLowerBound(c), splitter.CountTokens(c); b > n {
			t.Errorf("bound %d > cl100k %d for %q", b, n, c)
		}
	}
}

func TestBuildLibraryContext_TurnMetricPerMode(t *testing.T) {
	c := observability.LibraryChatTurnTotalForTest()
	delta := func(mode string, f func()) float64 {
		before := testutil.ToFloat64(c.WithLabelValues(mode))
		f()
		return testutil.ToFloat64(c.WithLabelValues(mode)) - before
	}
	big := []LibraryFile{{UserFileID: "uf-1", Name: "big.txt", Parsed: &parser.ParseResult{Text: bigText(1500)}}}
	extract := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _, _ string) ([]ai.LongContextFinding, error) {
		return []ai.LongContextFinding{{SourceIdx: 1, Claim: "c", Quote: "lorem"}}, nil
	}
	if d := delta("fulltext", func() {
		_, _ = buildLibraryContextWith(context.Background(), nil, nil, libCfg("", ""),
			LibraryContextParams{Files: twoFiles(), Query: "q", Language: "en"})
	}); d != 1 {
		t.Errorf("fulltext delta = %v", d)
	}
	if d := delta("map_reduce", func() {
		_, _ = buildLibraryContextWith(context.Background(), nil, extract, libCfg("4000", "100000"),
			LibraryContextParams{Files: big, Query: "q", Language: "en"})
	}); d != 1 {
		t.Errorf("map_reduce delta = %v", d)
	}
	if d := delta("too_large", func() {
		_, _ = buildLibraryContextWith(context.Background(), nil, nil, libCfg("4000", "10000"),
			LibraryContextParams{Files: []LibraryFile{{UserFileID: "uf-1", Name: "b.txt", Parsed: &parser.ParseResult{Text: bigText(4000)}}}, Query: "q", Language: "en"})
	}); d != 1 {
		t.Errorf("too_large delta = %v", d)
	}
}
