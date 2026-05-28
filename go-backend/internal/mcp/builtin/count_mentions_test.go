package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeMentionsCounter returns per-query canned counts. If counts[pattern]
// is set, that count is returned; otherwise defaultN is used.
type fakeMentionsCounter struct {
	counts   map[string]int
	defaultN int
	err      error
}

func (f *fakeMentionsCounter) CountMentions(_ context.Context, _, pattern string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if n, ok := f.counts[pattern]; ok {
		return n, nil
	}
	return f.defaultN, nil
}

func TestCountMentions_SingleQuery(t *testing.T) {
	t.Parallel()
	tool := NewCountMentions(&fakeMentionsCounter{defaultN: 12})
	args, _ := json.Marshal(map[string]any{"query": "Karcher", "kb_id": "kb-1"})
	res, err := tool.Handler.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(res.Text, `"Karcher"`) || !strings.Contains(res.Text, "12") {
		t.Fatalf("Text missing expected pieces: %q", res.Text)
	}
}

func TestCountMentions_MultiQuery_SortsByCountDesc(t *testing.T) {
	t.Parallel()
	tool := NewCountMentions(&fakeMentionsCounter{counts: map[string]int{
		"Karcher":         12,
		"Steffen Karcher": 1,
		"steffen.karcher": 1,
	}})
	args, _ := json.Marshal(map[string]any{
		"queries": []string{"Steffen Karcher", "Karcher", "steffen.karcher"},
		"kb_id":   "kb-1",
	})
	res, err := tool.Handler.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	// First line of Text must be the largest count ("Karcher": 12) so
	// the model's eye lands on the most meaningful figure first.
	firstLine := strings.SplitN(res.Text, "\n", 2)[0]
	if !strings.Contains(firstLine, `"Karcher"`) || !strings.Contains(firstLine, "12") {
		t.Fatalf("first line should be Karcher:12, got %q (full: %q)", firstLine, res.Text)
	}
	// Structured payload also carries all three counts.
	var s struct {
		Results []struct {
			Query string
			Count int
		}
	}
	if err := json.Unmarshal(res.Structured, &s); err != nil {
		t.Fatalf("structured unmarshal: %v", err)
	}
	if len(s.Results) != 3 {
		t.Fatalf("want 3 results, got %d", len(s.Results))
	}
	if s.Results[0].Query != "Karcher" || s.Results[0].Count != 12 {
		t.Fatalf("first sorted result wrong: %+v", s.Results[0])
	}
}

func TestCountMentions_QueryAndQueriesCombined(t *testing.T) {
	t.Parallel()
	tool := NewCountMentions(&fakeMentionsCounter{counts: map[string]int{
		"Karcher":         12,
		"Steffen Karcher": 1,
	}})
	args, _ := json.Marshal(map[string]any{
		"query":   "Karcher",
		"queries": []string{"Steffen Karcher"},
		"kb_id":   "kb-1",
	})
	res, err := tool.Handler.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(res.Text, `"Karcher": 12`) || !strings.Contains(res.Text, `"Steffen Karcher": 1`) {
		t.Fatalf("Text missing one of the queries: %q", res.Text)
	}
}

func TestCountMentions_RejectsAllEmpty(t *testing.T) {
	t.Parallel()
	tool := NewCountMentions(&fakeMentionsCounter{})
	args, _ := json.Marshal(map[string]any{"kb_id": "kb-1"})
	_, err := tool.Handler.Invoke(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required-error, got %v", err)
	}
}

func TestCountMentions_RejectsMissingKbID(t *testing.T) {
	t.Parallel()
	tool := NewCountMentions(&fakeMentionsCounter{})
	args, _ := json.Marshal(map[string]any{"query": "x"})
	_, err := tool.Handler.Invoke(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "kb_id") {
		t.Fatalf("expected kb_id error, got %v", err)
	}
}

func TestCountMentions_PropagatesSvcError(t *testing.T) {
	t.Parallel()
	tool := NewCountMentions(&fakeMentionsCounter{err: errFakeCountSvc})
	args, _ := json.Marshal(map[string]any{"query": "x", "kb_id": "kb-1"})
	_, err := tool.Handler.Invoke(context.Background(), args)
	if err == nil {
		t.Fatalf("expected error from svc")
	}
}

func TestCountMentions_SkipsEmptyEntriesInQueries(t *testing.T) {
	t.Parallel()
	tool := NewCountMentions(&fakeMentionsCounter{counts: map[string]int{"Karcher": 12}})
	args, _ := json.Marshal(map[string]any{
		"queries": []string{"", "Karcher", ""},
		"kb_id":   "kb-1",
	})
	res, err := tool.Handler.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	// Only one non-empty entry → single line of output.
	if strings.Count(res.Text, "\n") != 0 {
		t.Fatalf("expected single-line output, got %q", res.Text)
	}
}

type countSvcError string

func (s countSvcError) Error() string { return string(s) }

var errFakeCountSvc = countSvcError("svc failed")
