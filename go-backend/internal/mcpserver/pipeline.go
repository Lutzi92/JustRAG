package mcpserver

import (
	"context"
	"fmt"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

// KBPromptReader fetches a KB's custom system prompt (may be nil/absent).
type KBPromptReader interface {
	GetKBSystemPrompt(ctx context.Context, kbID string) (*string, error)
}

type pipelineAnswerer struct {
	aiResolver  *ai.ConfigResolver
	searchSvc   vector.Searcher
	cfg         chat.SiteConfigReader
	promptStore KBPromptReader
	fileDates   chat.FileDateLookup // optional; see WithFileDates
}

// PipelineOption configures the production Answerer.
type PipelineOption func(*pipelineAnswerer)

// WithFileDates injects the per-turn source-date lookup used to stamp
// createdAt/publishedAt onto the sources ask_kb returns. Optional — without
// it the date fields are simply omitted, exactly as before the freshness
// surface existed.
func WithFileDates(l chat.FileDateLookup) PipelineOption {
	return func(p *pipelineAnswerer) { p.fileDates = l }
}

// contextParams builds the chat.ChatContextParams for PrepareChatContext.
// Unlike publicapi/openaicompat, this surface passes a real p.cfg
// site-config reader below, so FileDates here is what lets the conflict /
// supersession detector render real date lines instead of "unknown" on
// ask_kb turns (W6-R2).
func (p *pipelineAnswerer) contextParams(kbID, question, language, kbSystemPrompt string) chat.ChatContextParams {
	return chat.ChatContextParams{
		KbID:           kbID,
		SearchQuery:    question, // stateless: the question IS the query (no follow-up condense)
		Language:       language,
		KbSystemPrompt: kbSystemPrompt,
		QueryType:      vector.QueryTypeComplexReasoning,
		FileDates:      p.fileDates,
	}
}

// NewPipelineAnswerer builds the production Answerer. It runs the real
// site-config-driven RAG pipeline (CRAG / enumeration / contextual prefix /
// sufficient-context gate / citation validation via PrepareChatContext) and
// a single non-streaming completion. Stateless: no chat record is created.
func NewPipelineAnswerer(aiResolver *ai.ConfigResolver, searchSvc vector.Searcher, cfg chat.SiteConfigReader, promptStore KBPromptReader, opts ...PipelineOption) Answerer {
	p := &pipelineAnswerer{aiResolver: aiResolver, searchSvc: searchSvc, cfg: cfg, promptStore: promptStore}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *pipelineAnswerer) Answer(ctx context.Context, kbID, question, language string) (AnswerResult, error) {
	var kbSystemPrompt string
	if sp, err := p.promptStore.GetKBSystemPrompt(ctx, kbID); err == nil && sp != nil {
		kbSystemPrompt = *sp
	}

	chatCtx, err := chat.PrepareChatContext(ctx, p.aiResolver, p.searchSvc, p.cfg, p.contextParams(kbID, question, language, kbSystemPrompt))
	if err != nil {
		return AnswerResult{}, fmt.Errorf("prepare context: %w", err)
	}

	// Freshness dates for the cited files (one batch query, fail-soft) —
	// the same enrichment the web chat, public-API and OpenAI-compat paths
	// do, applied before the sources are projected onto the tool result.
	chat.EnrichSourceDates(ctx, p.fileDates, chatCtx.Sources)

	// chatCtx.SystemPrompt already embeds the retrieved chunks; question is the raw user turn.
	completion, err := ai.GenerateCompletion(ctx, p.aiResolver, question, chatCtx.SystemPrompt, kbID, false)
	if err != nil {
		return AnswerResult{}, fmt.Errorf("generate completion: %w", err)
	}

	// Degenerate-run guard, post hoc (W5-R4): this surface is non-streaming,
	// so there is nothing to abort — but a runaway repetition must not be
	// handed to the MCP client as the answer.
	answer, _ := chat.GuardAnswerText(ctx, p.cfg, completion.Content, language, "mcp")

	return AnswerResult{
		Answer:  answer,
		Sources: mapSources(chatCtx.Sources),
	}, nil
}

func mapSources(in []chat.ChatSource) []Source {
	out := make([]Source, 0, len(in))
	for _, s := range in {
		out = append(out, Source{
			Index:    s.Index,
			FileID:   s.FileID,
			FileName: s.FileName,
			Score:    s.Score,

			CreatedAt:   chat.FormatSourceDate(s.CreatedAt),
			PublishedAt: chat.FormatSourceDate(s.PublishedAt),
		})
	}
	return out
}
