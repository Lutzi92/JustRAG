package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

func TestStringifyToolResult_Text(t *testing.T) {
	res := DispatchedToolResult{Text: "4"}
	got, err := stringifyToolResult(res, nil)
	if err != nil || got != "4" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestStringifyToolResult_Structured(t *testing.T) {
	res := DispatchedToolResult{Structured: json.RawMessage(`{"rows":[{"id":1}]}`)}
	got, err := stringifyToolResult(res, nil)
	if err != nil || got != `{"rows":[{"id":1}]}` {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

// TestStringifyToolResult_TextWinsOverStructured locks in the precedence
// order from the function's doc comment: Text takes priority over
// Structured when both are populated.
func TestStringifyToolResult_TextWinsOverStructured(t *testing.T) {
	res := DispatchedToolResult{
		Text:       "human-readable",
		Structured: json.RawMessage(`{"raw":"data"}`),
	}
	got, err := stringifyToolResult(res, nil)
	if err != nil || got != "human-readable" {
		t.Fatalf("got=%q err=%v; Text must win over Structured", got, err)
	}
}

// TestStringifyToolResult_ChunkPreviewUTF8Safe verifies the per-chunk
// preview truncation does not split a multibyte UTF-8 rune. Chunk content
// from multilingual KBs (German, Chinese, emoji) must never produce an
// invalid byte sequence when the truncation boundary lands mid-rune.
func TestStringifyToolResult_ChunkPreviewUTF8Safe(t *testing.T) {
	// Content that pushes the boundary at answerToolChunkPreviewBytes=300
	// into the middle of a multibyte sequence. "ä" = 2 bytes, "✓" = 3
	// bytes, "🎉" = 4 bytes; build a 350-byte string mixing them.
	var sb strings.Builder
	for sb.Len() < 350 {
		sb.WriteString("Hä-✓-🎉-")
	}
	content := sb.String()
	res := DispatchedToolResult{Chunks: []vector.SearchChunk{{FileName: "f.md", Content: content}}}
	got, err := stringifyToolResult(res, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
}

func TestStringifyToolResult_Chunks(t *testing.T) {
	res := DispatchedToolResult{Chunks: []vector.SearchChunk{
		{FileName: "a.md", Content: "alpha"},
		{FileName: "b.md", Content: "beta"},
	}}
	got, err := stringifyToolResult(res, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(got, "1.") || !strings.Contains(got, "a.md") ||
		!strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
		t.Fatalf("rendered chunks missing expected pieces: %q", got)
	}
}

func TestStringifyToolResult_ChunksCapped(t *testing.T) {
	// 50 chunks × 200 chars of content each = ~10KB; the cap should clip
	// to roughly answerToolChunksMaxBytes (≈4096) plus the truncation marker.
	chunks := make([]vector.SearchChunk, 50)
	for i := range chunks {
		chunks[i] = vector.SearchChunk{
			FileName: "f.md",
			Content:  strings.Repeat("x", 200),
		}
	}
	got, err := stringifyToolResult(DispatchedToolResult{Chunks: chunks}, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got) > answerToolChunksMaxBytes+200 { // +200 for truncation tail slack
		t.Fatalf("output too large: %d bytes", len(got))
	}
	if !strings.Contains(got, "[truncated") {
		t.Fatalf("expected truncation marker in: %q", got)
	}
}

func TestStringifyToolResult_DispatchError(t *testing.T) {
	got, err := stringifyToolResult(DispatchedToolResult{}, errors.New("calculator: divide by zero"))
	if err != nil {
		t.Fatalf("dispatch error should NOT propagate: %v", err)
	}
	if !strings.Contains(got, "error") || !strings.Contains(got, "divide by zero") {
		t.Fatalf("error not surfaced as JSON: %q", got)
	}
}

func TestStringifyToolResult_Empty(t *testing.T) {
	got, err := stringifyToolResult(DispatchedToolResult{}, nil)
	if err != nil || got != "(empty result)" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestAssembleToolCalls_SingleCallAcrossChunks(t *testing.T) {
	deltas := []ai.ToolCallDelta{
		{Index: 0, ID: "c1", Type: "function", Name: "calculator", Arguments: `{"e`},
		{Index: 0, Arguments: `xpression":"2+2"}`},
	}
	calls := assembleToolCalls(deltas)
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(calls))
	}
	if calls[0].ID != "c1" || calls[0].Function.Name != "calculator" {
		t.Fatalf("call meta lost: %+v", calls[0])
	}
	if calls[0].Function.Arguments != `{"expression":"2+2"}` {
		t.Fatalf("args not concatenated: %q", calls[0].Function.Arguments)
	}
}

func TestAssembleToolCalls_ParallelCalls(t *testing.T) {
	deltas := []ai.ToolCallDelta{
		{Index: 0, ID: "a", Type: "function", Name: "calculator", Arguments: `{"x":1}`},
		{Index: 1, ID: "b", Type: "function", Name: "sql_query", Arguments: `{"q":"se`},
		{Index: 1, Arguments: `lect 1"}`},
	}
	calls := assembleToolCalls(deltas)
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d (%+v)", len(calls), calls)
	}
	// Order: by index ascending so callers see a stable execution order.
	if calls[0].ID != "a" || calls[1].ID != "b" {
		t.Fatalf("order: %+v", calls)
	}
	if calls[1].Function.Arguments != `{"q":"select 1"}` {
		t.Fatalf("parallel args lost: %q", calls[1].Function.Arguments)
	}
}

func TestAssembleToolCalls_EmptyInput(t *testing.T) {
	if got := assembleToolCalls(nil); got != nil {
		t.Fatalf("nil deltas should return nil, got %+v", got)
	}
}

// TestRunAnswerWithTools_InjectsKbIDIntoToolArgs locks in the AP fix
// where the loop must inject kb_id into the model's tool-call args
// before dispatch. Without this, tools like kb_search / count_mentions
// reject the call with "kb_id was not injected by the orchestrator".
func TestRunAnswerWithTools_InjectsKbIDIntoToolArgs(t *testing.T) {
	runner := &fakeRoundRunner{
		rounds: [][]roundStreamEvent{
			{
				{ToolCallDeltas: []ai.ToolCallDelta{
					{Index: 0, ID: "c1", Type: "function", Name: "count_mentions",
						Arguments: `{"query":"Karcher"}`},
				}},
				{FinishReason: "tool_calls", Done: true},
			},
			{
				{Content: "12"},
				{FinishReason: "stop", Done: true},
			},
		},
	}
	disp := &fakeToolDispatcher{result: DispatchedToolResult{Text: "12"}}
	err := runAnswerWithToolsWithRunner(context.Background(), runAnswerInput{
		Messages:   []ai.ChatMessage{{Role: "user", Content: "wie oft Karcher?"}},
		Tools:      []ai.ChatTool{{Type: "function", Function: ai.ChatToolFunction{Name: "count_mentions"}}},
		Dispatcher: disp,
		KbID:       "kb-42",
		MaxRounds:  5,
	}, runner, func(_ ai.StreamEvent) {}, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(disp.lastArgs, `"kb_id":"kb-42"`) {
		t.Fatalf("kb_id not injected; lastArgs=%q", disp.lastArgs)
	}
	if !strings.Contains(disp.lastArgs, `"query":"Karcher"`) {
		t.Fatalf("query field lost during injection; lastArgs=%q", disp.lastArgs)
	}
}

// TestRunAnswerWithTools_InjectsChatIDIntoMemoryToolArgs verifies that
// memory_read / memory_write get chat_id injected. Without this, the
// AnswerToolCatalog deliberately exposes memory tools to the LLM but
// every dispatch would fail with "chat_id was not injected by the
// orchestrator" (see internal/mcp/builtin/memory.go).
func TestRunAnswerWithTools_InjectsChatIDIntoMemoryToolArgs(t *testing.T) {
	runner := &fakeRoundRunner{
		rounds: [][]roundStreamEvent{
			{
				{ToolCallDeltas: []ai.ToolCallDelta{
					{Index: 0, ID: "c1", Type: "function", Name: "memory_read",
						Arguments: `{"limit":5}`},
				}},
				{FinishReason: "tool_calls", Done: true},
			},
			{
				{Content: "ok"},
				{FinishReason: "stop", Done: true},
			},
		},
	}
	disp := &fakeToolDispatcher{result: DispatchedToolResult{Text: "no notes"}}
	err := runAnswerWithToolsWithRunner(context.Background(), runAnswerInput{
		Messages:   []ai.ChatMessage{{Role: "user", Content: "what do you remember?"}},
		Tools:      []ai.ChatTool{{Type: "function", Function: ai.ChatToolFunction{Name: "memory_read"}}},
		Dispatcher: disp,
		KbID:       "kb-7",
		ChatID:     "chat-abc",
		MaxRounds:  5,
	}, runner, func(_ ai.StreamEvent) {}, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(disp.lastArgs, `"chat_id":"chat-abc"`) {
		t.Fatalf("chat_id not injected; lastArgs=%q", disp.lastArgs)
	}
	if !strings.Contains(disp.lastArgs, `"kb_id":"kb-7"`) {
		t.Fatalf("kb_id also expected; lastArgs=%q", disp.lastArgs)
	}
	if !strings.Contains(disp.lastArgs, `"limit":5`) {
		t.Fatalf("user-supplied arg lost during injection; lastArgs=%q", disp.lastArgs)
	}
}

// TestRunAnswerWithTools_BudgetExceededShortCircuits asserts that the
// loop consults the TurnBudget at the top of every round and, on the
// first exhausted dimension, jumps straight to the forced no-tool
// finish (matching agentic_chat.go's "budget_exceeded" handling).
// Without this, the loop would keep running tool-call rounds up to
// MaxRounds even after the AP-A3 budget had been blown.
func TestRunAnswerWithTools_BudgetExceededShortCircuits(t *testing.T) {
	// Round 1 would normally emit tool_calls; round 2 the forced finish.
	// We expect the loop to skip round 1 entirely because the budget is
	// exhausted before it runs.
	runner := &fakeRoundRunner{
		rounds: [][]roundStreamEvent{
			{{Content: "forced answer"}, {FinishReason: "stop", Done: true}},
		},
	}
	var toolChoices []any
	runner.observeToolChoice = func(tc any) { toolChoices = append(toolChoices, tc) }

	// Deadline already in the past — first CheckExceeded returns "time".
	budget := NewTurnBudget(1, 0)
	// Force the deadline to be in the past for the test.
	budget.Deadline = budget.Deadline.Add(-time.Hour)
	ctx := WithTurnBudget(context.Background(), budget)

	disp := &fakeToolDispatcher{}
	var traceCalls []string
	trace := func(_, decision, reason string, _ map[string]any) {
		if decision == "budget_exceeded" {
			traceCalls = append(traceCalls, "budget:"+reason)
		}
	}
	var content strings.Builder
	err := runAnswerWithToolsWithRunner(ctx, runAnswerInput{
		Messages:   []ai.ChatMessage{{Role: "user", Content: "anything"}},
		Tools:      []ai.ChatTool{{Type: "function", Function: ai.ChatToolFunction{Name: "calculator"}}},
		Dispatcher: disp,
		KbID:       "kb-1",
		MaxRounds:  5,
	}, runner, func(e ai.StreamEvent) { content.WriteString(e.Content) }, trace)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(traceCalls) != 1 || traceCalls[0] != "budget:time" {
		t.Fatalf("expected one budget_exceeded trajectory event with reason=time, got %v", traceCalls)
	}
	if len(disp.calls) != 0 {
		t.Fatalf("no tool dispatch should happen on a budget-exhausted run; got %+v", disp.calls)
	}
	// Exactly one upstream round runs — the forced finish with
	// tool_choice="none".
	if len(toolChoices) != 1 || toolChoices[0] != "none" {
		t.Fatalf("expected exactly one forced 'none' round, got %v", toolChoices)
	}
	if content.String() != "forced answer" {
		t.Fatalf("content=%q want 'forced answer'", content.String())
	}
}

// fakeStreamer mimics ai.StreamCompletion-style channels for testing the
// round handler in isolation. Returns a buffered channel pre-populated
// with the given events.
func fakeStreamer(events []ai.StreamEvent, toolDeltas []ai.ToolCallDelta, finish string) <-chan roundStreamEvent {
	ch := make(chan roundStreamEvent, len(events)+2)
	for _, e := range events {
		ch <- roundStreamEvent{Content: e.Content, Reasoning: e.Reasoning}
	}
	if len(toolDeltas) > 0 {
		ch <- roundStreamEvent{ToolCallDeltas: toolDeltas}
	}
	ch <- roundStreamEvent{FinishReason: finish, Done: true}
	close(ch)
	return ch
}

func TestStreamOneRound_ContentRoundStreamsLive(t *testing.T) {
	upstream := fakeStreamer(
		[]ai.StreamEvent{{Content: "the answer is "}, {Content: "4"}},
		nil, "stop",
	)
	var emitted []string
	emit := func(e ai.StreamEvent) {
		if e.Content != "" {
			emitted = append(emitted, e.Content)
		}
	}
	calls, finish, err := streamOneRound(context.Background(), upstream, emit)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if finish != "stop" {
		t.Fatalf("finish=%q want stop", finish)
	}
	if len(calls) != 0 {
		t.Fatalf("content round must not return tool calls: %+v", calls)
	}
	if strings.Join(emitted, "") != "the answer is 4" {
		t.Fatalf("content not streamed live: %q", emitted)
	}
}

func TestStreamOneRound_ToolRoundBuffersSilently(t *testing.T) {
	upstream := fakeStreamer(
		nil,
		[]ai.ToolCallDelta{
			{Index: 0, ID: "c1", Type: "function", Name: "calculator", Arguments: `{"expression":"2+2"}`},
		},
		"tool_calls",
	)
	var emitted []string
	emit := func(e ai.StreamEvent) {
		if e.Content != "" {
			emitted = append(emitted, e.Content)
		}
	}
	calls, finish, err := streamOneRound(context.Background(), upstream, emit)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish=%q want tool_calls", finish)
	}
	if len(calls) != 1 || calls[0].Function.Name != "calculator" {
		t.Fatalf("calls=%+v", calls)
	}
	if len(emitted) != 0 {
		t.Fatalf("tool round must NOT stream content: %q", emitted)
	}
}

// fakeToolDispatcher records the calls it receives and returns canned
// results. Satisfies the chat.ToolDispatcher interface used by the loop.
type fakeToolDispatcher struct {
	calls    []string
	lastArgs string
	result   DispatchedToolResult
	err      error
}

func (f *fakeToolDispatcher) Dispatch(_ context.Context, _, tool string, args json.RawMessage) (DispatchedToolResult, error) {
	f.calls = append(f.calls, tool)
	f.lastArgs = string(args)
	return f.result, f.err
}

// fakeRoundRunner is a swappable replacement for the real
// ai.StreamChatCompletion-backed runner. Each entry in `rounds` is one
// round's worth of upstream events.
type fakeRoundRunner struct {
	rounds            [][]roundStreamEvent
	seen              int
	observeToolChoice func(any) // optional hook used by later tests
}

func (f *fakeRoundRunner) Run(_ context.Context, _ []ai.ChatMessage, _ []ai.ChatTool, toolChoice any) (<-chan roundStreamEvent, error) {
	if f.observeToolChoice != nil {
		f.observeToolChoice(toolChoice)
	}
	if f.seen >= len(f.rounds) {
		return nil, fmt.Errorf("fakeRoundRunner: out of rounds (seen=%d)", f.seen)
	}
	ch := make(chan roundStreamEvent, len(f.rounds[f.seen]))
	for _, e := range f.rounds[f.seen] {
		ch <- e
	}
	close(ch)
	f.seen++
	return ch, nil
}

func TestRunAnswerWithTools_NoToolsHappyPath(t *testing.T) {
	runner := &fakeRoundRunner{
		rounds: [][]roundStreamEvent{{
			{Content: "no tools needed: 4"},
			{FinishReason: "stop", Done: true},
		}},
	}
	disp := &fakeToolDispatcher{}
	var events []ai.StreamEvent
	err := runAnswerWithToolsWithRunner(context.Background(), runAnswerInput{
		Messages: []ai.ChatMessage{
			{Role: "system", Content: "you are helpful"},
			{Role: "user", Content: "what is 2+2"},
		},
		Tools:      []ai.ChatTool{{Type: "function", Function: ai.ChatToolFunction{Name: "calculator"}}},
		Dispatcher: disp,
		KbID:       "kb-1",
		MaxRounds:  5,
	}, runner, func(e ai.StreamEvent) { events = append(events, e) }, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(disp.calls) != 0 {
		t.Fatalf("no tools should have been dispatched: %+v", disp.calls)
	}
	var content strings.Builder
	for _, e := range events {
		content.WriteString(e.Content)
	}
	if content.String() != "no tools needed: 4" {
		t.Fatalf("content: %q", content.String())
	}
}

func TestRunAnswerWithTools_OneToolCallThenAnswer(t *testing.T) {
	runner := &fakeRoundRunner{
		rounds: [][]roundStreamEvent{
			// Round 1: model emits a tool_call.
			{
				{ToolCallDeltas: []ai.ToolCallDelta{
					{Index: 0, ID: "c1", Type: "function", Name: "calculator",
						Arguments: `{"expression":"47.3*8.2"}`},
				}},
				{FinishReason: "tool_calls", Done: true},
			},
			// Round 2: model emits the final answer.
			{
				{Content: "the answer is 387.86"},
				{FinishReason: "stop", Done: true},
			},
		},
	}
	disp := &fakeToolDispatcher{result: DispatchedToolResult{Text: "387.86"}}
	var events []ai.StreamEvent
	var traceCalls []string
	trace := func(_, decision, reason string, _ map[string]any) {
		if decision == "agent_tool_call" {
			traceCalls = append(traceCalls, "before:"+reason)
		}
		if decision == "agent_tool_call_result" {
			traceCalls = append(traceCalls, "after:"+reason)
		}
	}
	err := runAnswerWithToolsWithRunner(context.Background(), runAnswerInput{
		Messages:   []ai.ChatMessage{{Role: "user", Content: "47.3 * 8.2"}},
		Tools:      []ai.ChatTool{{Type: "function", Function: ai.ChatToolFunction{Name: "calculator"}}},
		Dispatcher: disp,
		KbID:       "kb-1",
		MaxRounds:  5,
	}, runner, func(e ai.StreamEvent) { events = append(events, e) }, trace)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(disp.calls) != 1 || disp.calls[0] != "calculator" {
		t.Fatalf("dispatch sequence: %+v", disp.calls)
	}
	if len(traceCalls) != 2 ||
		traceCalls[0] != "before:calculator" ||
		traceCalls[1] != "after:calculator" {
		t.Fatalf("trajectory events: %+v", traceCalls)
	}
	var content strings.Builder
	for _, e := range events {
		content.WriteString(e.Content)
	}
	if content.String() != "the answer is 387.86" {
		t.Fatalf("final content: %q", content.String())
	}
	if runner.seen != 2 {
		t.Fatalf("rounds consumed: %d, want 2", runner.seen)
	}
}

func TestRunAnswerWithTools_ExhaustionForcesNoToolFinish(t *testing.T) {
	// 3 tool rounds, then a forced content round.
	toolRound := []roundStreamEvent{
		{ToolCallDeltas: []ai.ToolCallDelta{
			{Index: 0, ID: "c", Type: "function", Name: "calculator", Arguments: `{"expression":"1"}`},
		}},
		{FinishReason: "tool_calls", Done: true},
	}
	runner := &fakeRoundRunner{
		rounds: [][]roundStreamEvent{
			toolRound, toolRound, toolRound,
			{{Content: "forced answer"}, {FinishReason: "stop", Done: true}},
		},
	}
	var toolChoices []any
	runner.observeToolChoice = func(tc any) { toolChoices = append(toolChoices, tc) }

	disp := &fakeToolDispatcher{result: DispatchedToolResult{Text: "1"}}
	var events []ai.StreamEvent
	err := runAnswerWithToolsWithRunner(context.Background(), runAnswerInput{
		Messages:   []ai.ChatMessage{{Role: "user", Content: "loop please"}},
		Tools:      []ai.ChatTool{{Type: "function", Function: ai.ChatToolFunction{Name: "calculator"}}},
		Dispatcher: disp,
		KbID:       "kb-1",
		MaxRounds:  3,
	}, runner, func(e ai.StreamEvent) { events = append(events, e) }, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(disp.calls) != 3 {
		t.Fatalf("expected 3 dispatched calls, got %d", len(disp.calls))
	}
	// First 3 rounds use "auto", forced 4th uses "none".
	if len(toolChoices) != 4 {
		t.Fatalf("toolChoices len=%d, want 4 (%+v)", len(toolChoices), toolChoices)
	}
	for i := 0; i < 3; i++ {
		if toolChoices[i] != "auto" {
			t.Fatalf("round %d tool_choice=%v, want auto", i+1, toolChoices[i])
		}
	}
	if toolChoices[3] != "none" {
		t.Fatalf("forced final round tool_choice=%v, want none", toolChoices[3])
	}
	var content strings.Builder
	for _, e := range events {
		content.WriteString(e.Content)
	}
	if content.String() != "forced answer" {
		t.Fatalf("final content: %q", content.String())
	}
}

// chatTestConfigStore is a minimal ai.ConfigStore for RunAnswerWithTools
// integration tests. Returns a single provider+model pointing at the test
// httptest server URL.
type chatTestConfigStore struct {
	baseURL string
	model   string
}

func (s *chatTestConfigStore) GetActiveAIProvider(_ context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{ID: "p1", Name: "test", APIKey: "k", BaseURL: s.baseURL}, nil
}
func (s *chatTestConfigStore) GetAIProviderByID(_ context.Context, _ string) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{ID: "p1", Name: "test", APIKey: "k", BaseURL: s.baseURL}, nil
}
func (s *chatTestConfigStore) GetAIModelsByProvider(_ context.Context, _ string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{{Name: s.model}}, nil
}
func (s *chatTestConfigStore) GetKBModelOverrides(_ context.Context, _ string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

func TestRunAnswerWithTools_LiveStreamRunner(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		``,
		`data: {"choices":[{"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got ai.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&got)
		if len(got.Tools) != 1 || got.Tools[0].Function.Name != "calculator" {
			t.Errorf("tools forwarded incorrectly: %+v", got.Tools)
		}
		if got.ToolChoice != "auto" {
			t.Errorf("tool_choice: %v", got.ToolChoice)
		}
		if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Role != "user" {
			t.Errorf("messages: %+v", got.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	store := &chatTestConfigStore{baseURL: srv.URL + "/v1/", model: "fake-model"}
	resolver := ai.NewConfigResolver(store)

	var events []ai.StreamEvent
	err := RunAnswerWithTools(context.Background(), AnswerToolsParams{
		AIResolver:   resolver,
		KbID:         "kb-1",
		SystemPrompt: "you are helpful",
		UserPrompt:   "hi",
		Tools: []ai.ChatTool{{Type: "function", Function: ai.ChatToolFunction{
			Name: "calculator", Description: "math", Parameters: json.RawMessage(`{}`),
		}}},
		Dispatcher: &fakeToolDispatcher{},
		MaxRounds:  5,
	}, func(e ai.StreamEvent) { events = append(events, e) }, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	var content strings.Builder
	for _, e := range events {
		content.WriteString(e.Content)
	}
	if content.String() != "hello" {
		t.Fatalf("content=%q want hello", content.String())
	}
}
