package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/vector"
)

// errSearcherUnconfigured is returned when an agent is invoked without
// a wired SearchInvoker. Production wiring sets one; this surfaces
// programming errors visibly instead of producing zero-result chunks
// the orchestrator would otherwise treat as "the KB has nothing".
var errSearcherUnconfigured = errors.New("agents: searcher unconfigured")

// EnumerationClassifier is the chat-side IsEnumerationQuery
// shape. Defined as a tiny interface here so the EnumeratorAgent
// doesn't import the chat package (which would cycle: chat imports
// agents → agents imports chat).
type EnumerationClassifier func(query, lang string) bool

// EnumeratorAgent handles list / aggregate questions ("Welche
// Projekte nutzen X?", "Liste alle …"). v1 keeps the existing
// retrieval path — one SearchService call with the supplied query —
// and delegates the structured-extraction completeness pass to the
// chat orchestrator's existing enumeration-prepass machinery. The
// agent's distinct value here is route signaling: when the supervisor
// dispatches to this specialist, the trajectory event records
// "enumerator" so operators can see how often enumeration intent is
// being correctly identified.
//
// A future iteration can lift the structured-extraction pre-pass into
// this agent's body so the enumeration completeness contract is owned
// at this layer; v1 keeps the integration surface small.
type EnumeratorAgent struct {
	Searcher      SearchInvoker
	PlanningModel string
	// Classifier is the routing rule helper. The supervisor consults
	// it before dispatch; the specialist itself doesn't re-classify.
	// It's exposed on the struct so the supervisor's stub-driven tests
	// can inject deterministic intent.
	Classifier EnumerationClassifier
}

// NewEnumeratorAgent wires the dependencies the agent needs.
func NewEnumeratorAgent(s SearchInvoker, planningModel string, c EnumerationClassifier) *EnumeratorAgent {
	return &EnumeratorAgent{Searcher: s, PlanningModel: planningModel, Classifier: c}
}

// Name satisfies Agent.
func (e *EnumeratorAgent) Name() string { return "enumerator" }

// Execute satisfies Agent. The implementation is deliberately the
// same retrieval shape as RetrieverAgent — the routing-layer
// distinction is what matters in v1; specialization grows here in
// follow-ups.
func (e *EnumeratorAgent) Execute(ctx context.Context, in Input) (Output, error) {
	if e == nil || e.Searcher == nil {
		return Output{}, errSearcherUnconfigured
	}
	opts := vector.SearchOptions{
		FileIDs:       in.FileIDs,
		ModelOverride: e.PlanningModel,
		QueryType:     vector.QueryTypeEnumeration,
		GraphChunkIDs: in.GraphChunkIDs,
		HyPESearch:    in.HyPESearch,
	}
	res, err := e.Searcher.Search(ctx, in.KbID, in.Query, 0, opts)
	if err != nil {
		return Output{}, fmt.Errorf("enumerator: search: %w", err)
	}
	// Surface a small Notes line so the synthesizer's prompt receives
	// an explicit "this came from the enumerator" signal — useful when
	// the operator inspects the trajectory panel.
	notes := "enumerator: dispatched on enumeration intent"
	if e.Classifier != nil && !e.Classifier(in.Query, in.Language) {
		notes = strings.Join([]string{notes, "(intent reclassified by supervisor)"}, " ")
	}
	return Output{Chunks: res.Chunks, Notes: notes}, nil
}
