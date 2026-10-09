package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"

	"github.com/justrag/go-backend/internal/adkbridge"
	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/sessionmem"
)

func TestAgentChatPropsOf(t *testing.T) {
	cases := []struct {
		body            string
		reasoning, lang string
	}{
		{`{"forwardedProps":{"reasoning":"high","language":"en"}}`, "high", "en"},
		{`{"forwarded_props":{"reasoning":"low"}}`, "low", "de"},
		{`{"forwardedProps":{"reasoning":"max","language":"fr"}}`, "", "de"},
		{`{"forwardedProps":{"reasoning":true}}`, "", "de"},
		{`{"forwardedProps":"medium"}`, "", "de"},
		{`{}`, "", "de"},
	}
	for _, c := range cases {
		var in types.RunAgentInput
		if err := json.Unmarshal([]byte(c.body), &in); err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		got := agentChatPropsOf(&in)
		if got.reasoning != c.reasoning || got.language != c.lang {
			t.Errorf("%s: got %+v, want reasoning %q language %q", c.body, got, c.reasoning, c.lang)
		}
	}
}

// Ruling P2-R15: the answer agent gets only approval-free tools. A tool
// confirmation inside the workflow's answer node cannot be resumed (ADK
// v2.5.0), so web_search — and any approval-always or unknown tool — must
// not be offered even when registered.
func TestAnswerToolsAreApprovalFree(t *testing.T) {
	reg := mcp.NewRegistry()
	noop := mcp.ToolHandlerFunc(func(context.Context, json.RawMessage) (mcp.ToolResult, error) { return mcp.ToolResult{}, nil })
	schema := json.RawMessage(`{"type":"object"}`)
	for _, n := range []string{"web_search", "memory_read", "memory_write"} {
		reg.RegisterBuiltin(mcp.Tool{Name: n, Description: n, InputSchema: schema, Handler: noop})
	}
	h := NewAgentChatHandler(AgentChatDeps{Registry: reg, SessionMemory: sessionmem.NewInMemoryStore()})
	reader := mapSiteConfig{"chat_session_memory_enabled": "true"}
	var got []string
	for _, tl := range h.answerTools(context.Background(), "kb1", reader, &AgentRetriever{}) {
		got = append(got, tl.Name())
		if p := adkbridge.PolicyFor(tl.Name()); p.Approval != adkbridge.ApprovalNever {
			t.Errorf("answer tool %s needs approval %s", tl.Name(), p.Approval)
		}
	}
	if strings.Join(got, ",") != "kb_search,memory_read,memory_write" {
		t.Fatalf("answer tools = %v, want kb_search,memory_read,memory_write", got)
	}
}

type mapSiteConfig map[string]string

func (m mapSiteConfig) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := m[key]; ok {
		return &v, nil
	}
	return nil, nil
}

// Final review item 1: the handler builds the turn's retriever with the
// KB's prompt, the overlay's date line, the file-date lookup and a
// condenser over the thread's persisted history.
func TestNewRetrieverWiresLegacyPromptInputs(t *testing.T) {
	var condensed []string
	h := NewAgentChatHandler(AgentChatDeps{
		FileDates: &fakeFileDates{},
		kbPrompt: func(_ context.Context, kbID string) (*string, error) {
			s := "KB-PROMPT " + kbID
			return &s, nil
		},
		condense: func(_ context.Context, chatID string, parent *string, q, kbID, lang string) (string, error) {
			condensed = append(condensed, strings.Join([]string{chatID, *parent, q, kbID, lang}, "|"))
			return "Hat die Mensa am Samstag geöffnet?", nil
		},
	})
	hooks := &agentChatHooks{}
	r := h.newRetriever(context.Background(), "kb1", mapSiteConfig{}, nil, "en", hooks)
	if r.KbSystemPrompt != "KB-PROMPT kb1" || r.CurrentDateLine == "" || r.FileDates == nil || r.Lang != "en" {
		t.Fatalf("retriever = %+v", r)
	}
	if off := h.newRetriever(context.Background(), "kb1", mapSiteConfig{"chat_date_awareness_enabled": "false"}, nil, "de", hooks); off.CurrentDateLine != "" {
		t.Fatalf("date line with date awareness off = %q", off.CurrentDateLine)
	}

	// A new thread has no history: no condense call.
	if got := r.floorQuery(context.Background(), "Und am Samstag?"); got != "Und am Samstag?" || len(condensed) != 0 {
		t.Fatalf("new thread: query %q, condense calls %q", got, condensed)
	}
	// A follow-up condenses over the history before this turn's question.
	parent := "m9"
	hooks.threadID, hooks.parentMsgID = "c1", &parent
	if got := r.floorQuery(context.Background(), "Und am Samstag?"); got != "Hat die Mensa am Samstag geöffnet?" {
		t.Fatalf("follow-up query = %q", got)
	}
	if len(condensed) != 1 || condensed[0] != "c1|m9|Und am Samstag?|kb1|en" {
		t.Fatalf("condense calls = %q", condensed)
	}
}

// storeUserMessage remembers the thread and the message this turn's
// question replies to (the condenser's history anchor).
func TestHooksRememberHistoryAnchor(t *testing.T) {
	st := &hooksStore{last: "m9"}
	h := &agentChatHooks{store: st}
	if _, err := h.ResolveThread(context.Background(), adkbridge.Scope{UserID: "u1", KBID: "kb1"}, "8d1c4b3e-0f5a-4d7e-9a61-2b7c3d4e5f60", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.storeUserMessage(context.Background(), adkbridge.Scope{UserID: "u1", KBID: "kb1"}, "8d1c4b3e-0f5a-4d7e-9a61-2b7c3d4e5f60", "Und am Samstag?"); err != nil {
		t.Fatal(err)
	}
	if h.threadID != "8d1c4b3e-0f5a-4d7e-9a61-2b7c3d4e5f60" || h.parentMsgID == nil || *h.parentMsgID != "m9" {
		t.Fatalf("thread %q parent %v", h.threadID, h.parentMsgID)
	}
}

type hooksStore struct{ last string }

func (s *hooksStore) GetChatByID(_ context.Context, id string) (*ChatRow, error) {
	return &ChatRow{ID: id, UserID: "u1", KbID: "kb1"}, nil
}
func (s *hooksStore) CreateChatWithID(context.Context, string, string, string, string) (*ChatRow, error) {
	return &ChatRow{}, nil
}
func (s *hooksStore) DeleteChat(context.Context, string) error { return nil }
func (s *hooksStore) LastMessageID(context.Context, string) (*string, error) {
	return &s.last, nil
}
func (s *hooksStore) AddMessage(context.Context, AddMessageParams) (*MessageRow, error) {
	return &MessageRow{ID: "m10"}, nil
}

type fakeConfluenceConns struct {
	conn bool
	err  error
}

func (f fakeConfluenceConns) GetConfluenceConnectionByUserID(context.Context, string) (*confluence.ConfluenceConnectionRow, error) {
	if f.err != nil || !f.conn {
		return nil, f.err
	}
	return &confluence.ConfluenceConnectionRow{ID: "conn1"}, nil
}

// Final review item 2: web is offered only when web search is enabled AND
// configured (the settings research.WebClient reads); confluence only when
// Confluence is enabled AND the user has a connection.
func TestActionAvailability(t *testing.T) {
	sc := adkbridge.Scope{UserID: "u1", KBID: "kb1", Role: "edit"}
	web := mapSiteConfig{"web_search_enabled": "true", "google_search_api_key": "k", "google_search_cx": "cx"}
	for _, tc := range []struct {
		name  string
		cfg   mapSiteConfig
		conns ConfluenceConnections
		tool  string
		want  bool
	}{
		{"web configured", web, nil, "web_search", true},
		{"web disabled", mapSiteConfig{"google_search_api_key": "k", "google_search_cx": "cx"}, nil, "web_search", false},
		{"web without key", mapSiteConfig{"web_search_enabled": "true", "google_search_cx": "cx"}, nil, "web_search", false},
		{"web without cx", mapSiteConfig{"web_search_enabled": "true", "google_search_api_key": "k"}, nil, "web_search", false},
		{"confluence ok", mapSiteConfig{"confluence_enabled": "true"}, fakeConfluenceConns{conn: true}, "confluence_import", true},
		{"confluence disabled", mapSiteConfig{}, fakeConfluenceConns{conn: true}, "confluence_import", false},
		{"confluence no connection", mapSiteConfig{"confluence_enabled": "true"}, fakeConfluenceConns{}, "confluence_import", false},
		{"confluence lookup error", mapSiteConfig{"confluence_enabled": "true"}, fakeConfluenceConns{err: errors.New("db")}, "confluence_import", false},
		{"confluence no store", mapSiteConfig{"confluence_enabled": "true"}, nil, "confluence_import", false},
		{"library", mapSiteConfig{}, nil, "library_add_to_kb", true},
	} {
		h := NewAgentChatHandler(AgentChatDeps{SiteConfig: tc.cfg, ConfluenceConns: tc.conns})
		if got := h.actionAvailable(context.Background(), sc, tc.tool); got != tc.want {
			t.Errorf("%s: available = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Final review item 5: the turn's limits come from the legacy readers on
// the KB overlay — chat_answer_tools_max_rounds caps the answer agent's tool
// calls (default 5), chat_turn_budget_seconds bounds the run (0 = none).
func TestAgentTurnLimits(t *testing.T) {
	for _, tc := range []struct {
		cfg      mapSiteConfig
		calls    int
		deadline time.Duration
	}{
		{mapSiteConfig{}, 5, 0},
		{mapSiteConfig{"chat_answer_tools_max_rounds": "2", "chat_turn_budget_seconds": "30"}, 2, 30 * time.Second},
		{mapSiteConfig{"chat_answer_tools_max_rounds": "99", "chat_turn_budget_seconds": "-1"}, 5, 0},
	} {
		calls, deadline := agentTurnLimits(context.Background(), tc.cfg)
		if calls != tc.calls || deadline != tc.deadline {
			t.Errorf("%v: limits = %d, %v; want %d, %v", tc.cfg, calls, deadline, tc.calls, tc.deadline)
		}
	}
}
