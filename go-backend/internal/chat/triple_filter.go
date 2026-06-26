package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/logctx"
)

// tripleFilterResult is the strict structured-output contract for the
// HippoRAG-2 recognition-memory pass.
type tripleFilterResult struct {
	KeepSeedIDs       []int64 `json:"keepSeedIds"`
	RelevantTripleIDs []int   `json:"relevantTripleIds"`
}

// tripleFilterSpec is the json_schema for the filter call.
func tripleFilterSpec() *ai.StructuredSpec {
	return &ai.StructuredSpec{
		Name: "ppr_triple_filter",
		Schema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"keepSeedIds":{"type":"array","items":{"type":"integer"}},
				"relevantTripleIds":{"type":"array","items":{"type":"integer"}}
			},
			"required":["keepSeedIds","relevantTripleIds"],
			"additionalProperties":false
		}`),
	}
}

// applyTripleFilterResult is the pure prune+expand logic. Keeps matched
// entities whose id is in keepSeedIDs (prune); for each relevant triple index,
// adds the triple endpoints not already present (expand). If the kept set is
// empty, falls back to the full matched set (PPR must never run seedless).
func applyTripleFilterResult(matched []kg.Entity, triples []kg.IncidentTriple, keepSeedIDs []int64, relevantTripleIdx []int) []kg.Entity {
	keep := make(map[int64]bool, len(keepSeedIDs))
	for _, id := range keepSeedIDs {
		keep[id] = true
	}
	byID := make(map[int64]kg.Entity, len(matched))
	for _, e := range matched {
		byID[e.ID] = e
	}

	out := make([]kg.Entity, 0, len(matched)+len(relevantTripleIdx))
	added := make(map[int64]bool)
	add := func(e kg.Entity) {
		if e.ID == 0 || added[e.ID] {
			return
		}
		added[e.ID] = true
		out = append(out, e)
	}
	for _, e := range matched {
		if keep[e.ID] {
			add(e)
		}
	}
	for _, idx := range relevantTripleIdx {
		if idx < 0 || idx >= len(triples) {
			continue
		}
		tr := triples[idx]
		if _, isMatched := byID[tr.SrcID]; !isMatched {
			add(kg.Entity{ID: tr.SrcID, CanonicalName: tr.SrcName})
		}
		if _, isMatched := byID[tr.DstID]; !isMatched {
			add(kg.Entity{ID: tr.DstID, CanonicalName: tr.DstName})
		}
	}
	if len(out) == 0 {
		return matched
	}
	return out
}

const tripleFilterSystemPrompt = "You refine a knowledge-graph seed set for retrieval. " +
	"Given a user question, candidate seed entities, and candidate triples, " +
	"return which seed ids are relevant to the question (keepSeedIds) and which " +
	"triple ids point to entities worth adding (relevantTripleIds). Be selective."

func buildTripleFilterPrompt(query string, matched []kg.Entity, triples []kg.IncidentTriple) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nSeed entities:\n", query)
	for _, e := range matched {
		fmt.Fprintf(&b, "- id=%d %s\n", e.ID, e.CanonicalName)
	}
	b.WriteString("\nCandidate triples (index: src -[rel]-> dst):\n")
	for i, tr := range triples {
		fmt.Fprintf(&b, "%d: %s -[%s]-> %s\n", i, tr.SrcName, tr.Rel, tr.DstName)
	}
	b.WriteString("\nReturn keepSeedIds (subset of seed ids) and relevantTripleIds (subset of triple indices).")
	return b.String()
}

// refineSeedsWithTripleFilter runs the best-effort HippoRAG-2 recognition
// memory: gather the seeds' 1-hop triples, ask a fast-tier LLM which seeds to
// keep and which triples are relevant, then prune+expand. ANY failure returns
// the original matched seeds unchanged.
func refineSeedsWithTripleFilter(ctx context.Context, fn structuredFn, store kg.Store, kbID, query, model string, maxTriples int, matched []kg.Entity) []kg.Entity {
	if len(matched) == 0 || store == nil || fn == nil {
		return matched
	}
	ids := make([]int64, len(matched))
	for i, e := range matched {
		ids[i] = e.ID
	}
	triples, err := store.IncidentTriples(ctx, kbID, ids, maxTriples)
	if err != nil || len(triples) == 0 {
		if err != nil {
			logctx.From(ctx).Warn("triple_filter: incident triples failed; using matched seeds", "error", err, "kb_id", kbID)
		}
		return matched
	}
	prompt := buildTripleFilterPrompt(query, matched, triples)
	raw, err := fn(ctx, prompt, tripleFilterSystemPrompt, kbID, model, tripleFilterSpec())
	if err != nil {
		logctx.From(ctx).Warn("triple_filter: llm call failed; using matched seeds", "error", err, "kb_id", kbID)
		return matched
	}
	var res tripleFilterResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &res); err != nil {
		logctx.From(ctx).Warn("triple_filter: unparseable output; using matched seeds", "error", err, "kb_id", kbID)
		return matched
	}
	return applyTripleFilterResult(matched, triples, res.KeepSeedIDs, res.RelevantTripleIDs)
}
