package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chatattach"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Call-site wiring coverage for the OrchComparison + teamSel != nil branch.
//
// Time-boxed per the fix-round-1 ruling: this drives tryDeepChat end to end
// (through the real RunComparisonChat + RunTeamChat + the post-dispatch SSE
// streamer) using ONLY seams that already exist elsewhere in this package —
// chatTestConfigStore + httptest (answer_tools_test.go, multipass_test.go),
// chatattach.NewInMemoryStore (comparison_chat_test.go), and a Search fake in
// the same shape as fakeSearcher (plan_execute_chat_test.go). No production
// code changed to make this possible.
// ---------------------------------------------------------------------------

// wiringSearcher routes by query string so the same fake can answer both the
// comparison stage's peer search (query = section text) and the team
// specialist's retrieval (query = params.Query) with different chunk sets —
// without needing to fake two different LLM response shapes to keep them
// apart. Returning zero chunks for the specialist retrieval also means
// runTeamSpecialist takes its "no evidence" canned-analysis branch, so the
// team run needs no second LLM call.
type wiringSearcher struct {
	byQuery map[string][]vector.SearchChunk
}

func (s wiringSearcher) Search(_ context.Context, _, query string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	return &vector.SearchResult{Chunks: s.byQuery[query]}, nil
}

func (s wiringSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

var _ vector.Searcher = wiringSearcher{}

// wiringConfigResolver spins up an httptest server that answers both the
// non-streaming structured calls (comparison findings extraction, follow-up
// questions) and the final streaming answer completion, using the same
// request-shape detection multipassTestServer/chatTestConfigStore already
// establish for this package's tests. A single canned "{"findings":[]}"
// non-stream body is valid input to every non-stream parser this wiring
// path exercises (comparisonFindingsPayload unmarshal; GenerateFollowUpQuestions'
// best-effort string-array parse, which no-ops to an empty slice on a shape
// mismatch instead of erroring).
func wiringConfigResolver(t *testing.T) *ai.ConfigResolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var probe struct {
			Stream bool `json:"stream"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &probe)
		if probe.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"summary text\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"}}]}`))
	}))
	t.Cleanup(srv.Close)
	store := &chatTestConfigStore{baseURL: srv.URL + "/v1/", model: "fake-model"}
	return ai.NewConfigResolver(store)
}

// wiringConfigResolverCapturing is wiringConfigResolver plus a raw-body
// recorder, used to tell RunAnswerWithTools' request shape (always sends
// tool_choice, even with an empty tool catalog — see answer_tools.go's
// runner.Run(ctx, messages, in.Tools, "auto")) apart from
// ai.StreamCompletionWithHistory's (never sets ToolChoice, so it is omitted
// by the json:"tool_choice,omitempty" tag).
func wiringConfigResolverCapturing(t *testing.T) (*ai.ConfigResolver, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		var probe struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"summary text\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"}}]}`))
	}))
	t.Cleanup(srv.Close)
	store := &chatTestConfigStore{baseURL: srv.URL + "/v1/", model: "fake-model"}
	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
	return ai.NewConfigResolver(store), snapshot
}

// noopToolDispatcher is a minimal ToolDispatcher — never actually invoked in
// the AnswerToolsStayOff test below (the mock model never returns tool_calls),
// present only so h.toolDispatcher != nil is satisfiable.
type noopToolDispatcher struct{}

func (noopToolDispatcher) Dispatch(context.Context, string, string, json.RawMessage) (DispatchedToolResult, error) {
	return DispatchedToolResult{}, nil
}

var _ ToolDispatcher = noopToolDispatcher{}

// wiringStore is a minimal Store that records every AddMessage call so the
// test can assert on the "ai" role message's TeamID/AgentID attribution.
type wiringStore struct {
	added []AddMessageParams
}

func (s *wiringStore) GetChats(context.Context, string, string) ([]ChatRow, error) { return nil, nil }
func (s *wiringStore) GetChatByID(context.Context, string) (*ChatRow, error)       { return nil, nil }
func (s *wiringStore) CreateChat(context.Context, string, string, string) (*ChatRow, error) {
	return nil, nil
}
func (s *wiringStore) DeleteChat(context.Context, string) error { return nil }
func (s *wiringStore) GetChatMessages(context.Context, string) ([]MessageRow, error) {
	return nil, nil
}
func (s *wiringStore) GetMessageAncestors(context.Context, string, string) ([]MessageRow, error) {
	return nil, nil
}
func (s *wiringStore) AddMessage(_ context.Context, p AddMessageParams) (*MessageRow, error) {
	s.added = append(s.added, p)
	return &MessageRow{ID: p.Role + "-msg-id", ChatID: p.ChatID, Role: p.Role, Content: p.Content}, nil
}
func (s *wiringStore) UpdateMessageFeedback(context.Context, string, string, string, *string, *string) error {
	return nil
}
func (s *wiringStore) UpdateMessageVerification(context.Context, string, *MessageVerification) error {
	return nil
}
func (s *wiringStore) UpdateMessageContent(context.Context, string, string) error { return nil }
func (s *wiringStore) UpdateMessageTraceID(context.Context, string, string) error { return nil }
func (s *wiringStore) GetKBSystemPrompt(context.Context, string) (*string, error) { return nil, nil }
func (s *wiringStore) UpdateChatAgentSelection(context.Context, string, *string, *string) error {
	return nil
}

var _ Store = (*wiringStore)(nil)

// wiringSiteConfig turns off every side quest tryDeepChat/runPostResponseTasks
// could otherwise take (factcheck's own LLM call, every other orchestrator
// flag) so the only two model round-trips in play are the comparison
// findings extraction and the final streamed summary — both served by
// wiringConfigResolver.
func wiringSiteConfig() *fakeSiteConfigReader {
	return &fakeSiteConfigReader{values: map[string]*string{
		"chat_compare_enabled": strPtr("true"),
		"factcheck_in_chat":    strPtr("false"),
	}}
}

func wiringHandler(t *testing.T, attachmentID string, store chatattach.Store) *Handler {
	t.Helper()
	return &Handler{
		store:            &wiringStore{},
		aiResolver:       wiringConfigResolver(t),
		siteConfigReader: wiringSiteConfig(),
		attachmentStore:  store,
		searchService: wiringSearcher{byQuery: map[string][]vector.SearchChunk{
			"Section A": {{ID: "c1", FileID: "f1", FileName: "f1.md", Content: "peer content"}},
		}},
	}
}

func wiringBody(attachmentID string) sendMessageRequest {
	return sendMessageRequest{
		Message:         "Bitte vergleichen",
		AttachmentID:    attachmentID,
		ComparisonModes: []string{"contradiction"},
	}
}

func wiringCtx(userID string) context.Context {
	return auth.WithUser(context.Background(), &auth.Claims{ID: userID, Role: "user"})
}

// TestTryDeepChat_ComparisonTeamSummary_Success drives the real OrchComparison
// + teamSel != nil branch: a resolved single-agent team selection must
// produce a summary RunTeamChat wrote, attribute the AI message to that
// team, and keep the comparison stage's peer chunk ("c1") in the final
// source list via mergeComparisonChunks.
func TestTryDeepChat_ComparisonTeamSummary_Success(t *testing.T) {
	attStore := chatattach.NewInMemoryStore(time.Hour)
	attID, err := attStore.Put(context.Background(), chatattach.Attachment{
		UserID: "u1", KbID: "kb1", Sections: []string{"Section A"},
	})
	if err != nil {
		t.Fatalf("attStore.Put: %v", err)
	}

	h := wiringHandler(t, attID, attStore)
	teamSel := &teamSelection{team: &agentteams.TeamForChat{
		Team:    agentteams.TeamRecord{ID: "t1", Name: "Vergleichsteam"},
		Members: []agentteams.AgentRecord{{ID: "m1", Name: "Prüfer"}},
	}}

	ctx := wiringCtx("u1")
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", nil)
	w := httptest.NewRecorder()

	handled := h.tryDeepChat(ctx, w, r, "chat1", "kb1", "de", "", "Bitte vergleichen", "",
		"", "", wiringBody(attID), nil, GraphTraversalDecision{}, nil, nil, nil, teamSel, "")
	if !handled {
		t.Fatalf("tryDeepChat did not handle the comparison-team turn; body: %s", w.Body.String())
	}

	ws := h.store.(*wiringStore)
	var aiMsg *AddMessageParams
	for i := range ws.added {
		if ws.added[i].Role == "ai" {
			aiMsg = &ws.added[i]
		}
	}
	if aiMsg == nil {
		t.Fatalf("no ai message recorded; body: %s", w.Body.String())
	}
	if aiMsg.TeamID == nil || *aiMsg.TeamID != "t1" {
		t.Errorf("ai message TeamID = %v, want t1 (comparison summary written by the selected team must be attributed)", aiMsg.TeamID)
	}
	foundC1 := false
	for _, src := range aiMsg.Sources {
		if src.ChunkID == "c1" {
			foundC1 = true
		}
	}
	if !foundC1 {
		t.Errorf("comparison stage's peer chunk c1 missing from the final Sources — mergeComparisonChunks did not reach the streamer: %+v", aiMsg.Sources)
	}
}

// TestTryDeepChat_ComparisonTeamSummary_TeamRunFails drives the fail-soft
// branch: a resolved team selection whose RunTeamChat call fails (here, a
// team with zero members — the same ErrTeamNoRoute path team_chat_test.go
// already exercises at the runTeamChatTestable level) must fall back to the
// plain summary prompt AND leave the turn unattributed.
func TestTryDeepChat_ComparisonTeamSummary_TeamRunFails(t *testing.T) {
	attStore := chatattach.NewInMemoryStore(time.Hour)
	attID, err := attStore.Put(context.Background(), chatattach.Attachment{
		UserID: "u1", KbID: "kb1", Sections: []string{"Section A"},
	})
	if err != nil {
		t.Fatalf("attStore.Put: %v", err)
	}

	h := wiringHandler(t, attID, attStore)
	// Zero members: RunTeamChat/runTeamChatTestable returns ErrTeamNoRoute
	// before any LLM call — a deterministic, already-tested failure mode,
	// not a synthetic error injected just for this test.
	teamSel := &teamSelection{team: &agentteams.TeamForChat{
		Team: agentteams.TeamRecord{ID: "t1", Name: "Leeres Team"},
	}}

	ctx := wiringCtx("u1")
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", nil)
	w := httptest.NewRecorder()

	handled := h.tryDeepChat(ctx, w, r, "chat1", "kb1", "de", "", "Bitte vergleichen", "",
		"", "", wiringBody(attID), nil, GraphTraversalDecision{}, nil, nil, nil, teamSel, "")
	if !handled {
		t.Fatalf("tryDeepChat did not handle the comparison turn; body: %s", w.Body.String())
	}

	if !strings.Contains(w.Body.String(), "team_unavailable") {
		t.Errorf("expected a team_unavailable trajectory event in the SSE stream; body: %s", w.Body.String())
	}

	ws := h.store.(*wiringStore)
	var aiMsg *AddMessageParams
	for i := range ws.added {
		if ws.added[i].Role == "ai" {
			aiMsg = &ws.added[i]
		}
	}
	if aiMsg == nil {
		t.Fatalf("no ai message recorded; body: %s", w.Body.String())
	}
	if aiMsg.TeamID != nil {
		t.Errorf("ai message TeamID = %v, want nil — a team whose run FAILED must not be attributed", *aiMsg.TeamID)
	}
}

// TestTryDeepChat_ComparisonTeamSummary_AnswerToolsStayOff covers fix-round-1
// concern B: a comparison turn whose summary a team wrote carries the same
// kind of team-synthesised, persona-influenced system-prompt content as a
// pure OrchTeam turn, and must be excluded from answer-time tools by the
// same useAnswerTools gate — not just "orch != OrchTeam". Answer-time tools
// are enabled and a dispatcher is wired, so if useAnswerTools regressed back
// to "orch != OrchTeam" (true for OrchComparison), RunAnswerWithTools would
// run instead of ai.StreamCompletionWithHistory. The two are told apart at
// the wire level: RunAnswerWithTools' runner always sends "tool_choice"
// (answer_tools.go's runner.Run(..., "auto")), even with an empty catalog;
// ai.StreamCompletionWithHistory never sets ToolChoice at all, so the
// omitempty tag drops it.
func TestTryDeepChat_ComparisonTeamSummary_AnswerToolsStayOff(t *testing.T) {
	attStore := chatattach.NewInMemoryStore(time.Hour)
	attID, err := attStore.Put(context.Background(), chatattach.Attachment{
		UserID: "u1", KbID: "kb1", Sections: []string{"Section A"},
	})
	if err != nil {
		t.Fatalf("attStore.Put: %v", err)
	}

	resolver, snapshot := wiringConfigResolverCapturing(t)
	h := &Handler{
		store:      &wiringStore{},
		aiResolver: resolver,
		siteConfigReader: &fakeSiteConfigReader{values: map[string]*string{
			"chat_compare_enabled":      strPtr("true"),
			"factcheck_in_chat":         strPtr("false"),
			"chat_answer_tools_enabled": strPtr("true"),
		}},
		attachmentStore: attStore,
		toolDispatcher:  noopToolDispatcher{},
		searchService: wiringSearcher{byQuery: map[string][]vector.SearchChunk{
			"Section A": {{ID: "c1", FileID: "f1", FileName: "f1.md", Content: "peer content"}},
		}},
	}
	teamSel := &teamSelection{team: &agentteams.TeamForChat{
		Team:    agentteams.TeamRecord{ID: "t1", Name: "Vergleichsteam"},
		Members: []agentteams.AgentRecord{{ID: "m1", Name: "Prüfer"}},
	}}

	ctx := wiringCtx("u1")
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", nil)
	w := httptest.NewRecorder()

	handled := h.tryDeepChat(ctx, w, r, "chat1", "kb1", "de", "", "Bitte vergleichen", "",
		"", "", wiringBody(attID), nil, GraphTraversalDecision{}, nil, nil, nil, teamSel, "")
	if !handled {
		t.Fatalf("tryDeepChat did not handle the comparison-team turn; body: %s", w.Body.String())
	}

	for _, body := range snapshot() {
		if strings.Contains(body, "tool_choice") {
			t.Errorf("a request carried tool_choice — RunAnswerWithTools ran on a team-authored comparison summary, which useAnswerTools must exclude: %s", body)
		}
	}
}

// TestTryDeepChat_ComparisonPlainPath_IgnoresKbSystemPrompt covers fix-round-1
// concern A: the plain (no team selected) comparison path must stay
// byte-identical to its pre-2026-08 behaviour (669ae5e) — it must NOT carry
// kbSystemPrompt, even though comparisonSummaryPromptFor is now shared with
// the team path. A future "helpful" re-merge of the two call sites (passing
// kbSystemPrompt on both, since it's right there as a parameter) must turn
// this red.
func TestTryDeepChat_ComparisonPlainPath_IgnoresKbSystemPrompt(t *testing.T) {
	attStore := chatattach.NewInMemoryStore(time.Hour)
	attID, err := attStore.Put(context.Background(), chatattach.Attachment{
		UserID: "u1", KbID: "kb1", Sections: []string{"Section A"},
	})
	if err != nil {
		t.Fatalf("attStore.Put: %v", err)
	}

	resolver, snapshot := wiringConfigResolverCapturing(t)
	h := &Handler{
		store:            &wiringStore{},
		aiResolver:       resolver,
		siteConfigReader: wiringSiteConfig(),
		attachmentStore:  attStore,
		searchService: wiringSearcher{byQuery: map[string][]vector.SearchChunk{
			"Section A": {{ID: "c1", FileID: "f1", FileName: "f1.md", Content: "peer content"}},
		}},
	}

	const sentinel = "SENTINEL-KB-SYSTEM-PROMPT-MARKER"
	ctx := wiringCtx("u1")
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", nil)
	w := httptest.NewRecorder()

	// teamSel is nil: no team/agent selected, so this is the plain path.
	handled := h.tryDeepChat(ctx, w, r, "chat1", "kb1", "de", "", "Bitte vergleichen", "",
		sentinel, "", wiringBody(attID), nil, GraphTraversalDecision{}, nil, nil, nil, nil, "")
	if !handled {
		t.Fatalf("tryDeepChat did not handle the comparison turn; body: %s", w.Body.String())
	}

	for _, body := range snapshot() {
		if strings.Contains(body, sentinel) {
			t.Errorf("plain comparison path leaked kbSystemPrompt into a model request — must stay byte-identical to pre-2026-08 behaviour: %s", body)
		}
	}
}
