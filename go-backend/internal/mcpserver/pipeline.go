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
}

// NewPipelineAnswerer builds the production Answerer. It runs the real
// site-config-driven RAG pipeline (CRAG / enumeration / contextual prefix /
// sufficient-context gate / citation validation via PrepareChatContext) and
// a single non-streaming completion. Stateless: no chat record is created.
func NewPipelineAnswerer(aiResolver *ai.ConfigResolver, searchSvc vector.Searcher, cfg chat.SiteConfigReader, promptStore KBPromptReader) Answerer {
	return &pipelineAnswerer{aiResolver: aiResolver, searchSvc: searchSvc, cfg: cfg, promptStore: promptStore}
}

func (p *pipelineAnswerer) Answer(ctx context.Context, kbID, question, language string) (AnswerResult, error) {
	var kbSystemPrompt string
	if sp, err := p.promptStore.GetKBSystemPrompt(ctx, kbID); err == nil && sp != nil {
		kbSystemPrompt = *sp
	}

	chatCtx, err := chat.PrepareChatContext(ctx, p.aiResolver, p.searchSvc, p.cfg, chat.ChatContextParams{
		KbID:           kbID,
		SearchQuery:    question, // stateless: the question IS the query (no follow-up condense)
		Language:       language,
		KbSystemPrompt: kbSystemPrompt,
		QueryType:      vector.QueryTypeComplexReasoning,
	})
	if err != nil {
		return AnswerResult{}, fmt.Errorf("prepare context: %w", err)
	}

	// chatCtx.SystemPrompt already embeds the retrieved chunks; question is the raw user turn.
	completion, err := ai.GenerateCompletion(ctx, p.aiResolver, question, chatCtx.SystemPrompt, kbID, false)
	if err != nil {
		return AnswerResult{}, fmt.Errorf("generate completion: %w", err)
	}

	return AnswerResult{
		Answer:  completion.Content,
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
		})
	}
	return out
}
