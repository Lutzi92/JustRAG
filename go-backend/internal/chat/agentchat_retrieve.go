package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/vector"
)

// AgentRetriever runs the production retrieval (PrepareChatContext) as the
// bridge's kb_search and records the turn's sources for persistence. One
// per turn: the floor retrieval and every answer-time kb_search share its
// source numbering.
type AgentRetriever struct {
	AI     *ai.ConfigResolver
	Search vector.Searcher  // per-KB overlay clone
	Config SiteConfigReader // per-KB overlay
	Lang   string

	// The turn's prompt inputs, as the legacy standard path passes them to
	// PrepareChatContext (Ruling P2-R16): the KB's configured prompt, the
	// date line ("" when chat_date_awareness_enabled is off) and the
	// source-date lookup. Every retrieval of the turn carries them.
	KbSystemPrompt  string
	CurrentDateLine string
	FileDates       FileDateLookup // may be nil
	// Condense rewrites the floor question into a standalone query from the
	// thread's history (legacy CondenseFollowUp). Applied to the floor
	// retrieval only — the answer model's own kb_search queries are
	// already standalone. nil searches the question verbatim.
	Condense func(ctx context.Context, question string) string

	// prepare is PrepareChatContext; a seam for tests.
	prepare func(ctx context.Context, p ChatContextParams) (*ChatContext, error)

	mu      sync.Mutex
	sources []ChatSource // numbered across the whole turn
	prompt  string       // SystemPrompt of the FIRST (floor) retrieval
	called  bool
}

// Dispatch implements adkbridge.DispatchFunc for kb_search. Sources of a
// later call continue the turn's numbering, and the returned context text
// carries those numbers, so the model's [n] markers address Sources(). A
// chunk seen in an earlier call keeps its first number.
func (r *AgentRetriever) Dispatch(ctx context.Context, kbID, name string, args json.RawMessage) (mcp.ToolResult, error) {
	if name != "kb_search" {
		return mcp.ToolResult{}, fmt.Errorf("%w: %s", mcp.ErrUnknownTool, name)
	}
	var a struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ToolResult{}, fmt.Errorf("kb_search: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return mcp.ToolResult{}, errors.New("kb_search: query is required")
	}
	prepare := r.prepare
	if prepare == nil {
		prepare = func(ctx context.Context, p ChatContextParams) (*ChatContext, error) {
			return PrepareChatContext(ctx, r.AI, r.Search, r.Config, p)
		}
	}
	cc, err := prepare(ctx, ChatContextParams{
		KbID:            kbID,
		SearchQuery:     a.Query,
		Language:        r.Lang,
		KbSystemPrompt:  r.KbSystemPrompt,
		CurrentDateLine: r.CurrentDateLine,
		FileDates:       r.FileDates,
	})
	if err != nil {
		return mcp.ToolResult{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.called {
		r.called = true
		r.prompt = cc.SystemPrompt
	}
	if cc.Abstain || len(cc.FinalChunks) == 0 {
		// No evidence: no chunks (the flow routes no_evidence), nothing to
		// cite, and no rendered context — its [n] markers would address
		// the turn's existing sources.
		return mcp.ToolResult{Text: agentNoEvidenceText}, nil
	}
	return mcp.ToolResult{Text: r.number(cc), Chunks: resultChunks(cc.FinalChunks)}, nil
}

// number appends cc's sources to the turn and returns the context text
// with the turn's numbers. Caller holds r.mu.
func (r *AgentRetriever) number(cc *ChatContext) string {
	if len(cc.Sources) != len(cc.FinalChunks) {
		// Not the 1:1 projection buildChatSourcesAndContext produces;
		// renumbering would misattribute, so keep the text as rendered
		// and only extend the list. Unreachable on the flat path today.
		for _, s := range cc.Sources {
			s.Index = len(r.sources) + 1
			r.sources = append(r.sources, s)
		}
		return cc.Context
	}
	parts := make([]string, len(cc.FinalChunks))
	for i, c := range cc.FinalChunks {
		idx := r.indexOf(cc.Sources[i].ChunkID)
		if idx == 0 {
			s := cc.Sources[i]
			s.Index = len(r.sources) + 1
			r.sources = append(r.sources, s)
			idx = s.Index
		}
		annotation, _ := renderChunkAnnotation(idx, c)
		parts[i] = annotation + "\n" + c.Content
	}
	return strings.Join(parts, chunkBlockSeparator)
}

// indexOf returns the turn number of chunkID, 0 if unseen. Caller holds r.mu.
func (r *AgentRetriever) indexOf(chunkID string) int {
	if chunkID == "" {
		return 0
	}
	if i := slices.IndexFunc(r.sources, func(s ChatSource) bool { return s.ChunkID == chunkID }); i >= 0 {
		return r.sources[i].Index
	}
	return 0
}

// floorQuery is the query the floor retrieval searches: the condensed
// question when a condenser is set and returns text, else the question.
func (r *AgentRetriever) floorQuery(ctx context.Context, q string) string {
	if r.Condense == nil {
		return q
	}
	if c := strings.TrimSpace(r.Condense(ctx, q)); c != "" {
		return c
	}
	return q
}

// Sources returns the turn's numbered sources so far.
func (r *AgentRetriever) Sources() []ChatSource {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sources)
}

// SystemPrompt returns the floor retrieval's system prompt (with its
// numbered CONTEXT block), "" before the first retrieval.
func (r *AgentRetriever) SystemPrompt() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompt
}

func resultChunks(chunks []vector.SearchChunk) []mcp.ResultChunk {
	out := make([]mcp.ResultChunk, len(chunks))
	for i, c := range chunks {
		out[i] = mcp.ResultChunk{ID: c.ID, FileID: c.FileID, FileName: c.FileName, Content: c.Content, Score: c.Score}
	}
	return out
}
