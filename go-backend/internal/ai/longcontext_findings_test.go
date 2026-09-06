package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/prompts"
)

func TestExtractLongContextFindings_HappyPath(t *testing.T) {
	r := stubCompletion(t, `{"findings":[{"source_idx":2,"claim":"A stated X","quote":"X happened"},{"source_idx":5,"claim":"B stated Y","quote":""}]}`)
	got, err := ExtractLongContextFindings(context.Background(), r, "q", "[2] ...\n[5] ...", "kb", "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 2 || got[0].SourceIdx != 2 || got[0].Claim != "A stated X" || got[0].Quote != "X happened" {
		t.Fatalf("unexpected findings: %#v", got)
	}
}

func TestExtractLongContextFindings_ProseWrappedJSON(t *testing.T) {
	r := stubCompletion(t, "Here you go:\n{\"findings\":[{\"source_idx\":1,\"claim\":\"c\",\"quote\":\"q\"}]}\nDone.")
	got, err := ExtractLongContextFindings(context.Background(), r, "q", "g", "kb", "de", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 1 || got[0].SourceIdx != 1 {
		t.Fatalf("prose-wrapped JSON not recovered: %#v", got)
	}
}

func TestExtractLongContextFindings_BareArrayTolerated(t *testing.T) {
	r := stubCompletion(t, `[{"source_idx":3,"claim":"c","quote":"q"}]`)
	got, err := ExtractLongContextFindings(context.Background(), r, "q", "g", "kb", "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 1 || got[0].SourceIdx != 3 {
		t.Fatalf("bare array not recovered: %#v", got)
	}
}

func TestExtractLongContextFindings_DropsEmptyClaims(t *testing.T) {
	r := stubCompletion(t, `{"findings":[{"source_idx":1,"claim":"  ","quote":"q"},{"source_idx":2,"claim":" real ","quote":" q2 "}]}`)
	got, _ := ExtractLongContextFindings(context.Background(), r, "q", "g", "kb", "en", "")
	if len(got) != 1 || got[0].Claim != "real" || got[0].Quote != "q2" {
		t.Fatalf("want one trimmed finding, got %#v", got)
	}
}

func TestExtractLongContextFindings_GarbageIsEmptyNotError(t *testing.T) {
	r := stubCompletion(t, "no json here")
	got, err := ExtractLongContextFindings(context.Background(), r, "q", "g", "kb", "en", "")
	if err != nil {
		t.Fatalf("garbage must not error (caller falls back): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty findings, got %#v", got)
	}
}

func TestExtractLongContextFindings_LLMErrorBubbles(t *testing.T) {
	r := stubCompletionError(t, errors.New("boom"))
	if _, err := ExtractLongContextFindings(context.Background(), r, "q", "g", "kb", "en", ""); err == nil {
		t.Fatalf("want the transport error to bubble so the group falls back")
	}
}

func TestLongContextPrompts_Langs(t *testing.T) {
	if en, de := prompts.LongContextFindingsPrompt("en"), prompts.LongContextFindingsPrompt("de"); en == "" || de == "" || en == de {
		t.Fatalf("findings prompt must differ per language")
	}
	if en, de := prompts.LongContextSynthesisSystem("en"), prompts.LongContextSynthesisSystem("de"); en == "" || de == "" || en == de {
		t.Fatalf("synthesis prompt must differ per language")
	}
}
