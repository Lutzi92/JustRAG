package ai

import (
	"strings"
	"testing"
)

// drain feeds inputs through the filter in order and collects every
// emitted segment plus the final Flush() result so the test can assert
// against the full transcript.
func drain(t *testing.T, inputs ...string) (reasoning, content string) {
	t.Helper()
	var f ThinkTagFilter
	var rb, cb strings.Builder
	for _, in := range inputs {
		for _, seg := range f.Process(in) {
			rb.WriteString(seg.Reasoning)
			cb.WriteString(seg.Content)
		}
	}
	tail := f.Flush()
	rb.WriteString(tail.Reasoning)
	cb.WriteString(tail.Content)
	return rb.String(), cb.String()
}

func TestThinkTagFilter_NoTags(t *testing.T) {
	t.Parallel()
	r, c := drain(t, "just content")
	if r != "" || c != "just content" {
		t.Fatalf("reasoning=%q content=%q", r, c)
	}
}

func TestThinkTagFilter_SingleTagOneCall(t *testing.T) {
	t.Parallel()
	r, c := drain(t, "before<think>inner</think>after")
	if r != "inner" {
		t.Errorf("reasoning=%q", r)
	}
	if c != "beforeafter" {
		t.Errorf("content=%q", c)
	}
}

// Splits the closing tag across two Process calls. The buffer must keep the
// partial "</thi" so that the next call resolves it as a tag boundary, not
// emit it as reasoning text.
func TestThinkTagFilter_ClosingTagSplit(t *testing.T) {
	t.Parallel()
	r, c := drain(t, "<think>thought</thi", "nk>answer")
	if r != "thought" {
		t.Errorf("reasoning=%q", r)
	}
	if c != "answer" {
		t.Errorf("content=%q", c)
	}
}

// Splits the opening tag across calls. Symmetric to the closing-split test.
func TestThinkTagFilter_OpeningTagSplit(t *testing.T) {
	t.Parallel()
	r, c := drain(t, "head<thi", "nk>brain</think>tail")
	if r != "brain" {
		t.Errorf("reasoning=%q", r)
	}
	if c != "headtail" {
		t.Errorf("content=%q", c)
	}
}

func TestThinkTagFilter_MultipleBlocks(t *testing.T) {
	t.Parallel()
	r, c := drain(t, "a<think>x</think>b<think>y</think>c")
	if r != "xy" {
		t.Errorf("reasoning=%q", r)
	}
	if c != "abc" {
		t.Errorf("content=%q", c)
	}
}

// An opening tag that the stream ends before closing: the buffered
// in-tag text should surface as reasoning at Flush, not content.
func TestThinkTagFilter_UnterminatedTagFlushesAsReasoning(t *testing.T) {
	t.Parallel()
	r, c := drain(t, "before<think>orphan")
	if r != "orphan" {
		t.Errorf("reasoning=%q want %q", r, "orphan")
	}
	if c != "before" {
		t.Errorf("content=%q want %q", c, "before")
	}
}

// A bare partial opener that never resolves should surface as content at
// Flush (it was not actually a tag) — preserves the original characters
// so the user sees what the model emitted.
func TestThinkTagFilter_PartialOpenerFlushesAsContent(t *testing.T) {
	t.Parallel()
	r, c := drain(t, "answer<thi")
	if r != "" {
		t.Errorf("reasoning=%q", r)
	}
	if c != "answer<thi" {
		t.Errorf("content=%q", c)
	}
}

// Streaming one character at a time exercises the buffer/partial-tag
// detection on every boundary. Output must match the single-shot result.
func TestThinkTagFilter_OneByteAtATime(t *testing.T) {
	t.Parallel()
	input := "lead<think>mind</think>tail"
	chunks := make([]string, 0, len(input))
	for _, b := range input {
		chunks = append(chunks, string(b))
	}
	r, c := drain(t, chunks...)
	if r != "mind" {
		t.Errorf("reasoning=%q", r)
	}
	if c != "leadtail" {
		t.Errorf("content=%q", c)
	}
}

func TestThinkTagFilter_EmptyInputs(t *testing.T) {
	t.Parallel()
	var f ThinkTagFilter
	if segs := f.Process(""); segs != nil {
		t.Fatalf("Process(\"\") returned %v, want nil", segs)
	}
	if tail := f.Flush(); tail.Reasoning != "" || tail.Content != "" {
		t.Fatalf("Flush on empty returned %+v", tail)
	}
}
