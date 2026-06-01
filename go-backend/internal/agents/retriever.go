package agents

import (
	"context"

	"github.com/justrag/go-backend/internal/vector"
)

// SearchInvoker is the minimal SearchService surface RetrieverAgent
// needs. Defining it here lets tests inject fakes without importing
// the full vector dependency chain.
type SearchInvoker interface {
	Search(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error)
}

// RetrieverAgent answers focused factual sub-questions by issuing one
// vector search and returning the chunks. It is the default specialist
// for non-enumeration queries; the Supervisor dispatches anything that
// IsEnumerationQuery does NOT classify as a list/aggregate intent here.
//
// PlanningModel is the optional model override forwarded into
// SearchOptions so HyDE / multi-query / step-back inherit the
// orchestrator's planning model rather than the KB's default chat
// model. Empty preserves KB defaults.
type RetrieverAgent struct {
	Searcher      SearchInvoker
	PlanningModel string
}

// NewRetrieverAgent is the constructor — exposed mostly for tests
// that want a literal-struct alternative.
func NewRetrieverAgent(s SearchInvoker, planningModel string) *RetrieverAgent {
	return &RetrieverAgent{Searcher: s, PlanningModel: planningModel}
}

// Name satisfies Agent.
func (r *RetrieverAgent) Name() string { return "retriever" }

// Execute satisfies Agent. Returns the search result's chunks; an
// error from the searcher propagates up so the supervisor can log
// `rag_agent_dispatch_total{specialist=retriever, status=error}`.
func (r *RetrieverAgent) Execute(ctx context.Context, in Input) (Output, error) {
	if r == nil || r.Searcher == nil {
		return Output{}, errSearcherUnconfigured
	}
	opts := vector.SearchOptions{
		FileIDs:       in.FileIDs,
		ModelOverride: r.PlanningModel,
		QueryType:     vector.QueryTypeComplexReasoning,
		GraphChunkIDs: in.GraphChunkIDs,
		HyPESearch:    in.HyPESearch,
	}
	res, err := r.Searcher.Search(ctx, in.KbID, in.Query, 0, opts)
	if err != nil {
		return Output{}, err
	}
	return Output{Chunks: res.Chunks}, nil
}
