package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	"github.com/justrag/go-backend/internal/adkbridge"
	"github.com/justrag/go-backend/internal/agui"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/vector"
)

// agentFakeClient replays scripted answer turns and records every request.
// A copy of adkbridge's package-private fakeClient.
type agentFakeClient struct {
	mu    sync.Mutex
	turns [][]ai.StreamChunk
	reqs  []ai.ChatRequest
	err   error
}

func (f *agentFakeClient) next(req ai.ChatRequest) []ai.StreamChunk {
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

func (f *agentFakeClient) ChatCompletion(_ context.Context, req *ai.ChatRequest) (*ai.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	var ch ai.ChatChoice
	for _, c := range f.next(*req) {
		ch.Message.Content += c.Content
		if c.FinishReason != "" {
			ch.FinishReason = c.FinishReason
		}
	}
	return &ai.ChatResponse{Choices: []ai.ChatChoice{ch}}, nil
}

func (f *agentFakeClient) StreamChatCompletion(_ context.Context, req ai.ChatRequest) (<-chan ai.StreamChunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	chunks := f.next(req)
	ch := make(chan ai.StreamChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (f *agentFakeClient) requests() []ai.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ai.ChatRequest(nil), f.reqs...)
}

func agentTextTurn(s string) []ai.StreamChunk {
	return []ai.StreamChunk{{Content: s}, {FinishReason: "stop", Done: true}}
}

// fakePrepare stands in for PrepareChatContext: each call returns the next
// scripted chunk set, rendered the way the production path renders it.
func fakePrepare(calls *[]ChatContextParams, sets ...[]vector.SearchChunk) func(context.Context, ChatContextParams) (*ChatContext, error) {
	return func(_ context.Context, p ChatContextParams) (*ChatContext, error) {
		*calls = append(*calls, p)
		var chunks []vector.SearchChunk
		if i := len(*calls) - 1; i < len(sets) {
			chunks = sets[i]
		}
		sources, text := buildChatSourcesAndContext(chunks)
		return &ChatContext{
			SystemPrompt: "SYSTEM FLOOR\n\nCONTEXT:\n" + text,
			Sources:      sources,
			Context:      text,
			FinalChunks:  chunks,
		}, nil
	}
}

var mensaChunks = []vector.SearchChunk{
	{ID: "c1", FileID: "f1", FileName: "mensa.pdf", Content: "Die Mensa öffnet um 11 Uhr.", Score: 0.9},
	{ID: "c2", FileID: "f2", FileName: "cafete.pdf", Content: "Die Cafeteria öffnet um 8 Uhr.", Score: 0.7},
}

func runAgentFlow(t *testing.T, deps AgentFlowDeps, sc adkbridge.Scope, q string) ([]*session.Event, error) {
	t.Helper()
	a, err := NewAgentFlow(deps)
	if err != nil {
		t.Fatalf("NewAgentFlow: %v", err)
	}
	r, err := runner.New(runner.Config{AppName: "agentchat", Agent: a, SessionService: session.InMemoryService(), AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	var evs []*session.Event
	for ev, err := range r.Run(adkbridge.WithScope(context.Background(), sc), "u1", "s1",
		genai.NewContentFromText(q, genai.RoleUser), agent.RunConfig{StreamingMode: agent.StreamingModeSSE}) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

func mustRunAgentFlow(t *testing.T, deps AgentFlowDeps, sc adkbridge.Scope, q string) []*session.Event {
	t.Helper()
	evs, err := runAgentFlow(t, deps, sc, q)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return evs
}

var viewScope = adkbridge.Scope{UserID: "u1", KBID: "kb1", Role: "view"}

func sourcesEvent(t *testing.T, evs []*session.Event) []ChatSource {
	t.Helper()
	for _, ev := range evs {
		c, ok := ev.CustomMetadata["agui.custom"].(map[string]any)
		if !ok || c["name"] != "justrag.sources.v1" {
			continue
		}
		raw, err := json.Marshal(c["value"])
		if err != nil {
			t.Fatal(err)
		}
		var out []ChatSource
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("sources payload is not []ChatSource: %v (%s)", err, raw)
		}
		return out
	}
	return nil
}

func outputsOf(evs []*session.Event) []string {
	var out []string
	for _, ev := range evs {
		if s, ok := ev.Output.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func newTestRetriever(calls *[]ChatContextParams, sets ...[]vector.SearchChunk) *AgentRetriever {
	r := &AgentRetriever{Lang: "de"}
	r.prepare = fakePrepare(calls, sets...)
	return r
}

// The de-risk test: the answer agent's instruction must be evaluated after
// the retrieve node ran, so it carries the floor prompt; and the node input
// (the question) must arrive as user content.
func TestInstructionProviderSeesFloorPrompt(t *testing.T) {
	var calls []ChatContextParams
	fc := &agentFakeClient{turns: [][]ai.StreamChunk{agentTextTurn("Um 11 Uhr [1].")}}
	deps := AgentFlowDeps{Model: adkbridge.NewModel(fc, "gemma"), Retriever: newTestRetriever(&calls, mensaChunks)}

	mustRunAgentFlow(t, deps, viewScope, "Wann öffnet die Mensa?")

	reqs := fc.requests()
	if len(reqs) != 1 {
		t.Fatalf("model requests = %d, want 1", len(reqs))
	}
	msgs := reqs[0].Messages
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "SYSTEM FLOOR") ||
		!strings.Contains(msgs[0].Content, "Die Mensa öffnet um 11 Uhr.") {
		t.Fatalf("system instruction lacks the floor prompt: %+v", msgs[0])
	}
	var users []string
	for _, m := range msgs {
		if m.Role == "user" {
			users = append(users, m.Content)
		}
	}
	if len(users) == 0 || users[len(users)-1] != "Wann öffnet die Mensa?" {
		t.Fatalf("question did not arrive as user content: %q", users)
	}
	if len(calls) != 1 || calls[0].KbID != "kb1" || calls[0].SearchQuery != "Wann öffnet die Mensa?" || calls[0].Language != "de" {
		t.Fatalf("floor retrieval params = %+v", calls)
	}
}

func TestFoundRoutesToAnswerWithSources(t *testing.T) {
	var calls []ChatContextParams
	fc := &agentFakeClient{turns: [][]ai.StreamChunk{agentTextTurn("Um 11 Uhr [1].")}}
	deps := AgentFlowDeps{Model: adkbridge.NewModel(fc, "gemma"), Retriever: newTestRetriever(&calls, mensaChunks)}

	evs := mustRunAgentFlow(t, deps, viewScope, "Wann öffnet die Mensa?")

	if text, _ := translate(t, evs); text != "Um 11 Uhr [1]." {
		t.Fatalf("answer text = %q", text)
	}
	src := sourcesEvent(t, evs)
	if len(src) != 2 || src[0].Index != 1 || src[1].Index != 2 || src[0].FileName != "mensa.pdf" {
		t.Fatalf("sources event = %+v", src)
	}
	var state map[string]any
	for _, ev := range evs {
		if ev.Actions.StateDelta["agentchat.question"] != nil {
			state = ev.Actions.StateDelta
		}
	}
	if state["agentchat.question"] != "Wann öffnet die Mensa?" || state["agentchat.reason"] != adkbridge.RouteFound {
		t.Fatalf("state delta = %v", state)
	}
}

func TestNoEvidenceRoutesToPlaceholderWithoutModelCall(t *testing.T) {
	var calls []ChatContextParams
	fc := &agentFakeClient{}
	deps := AgentFlowDeps{Model: adkbridge.NewModel(fc, "gemma"), Retriever: newTestRetriever(&calls)}

	evs := mustRunAgentFlow(t, deps, viewScope, "Budget 2027?")

	if n := len(fc.requests()); n != 0 {
		t.Fatalf("answer model called %d times on a dead end", n)
	}
	outs := outputsOf(evs)
	if len(outs) == 0 || outs[len(outs)-1] != deadEndNoEvidenceText {
		t.Fatalf("outputs = %q", outs)
	}
	reason := ""
	for _, ev := range evs {
		if r, ok := ev.Actions.StateDelta["agentchat.reason"].(string); ok {
			reason = r
		}
	}
	if reason != adkbridge.RouteNoEvidence {
		t.Fatalf("reason = %q", reason)
	}
}

func TestEmptyKBRoutesNoFilesBeforeRetrieval(t *testing.T) {
	var calls []ChatContextParams
	deps := AgentFlowDeps{
		Model:       adkbridge.NewModel(&agentFakeClient{}, "gemma"),
		Retriever:   newTestRetriever(&calls, mensaChunks),
		FileCounter: func(context.Context, string) (int, error) { return 0, nil },
	}
	evs := mustRunAgentFlow(t, deps, viewScope, "Mensa?")
	if len(calls) != 0 {
		t.Fatal("retrieval ran on a KB without files")
	}
	reason := ""
	for _, ev := range evs {
		if r, ok := ev.Actions.StateDelta["agentchat.reason"].(string); ok {
			reason = r
		}
	}
	if reason != adkbridge.RouteNoFiles {
		t.Fatalf("reason = %q", reason)
	}
}

func TestRetrieveEnforcesViewRole(t *testing.T) {
	var calls []ChatContextParams
	deps := AgentFlowDeps{Model: adkbridge.NewModel(&agentFakeClient{}, "gemma"), Retriever: newTestRetriever(&calls, mensaChunks)}
	_, err := runAgentFlow(t, deps, adkbridge.Scope{UserID: "u1", KBID: "kb1"}, "Mensa?")
	if !errors.Is(err, adkbridge.ErrForbiddenTool) {
		t.Fatalf("err = %v, want ErrForbiddenTool", err)
	}
	if len(calls) != 0 {
		t.Fatal("retrieval ran without a KB role")
	}
}

// A model failure surfaces as a runner error (the handler then persists the
// user message only — TestAnswerFailurePersistsUserMessageOnly, integration).
func TestAnswerFailureIsRunError(t *testing.T) {
	var calls []ChatContextParams
	fc := &agentFakeClient{err: errors.New("provider down")}
	deps := AgentFlowDeps{Model: adkbridge.NewModel(fc, "gemma"), Retriever: newTestRetriever(&calls, mensaChunks)}
	evs, err := runAgentFlow(t, deps, viewScope, "Mensa?")
	if err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("err = %v, want the model error", err)
	}
	if sourcesEvent(t, evs) != nil {
		t.Fatal("sources emitted for a failed answer")
	}
}

func TestRetrieverNumbersSourcesAcrossCalls(t *testing.T) {
	var calls []ChatContextParams
	second := []vector.SearchChunk{
		{ID: "c2", FileID: "f2", FileName: "cafete.pdf", Content: "Die Cafeteria öffnet um 8 Uhr.", Score: 0.8},
		{ID: "c3", FileID: "f3", FileName: "bib.pdf", Content: "Die Bibliothek öffnet um 9 Uhr.", Score: 0.6},
	}
	r := newTestRetriever(&calls, mensaChunks, second)

	first, err := r.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{"query":"Mensa"}`))
	if err != nil || len(first.Chunks) != 2 || !strings.Contains(first.Text, "[1]") {
		t.Fatalf("first = %+v, %v", first, err)
	}
	res, err := r.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{"query":"Bibliothek"}`))
	if err != nil {
		t.Fatal(err)
	}
	// c2 was already source [2]; c3 is new and continues the numbering.
	if !strings.Contains(res.Text, "[2]") || !strings.Contains(res.Text, "[3]") || strings.Contains(res.Text, "[1]") {
		t.Fatalf("follow-up context not renumbered:\n%s", res.Text)
	}
	got := r.Sources()
	if len(got) != 3 || got[2].Index != 3 || got[2].ChunkID != "c3" {
		t.Fatalf("sources = %+v", got)
	}
	if !strings.Contains(r.SystemPrompt(), "Die Mensa öffnet") || strings.Contains(r.SystemPrompt(), "Bibliothek") {
		t.Fatalf("system prompt must stay the floor prompt: %q", r.SystemPrompt())
	}
	if _, err := r.Dispatch(context.Background(), "kb1", "web_search", json.RawMessage(`{"query":"x"}`)); !errors.Is(err, mcp.ErrUnknownTool) {
		t.Fatalf("non-kb_search dispatch err = %v", err)
	}
}

func TestRetrieverAbstainYieldsNoChunks(t *testing.T) {
	r := &AgentRetriever{}
	r.prepare = func(context.Context, ChatContextParams) (*ChatContext, error) {
		sources, text := buildChatSourcesAndContext(mensaChunks)
		return &ChatContext{SystemPrompt: "ABSTAIN", Sources: sources, Context: text, FinalChunks: mensaChunks, Abstain: true}, nil
	}
	res, err := r.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{"query":"x"}`))
	if err != nil || len(res.Chunks) != 0 || len(r.Sources()) != 0 {
		t.Fatalf("abstain: chunks=%d sources=%d err=%v", len(res.Chunks), len(r.Sources()), err)
	}
}

func TestChatAgentChatEnabledDefaultOff(t *testing.T) {
	if ChatAgentChatEnabled(context.Background(), &fakeSiteConfigReader{values: map[string]*string{}}) {
		t.Fatal("missing key must default to false")
	}
	on := "true"
	if !ChatAgentChatEnabled(context.Background(), &fakeSiteConfigReader{values: map[string]*string{"chat_agent_chat_enabled": &on}}) {
		t.Fatal("explicit true not read")
	}
}

// translate runs the flow's events through the AG-UI Translator, as the
// handler does, and returns the client-visible text and the TEXT_MESSAGE
// contents in order.
func translate(t *testing.T, evs []*session.Event) (string, []string) {
	t.Helper()
	var contents []string
	tr, err := agui.NewTranslator("th", "run", func(e events.Event) error {
		if c, ok := e.(*events.TextMessageContentEvent); ok {
			contents = append(contents, c.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if err := tr.Event(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Finish(); err != nil {
		t.Fatal(err)
	}
	text, _, _ := tr.Output()
	return text, contents
}

func TestFoundPathReachesClientOnceThroughTranslator(t *testing.T) {
	var calls []ChatContextParams
	fc := &agentFakeClient{turns: [][]ai.StreamChunk{agentTextTurn("Um 11 Uhr [1].")}}
	deps := AgentFlowDeps{Model: adkbridge.NewModel(fc, "gemma"), Retriever: newTestRetriever(&calls, mensaChunks)}

	text, contents := translate(t, mustRunAgentFlow(t, deps, viewScope, "Wann öffnet die Mensa?"))

	if text != "Um 11 Uhr [1]." {
		t.Fatalf("client text = %q", text)
	}
	if strings.Join(contents, "") != "Um 11 Uhr [1]." {
		t.Fatalf("TEXT_MESSAGE contents = %q", contents)
	}
}

func TestDeadEndPathShowsPlaceholderThroughTranslator(t *testing.T) {
	var calls []ChatContextParams
	deps := AgentFlowDeps{Model: adkbridge.NewModel(&agentFakeClient{}, "gemma"), Retriever: newTestRetriever(&calls)}

	text, contents := translate(t, mustRunAgentFlow(t, deps, viewScope, "Budget 2027?"))

	if text != deadEndNoEvidenceText || strings.Join(contents, "") != deadEndNoEvidenceText {
		t.Fatalf("client text = %q, contents = %q", text, contents)
	}
}

func TestRetrieverFollowUpAbstainHasNoMarkers(t *testing.T) {
	var calls []ChatContextParams
	r := &AgentRetriever{}
	floor := fakePrepare(&calls, mensaChunks)
	r.prepare = func(ctx context.Context, p ChatContextParams) (*ChatContext, error) {
		cc, err := floor(ctx, p)
		if len(calls) > 1 {
			// Abstaining follow-up: chunks are rendered but not evidence.
			sources, text := buildChatSourcesAndContext(mensaChunks)
			cc = &ChatContext{SystemPrompt: "ABSTAIN", Sources: sources, Context: text, FinalChunks: mensaChunks, Abstain: true}
		}
		return cc, err
	}
	if _, err := r.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{"query":"Mensa"}`)); err != nil {
		t.Fatal(err)
	}
	res, err := r.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{"query":"Budget"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) != 0 || strings.Contains(res.Text, "[1]") || strings.Contains(res.Text, "[2]") || res.Text != agentNoEvidenceText {
		t.Fatalf("abstain result = %+v", res)
	}
	if got := r.Sources(); len(got) != 2 {
		t.Fatalf("sources changed on abstain: %+v", got)
	}
}

// Final review item 1 (Ruling P2-R16): every retrieval of the turn carries
// the KB prompt, the date line and the file-date lookup, like the legacy
// standard path.
func TestRetrieverParamsCarryKBPromptDateLineAndFileDates(t *testing.T) {
	var calls []ChatContextParams
	r := newTestRetriever(&calls, mensaChunks)
	r.KbSystemPrompt = "Du bist der Assistent des HRZ."
	r.CurrentDateLine = "Heute ist Donnerstag, der 8. Oktober 2026."
	r.FileDates = &fakeFileDates{}
	if _, err := r.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{"query":"Mensa"}`)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	p := calls[0]
	if p.KbSystemPrompt != r.KbSystemPrompt || p.CurrentDateLine != r.CurrentDateLine || p.FileDates == nil ||
		p.SearchQuery != "Mensa" || p.Language != "de" || p.KbID != "kb1" {
		t.Fatalf("floor params = %+v", p)
	}
}

// A follow-up's floor retrieval searches the condensed, standalone query;
// the dead end's web action and payload use it too. The answer model's own
// kb_search calls are never condensed.
func TestFloorQueryIsCondensed(t *testing.T) {
	var calls []ChatContextParams
	r := newTestRetriever(&calls) // finds nothing → dead end
	var condensed []string
	r.Condense = func(_ context.Context, q string) string {
		condensed = append(condensed, q)
		return "Hat die Mensa am Samstag geöffnet?"
	}
	deps := AgentFlowDeps{
		Model:       adkbridge.NewModel(&agentFakeClient{}, "gemma"),
		Retriever:   r,
		Allowed:     deadEndTools,
		ActDispatch: allDispatchers(),
	}
	req := pauseOf(newDeadEndHarness(t, deps, viewScope).ask("Und am Samstag?"))
	if len(condensed) != 1 || condensed[0] != "Und am Samstag?" {
		t.Fatalf("condenser calls = %q", condensed)
	}
	if len(calls) != 1 || calls[0].SearchQuery != "Hat die Mensa am Samstag geöffnet?" {
		t.Fatalf("floor query = %+v", calls)
	}
	if req == nil {
		t.Fatal("no pause")
	}
	p := req.Payload.(map[string]any)
	acts := p["actions"].([]adkbridge.Action)
	if p["query"] != "Hat die Mensa am Samstag geöffnet?" || acts[0].ID != "web" || acts[0].Args["query"] != "Hat die Mensa am Samstag geöffnet?" {
		t.Fatalf("payload = %#v", p)
	}

	// Direct (model) kb_search calls go through verbatim.
	if _, err := r.Dispatch(context.Background(), "kb1", "kb_search", json.RawMessage(`{"query":"Cafeteria"}`)); err != nil {
		t.Fatal(err)
	}
	if len(condensed) != 1 || calls[1].SearchQuery != "Cafeteria" {
		t.Fatalf("model kb_search condensed: %q / %+v", condensed, calls[1])
	}
}

func agentToolTurn(id, query string) []ai.StreamChunk {
	return []ai.StreamChunk{
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, ID: id, Name: "kb_search", Arguments: `{"query":"` + query + `"}`}}},
		{FinishReason: "tool_calls", Done: true},
	}
}

// Final review item 5: answer-time tool calls are capped per turn
// (chat_answer_tools_max_rounds). Past the cap a call gets a model-visible
// "budget exhausted" result instead of running, and the model is asked
// without tools, so it has to answer.
func TestAnswerToolCallsAreCappedPerTurn(t *testing.T) {
	var calls []ChatContextParams
	r := newTestRetriever(&calls, mensaChunks, mensaChunks, mensaChunks, mensaChunks)
	fc := &agentFakeClient{turns: [][]ai.StreamChunk{
		agentToolTurn("call-1", "Cafeteria"),
		agentToolTurn("call-2", "Bibliothek"),
		agentToolTurn("call-3", "Sporthalle"), // the model ignores the missing tools
		agentTextTurn("Um 11 Uhr [1]."),
	}}
	kbSearch := adkbridge.NewTool(adkbridge.ToolSpec{Name: "kb_search", Description: "search",
		InputSchema: json.RawMessage(agentKBSearchSchema), Policy: adkbridge.PolicyFor("kb_search")}, r.Dispatch)
	deps := AgentFlowDeps{Model: adkbridge.NewModel(fc, "gemma"), Retriever: r, Tools: []tool.Tool{kbSearch}, MaxToolCalls: 2}

	evs := mustRunAgentFlow(t, deps, viewScope, "Wann öffnet die Mensa?")

	if len(calls) != 3 { // the floor + two answer-time searches
		t.Fatalf("retrievals = %d, want 3: %+v", len(calls), calls)
	}
	reqs := fc.requests()
	if len(reqs) != 4 {
		t.Fatalf("model requests = %d, want 4", len(reqs))
	}
	for i, req := range reqs {
		if want := i < 2; (len(req.Tools) > 0) != want {
			t.Errorf("request %d declares tools %v, want tools=%v", i, req.Tools, want)
		}
	}
	var last strings.Builder
	for _, m := range reqs[3].Messages {
		last.WriteString(m.Content + "\n")
	}
	if !strings.Contains(last.String(), agentToolBudgetExhaustedText) {
		t.Fatalf("refused call's result not shown to the model:\n%s", last.String())
	}
	if got := outputsOf(evs); len(got) == 0 {
		t.Fatal("no answer")
	}
}

// MaxToolCalls 0 leaves the calls uncapped.
func TestAnswerToolCallsUncappedAtZero(t *testing.T) {
	var calls []ChatContextParams
	r := newTestRetriever(&calls, mensaChunks, mensaChunks, mensaChunks, mensaChunks)
	fc := &agentFakeClient{turns: [][]ai.StreamChunk{
		agentToolTurn("call-1", "a"), agentToolTurn("call-2", "b"), agentToolTurn("call-3", "c"), agentTextTurn("ok"),
	}}
	kbSearch := adkbridge.NewTool(adkbridge.ToolSpec{Name: "kb_search", Description: "search",
		InputSchema: json.RawMessage(agentKBSearchSchema), Policy: adkbridge.PolicyFor("kb_search")}, r.Dispatch)
	mustRunAgentFlow(t, AgentFlowDeps{Model: adkbridge.NewModel(fc, "gemma"), Retriever: r, Tools: []tool.Tool{kbSearch}}, viewScope, "q")
	if len(calls) != 4 {
		t.Fatalf("retrievals = %d, want 4", len(calls))
	}
}
