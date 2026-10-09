package adkbridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/mcp"
)

// fakeClient replays scripted turns and records every request it saw.
type fakeClient struct {
	mu    sync.Mutex
	turns [][]ai.StreamChunk
	reqs  []ai.ChatRequest
}

func (f *fakeClient) next(req ai.ChatRequest) []ai.StreamChunk {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if len(f.turns) == 0 {
		return []ai.StreamChunk{{Content: "(no more turns)", FinishReason: "stop", Done: true}}
	}
	t := f.turns[0]
	f.turns = f.turns[1:]
	return t
}

func (f *fakeClient) ChatCompletion(_ context.Context, req *ai.ChatRequest) (*ai.ChatResponse, error) {
	var ch ai.ChatChoice
	for _, c := range f.next(*req) {
		ch.Message.Content += c.Content
		ch.Message.ReasoningContent += c.ReasoningContent
		for _, d := range c.ToolCallDeltas {
			ch.Message.ToolCalls = append(ch.Message.ToolCalls, ai.ToolCall{ID: d.ID, Type: "function",
				Function: ai.ToolCallFunction{Name: d.Name, Arguments: d.Arguments}})
		}
		if c.FinishReason != "" {
			ch.FinishReason = c.FinishReason
		}
	}
	return &ai.ChatResponse{Choices: []ai.ChatChoice{ch}, Usage: ai.ChatUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}, nil
}

func (f *fakeClient) StreamChatCompletion(_ context.Context, req ai.ChatRequest) (<-chan ai.StreamChunk, error) {
	chunks := f.next(req)
	ch := make(chan ai.StreamChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (f *fakeClient) requests() []ai.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ai.ChatRequest(nil), f.reqs...)
}

// toolTurn streams a tool call split across two deltas, as vLLM does.
func toolTurn(id, name, args string) []ai.StreamChunk {
	half := len(args) / 2
	return []ai.StreamChunk{
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, ID: id, Name: name, Arguments: args[:half]}}},
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, Arguments: args[half:]}}},
		{FinishReason: "tool_calls", Done: true},
	}
}

func TestStreamingAggregatesThoughtTextAndCalls(t *testing.T) {
	fc := &fakeClient{turns: [][]ai.StreamChunk{{
		{ReasoningContent: "let me "},
		{ReasoningContent: "think"},
		{Content: "Hal"},
		{Content: "lo"},
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, Name: "kb_search", Arguments: `{"query":`}}},
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, Arguments: `"x"}`}}},
		{FinishReason: "tool_calls", Done: true},
	}}}
	m := NewModel(fc, "gemma")
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}}

	var partials int
	var final *model.LLMResponse
	for resp, err := range m.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatal(err)
		}
		if resp.Partial {
			partials++
			continue
		}
		final = resp
	}
	if partials != 4 {
		t.Fatalf("partials = %d, want 4 (2 thought + 2 text)", partials)
	}
	if final == nil || !final.TurnComplete {
		t.Fatal("no complete final response")
	}
	parts := final.Content.Parts
	if len(parts) != 3 || !parts[0].Thought || parts[0].Text != "let me think" || parts[1].Text != "Hallo" {
		t.Fatalf("unexpected parts: %+v", parts)
	}
	fcall := parts[2].FunctionCall
	if fcall == nil || fcall.Name != "kb_search" || fcall.Args["query"] != "x" || fcall.ID == "" {
		t.Fatalf("function call not assembled: %+v", fcall)
	}
}

func TestBuildRequestMapsSystemToolsSchemaAndHistory(t *testing.T) {
	temp := float32(0.2)
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("frage", genai.RoleUser),
			{Role: genai.RoleModel, Parts: []*genai.Part{
				{Text: "secret thoughts", Thought: true},
				{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "kb_search", Args: map[string]any{"query": "q"}}},
			}},
			{Role: genai.RoleUser, Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{ID: "c1", Name: "kb_search", Response: map[string]any{"result": "r"}}},
			}},
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("sys", "system"),
			Temperature:       &temp,
			ResponseSchema:    &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"verdict": {Type: genai.TypeString}}},
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: "kb_search", Description: "d", ParametersJsonSchema: json.RawMessage(`{"type":"object"}`),
			}}}},
			ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true},
		},
	}
	got, err := NewModel(&fakeClient{}, "gemma").buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, m := range got.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Fatalf("roles = %v", roles)
	}
	if got.Messages[2].Content != "" || len(got.Messages[2].ToolCalls) != 1 {
		t.Fatalf("thoughts must not be replayed; tool call must be: %+v", got.Messages[2])
	}
	if got.Messages[3].ToolCallID != "c1" {
		t.Fatalf("tool response not paired: %+v", got.Messages[3])
	}
	if got.ResponseFormat == nil || got.ResponseFormat.Type != "json_schema" ||
		!strings.Contains(string(got.ResponseFormat.JSONSchema.Schema), `"type":"object"`) {
		t.Fatalf("schema not lower-cased JSON schema: %+v", got.ResponseFormat)
	}
	if len(got.Tools) != 1 || got.ChatTemplateKwargs["enable_thinking"] != true || got.ReasoningEffort == "" {
		t.Fatalf("tools/thinking not mapped: tools=%d kwargs=%v effort=%q", len(got.Tools), got.ChatTemplateKwargs, got.ReasoningEffort)
	}
}

func newRunner(t *testing.T, fc *fakeClient, tools []tool.Tool, svc session.Service) *runner.Runner {
	t.Helper()
	a, err := llmagent.New(llmagent.Config{
		Name:        "kb_agent",
		Model:       NewModel(fc, "gemma"),
		Instruction: "Answer from the knowledge base.",
		Tools:       tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "justrag", Agent: a, SessionService: svc, AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fakeSearch(calls *int) DispatchFunc {
	return func(_ context.Context, _ string, _ string, _ json.RawMessage) (mcp.ToolResult, error) {
		*calls++
		return mcp.ToolResult{Text: "Die Mensa öffnet um 11 Uhr. [1]"}, nil
	}
}

func run(t *testing.T, r *runner.Runner, sid string, msg *genai.Content) []*session.Event {
	t.Helper()
	var evs []*session.Event
	for ev, err := range r.Run(WithScope(context.Background(), Scope{UserID: "u1", KBID: "kb", Role: "edit"}), "u1", sid, msg, agent.RunConfig{StreamingMode: agent.StreamingModeSSE}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func finalText(evs []*session.Event) string {
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		if ev.Partial || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.Text != "" && !p.Thought {
				return p.Text
			}
		}
	}
	return ""
}

func TestAgentToolLoopEndToEnd(t *testing.T) {
	fc := &fakeClient{turns: [][]ai.StreamChunk{
		toolTurn("call_1", "kb_search", `{"query":"Mensa"}`),
		{{Content: "Die Mensa öffnet um 11 Uhr [1]."}, {FinishReason: "stop", Done: true}},
	}}
	calls := 0
	search := NewTool(ToolSpec{Name: "kb_search", Description: "search", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}, fakeSearch(&calls))
	r := newRunner(t, fc, []tool.Tool{search}, session.InMemoryService())

	evs := run(t, r, "s1", genai.NewContentFromText("Wann öffnet die Mensa?", genai.RoleUser))
	if calls != 1 {
		t.Fatalf("tool calls = %d", calls)
	}
	if got := finalText(evs); !strings.Contains(got, "11 Uhr") {
		t.Fatalf("final text = %q", got)
	}
	reqs := fc.requests()
	if len(reqs) != 2 {
		t.Fatalf("model rounds = %d", len(reqs))
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "call_1" || !strings.Contains(last.Content, "11 Uhr") {
		t.Fatalf("tool result not fed back: %+v", last)
	}
}

// TestApprovalPausesAndResumes is the "approval: always" policy: the tool
// must not run until the user confirms on a follow-up turn.
func TestApprovalPausesAndResumes(t *testing.T) {
	fc := &fakeClient{turns: [][]ai.StreamChunk{
		toolTurn("call_1", "confluence_import", `{"spaceKey":"HRZ"}`),
		{{Content: "Import gestartet."}, {FinishReason: "stop", Done: true}},
	}}
	calls := 0
	imp := NewTool(ToolSpec{Name: "confluence_import", Description: "import", Policy: PolicyFor("confluence_import"),
		InputSchema: json.RawMessage(`{"type":"object","properties":{"spaceKey":{"type":"string"}}}`)}, fakeSearch(&calls))
	svc := session.InMemoryService()
	r := newRunner(t, fc, []tool.Tool{imp}, svc)

	evs := run(t, r, "s1", genai.NewContentFromText("Importiere HRZ", genai.RoleUser))
	if calls != 0 {
		t.Fatal("tool ran before approval")
	}
	var confirmID string
	for _, ev := range evs {
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionCall != nil && p.FunctionCall.Name == "adk_request_confirmation" {
				confirmID = p.FunctionCall.ID
			}
		}
	}
	if confirmID == "" {
		t.Fatalf("no confirmation request emitted; events: %d", len(evs))
	}

	// The user approves: a FunctionResponse answering the confirmation call.
	approve := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID: confirmID, Name: "adk_request_confirmation", Response: map[string]any{"confirmed": true},
	}}}}
	evs = run(t, r, "s1", approve)
	if calls != 1 {
		t.Fatalf("tool calls after approval = %d", calls)
	}
	if got := finalText(evs); !strings.Contains(got, "Import gestartet") {
		t.Fatalf("final text = %q", got)
	}
}

// blockingStreamClient emits chunks until its ctx is cancelled and closes
// done when the producer goroutine exits (as ai.StreamChatCompletion's would).
type blockingStreamClient struct {
	fakeClient
	done chan struct{}
}

func (b *blockingStreamClient) StreamChatCompletion(ctx context.Context, _ ai.ChatRequest) (<-chan ai.StreamChunk, error) {
	ch := make(chan ai.StreamChunk)
	go func() {
		defer close(b.done)
		defer close(ch)
		for {
			select {
			case ch <- ai.StreamChunk{Content: "x"}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// TestStreamEarlyStopReleasesProducer: a consumer that stops iterating must
// cancel the stream so the producer goroutine (and its HTTP body) is freed.
func TestStreamEarlyStopReleasesProducer(t *testing.T) {
	bc := &blockingStreamClient{done: make(chan struct{})}
	m := NewModel(bc, "gemma")
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}}
	for resp, err := range m.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatal(err)
		}
		if resp.Partial {
			break
		}
	}
	select {
	case <-bc.done:
	case <-time.After(time.Second):
		t.Fatal("producer goroutine still running after consumer stopped early")
	}
}

func TestInlineThinkTagsBecomeThoughts(t *testing.T) {
	fc := &fakeClient{turns: [][]ai.StreamChunk{{
		{Content: "<think>abw"}, {Content: "ägen</think>Ant"}, {Content: "wort"}, {FinishReason: "stop", Done: true},
	}}}
	var final *model.LLMResponse
	for resp, err := range NewModel(fc, "m").GenerateContent(context.Background(),
		&model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("q", genai.RoleUser)}}, true) {
		if err != nil {
			t.Fatal(err)
		}
		if !resp.Partial {
			final = resp
		}
	}
	p := final.Content.Parts
	if len(p) != 2 || !p[0].Thought || p[0].Text != "abwägen" || p[1].Text != "Antwort" {
		t.Fatalf("parts = %+v", p)
	}
}

func TestMalformedToolArgsReachToolAsSentinel(t *testing.T) {
	fc := &fakeClient{turns: [][]ai.StreamChunk{{
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, ID: "c", Name: "kb_search", Arguments: `{"query": "unterminated`}}},
		{FinishReason: "tool_calls", Done: true},
	}}}
	var final *model.LLMResponse
	for resp, err := range NewModel(fc, "m").GenerateContent(context.Background(),
		&model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("q", genai.RoleUser)}}, true) {
		if err != nil {
			t.Fatal(err)
		}
		final = resp
	}
	args := final.Content.Parts[0].FunctionCall.Args
	if _, ok := args["__invalid_arguments"]; !ok {
		t.Fatalf("args = %v", args)
	}
}

func textTurn(s string) []ai.StreamChunk {
	return []ai.StreamChunk{{Content: s}, {FinishReason: "stop", Done: true}}
}

func runCfg() agent.RunConfig { return agent.RunConfig{StreamingMode: agent.StreamingModeSSE} }

// genStream runs one streamed GenerateContent call and returns its partials
// and final response.
func genStream(t *testing.T, m *Model) (partials []*model.LLMResponse, final *model.LLMResponse) {
	t.Helper()
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("q", genai.RoleUser)}}
	for resp, err := range m.GenerateContent(context.Background(), req, true) {
		if err != nil {
			t.Fatal(err)
		}
		if resp.Partial {
			partials = append(partials, resp)
		} else {
			final = resp
		}
	}
	return partials, final
}

// Providers that omit tool-call ids must not yield the same minted id in
// two turns of one run: AG-UI toolCallIds and ADK's id pairing need them
// unique across the run.
func TestMintedToolCallIDsAreUniqueAcrossTurns(t *testing.T) {
	noID := []ai.StreamChunk{
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, Name: "kb_search", Arguments: `{"query":"a"}`}}},
		{FinishReason: "tool_calls", Done: true},
	}
	m := NewModel(&fakeClient{turns: [][]ai.StreamChunk{noID, noID}}, "m")
	_, f1 := genStream(t, m)
	_, f2 := genStream(t, m)
	id1, id2 := f1.Content.Parts[0].FunctionCall.ID, f2.Content.Parts[0].FunctionCall.ID
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("minted ids not unique: %q %q", id1, id2)
	}
}

// A tail the think-tag filter held back until Flush must reach the wire as
// a partial: AG-UI drops the final text once partials were streamed.
func TestFlushedTailIsStreamedAsPartial(t *testing.T) {
	m := NewModel(&fakeClient{turns: [][]ai.StreamChunk{{
		{Content: "a < b, also x <"}, {FinishReason: "stop", Done: true},
	}}}, "m")
	partials, final := genStream(t, m)
	var streamed strings.Builder
	for _, p := range partials {
		for _, part := range p.Content.Parts {
			if !part.Thought {
				streamed.WriteString(part.Text)
			}
		}
	}
	if streamed.String() != "a < b, also x <" || final.Content.Parts[0].Text != "a < b, also x <" {
		t.Fatalf("streamed=%q final=%q", streamed.String(), final.Content.Parts[0].Text)
	}
}
