package chat

import (
	"context"

	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
)

// GraphTraversalDecision is the AP-C4 router's per-query verdict.
// When Fired is true, MatchedEntities lists the kg_entities the
// query mentioned (alias overlap); the chat handler dispatches
// graph_search against the highest-overlap candidate.
//
// Outcome mirrors the rag_graph_traversal_total enum so the metric
// + the trajectory event speak the same language: fired,
// skipped_no_entity, skipped_disabled, skipped_route, db_error.
type GraphTraversalDecision struct {
	Fired           bool
	Outcome         string
	MatchedEntities []kg.Entity
}

// NeedsGraphTraversal is the AP-C4 router heuristic. Returns
// `Fired=true` only when:
//  1. the gate `chat_graph_routing_enabled` is on
//  2. the query type is `complex_reasoning` (lookup / enumeration
//     have their own dedicated paths and don't benefit from graph
//     traversal in measured eval)
//  3. at least one query token overlaps with a kg_entities alias
//     for the KB (the GIN index makes this <1ms)
//
// The KG store is optional — when nil (no kg.Store wired), the
// heuristic reports `db_error`. Operators that haven't run AP-C1
// ingestion yet see this consistent label so they can spot the
// "extraction is off but routing is on" misconfiguration.
//
// Tokenisation is intentionally simple: split on whitespace +
// punctuation, lowercase, drop tokens shorter than 3 chars. This is
// not a real NER — it's the cheap-fast heuristic the plan calls
// for. A full NER stage (LLM-driven) is a Phase D follow-up.
func NeedsGraphTraversal(ctx context.Context, store kg.Store, siteCfg SiteConfigReader, kbID, query, queryType string) GraphTraversalDecision {
	if !ChatGraphRoutingEnabled(ctx, siteCfg) {
		observability.RecordGraphTraversal("skipped_disabled")
		return GraphTraversalDecision{Outcome: "skipped_disabled"}
	}
	if queryType != "complex_reasoning" {
		observability.RecordGraphTraversal("skipped_route")
		return GraphTraversalDecision{Outcome: "skipped_route"}
	}
	if store == nil {
		observability.RecordGraphTraversal("db_error")
		logctx.From(ctx).Warn("graph_routing: gate is on but kg.Store is not wired (kg_extraction_enabled probably also off)")
		return GraphTraversalDecision{Outcome: "db_error"}
	}

	tokens := tokeniseForGraph(query)
	if len(tokens) == 0 {
		observability.RecordGraphTraversal("skipped_no_entity")
		return GraphTraversalDecision{Outcome: "skipped_no_entity"}
	}
	matches, err := store.MatchAliasesInTokens(ctx, kbID, tokens)
	if err != nil {
		observability.RecordGraphTraversal("db_error")
		logctx.From(ctx).Warn("graph_routing: alias lookup failed",
			"error", err, "kbId", kbID, "tokens", len(tokens))
		return GraphTraversalDecision{Outcome: "db_error"}
	}
	if len(matches) == 0 {
		observability.RecordGraphTraversal("skipped_no_entity")
		return GraphTraversalDecision{Outcome: "skipped_no_entity"}
	}

	observability.RecordGraphTraversal("fired")
	logctx.From(ctx).Info("rag.graph_routing.decide",
		"matched_entities", len(matches),
		"top_entity", matches[0].CanonicalName)
	return GraphTraversalDecision{
		Fired:           true,
		Outcome:         "fired",
		MatchedEntities: matches,
	}
}

// ResolveGraphChunksIfEnabled wraps the AP-C4 chunk-injection gate
// + ResolveGraphChunks call. Returns nil when the decision didn't
// fire OR `chat_graph_routing_inject_chunks` is off OR the
// underlying traversal returned nothing (no entities, no chunks,
// store error — every branch fails open).
//
// Centralised so every orchestrator dispatch path in http_send.go
// (Supervisor / Plan-Execute / Agentic / DeepChat / standard
// PrepareChatContext) calls this once and forwards the result
// without duplicating the gate logic. The gate is read at the call
// site (not cached) so admin flips take effect without a restart.
//
// Dispatches on chat_graph_routing_path_mode:
//   - "neighbors" (default): legacy depth-1 BFS — ResolveGraphChunks
//   - "ppr":                Personalized PageRank — resolveGraphChunksPPR
//   - "paths":              relational paths — resolveGraphChunksPaths;
//     falls back to neighbors if only one entity matched (no pair to
//     connect)
func ResolveGraphChunksIfEnabled(ctx context.Context, store kg.Store, siteCfg SiteConfigReader, kbID string, dec GraphTraversalDecision) []string {
	if !dec.Fired {
		return nil
	}
	if !ChatGraphRoutingInjectChunks(ctx, siteCfg) {
		return nil
	}
	maxChunks := ChatGraphRoutingMaxChunks(ctx, siteCfg)
	mode := ChatGraphRoutingPathMode(ctx, siteCfg)
	switch mode {
	case "ppr":
		out := resolveGraphChunksPPR(ctx, store, siteCfg, kbID, dec, maxChunks)
		if len(out) > 0 {
			return out
		}
		// Fail open to neighbours so a tuning misconfig (e.g.
		// damping=0.99 producing a flat score map) still injects
		// something rather than silently dropping graph signal.
		return ResolveGraphChunks(ctx, store, kbID, dec, maxChunks)
	case "paths":
		out := resolveGraphChunksPaths(ctx, store, siteCfg, kbID, dec, maxChunks)
		if len(out) > 0 {
			return out
		}
		return ResolveGraphChunks(ctx, store, kbID, dec, maxChunks)
	default:
		return ResolveGraphChunks(ctx, store, kbID, dec, maxChunks)
	}
}

// resolveGraphChunksPPR is the T1-4 PPR injection strategy. Seeds
// PPR with every matched entity (not just the top-1) so a query
// that anchors on two distinct entities benefits from both random
// walks. The PPR config comes from chat_graph_routing_ppr_*.
//
// Returns nil when the store is unwired, no entities matched, or
// PPRChunks returned an error / empty result — the caller's switch
// then falls back to neighbours.
func resolveGraphChunksPPR(ctx context.Context, store kg.Store, siteCfg SiteConfigReader, kbID string, dec GraphTraversalDecision, maxChunks int) []string {
	if store == nil || len(dec.MatchedEntities) == 0 {
		return nil
	}
	seeds := make([]int64, 0, len(dec.MatchedEntities))
	for _, e := range dec.MatchedEntities {
		seeds = append(seeds, e.ID)
	}
	cfg := kg.PPRConfig{
		Damping: ChatGraphRoutingPPRDamping(ctx, siteCfg),
		MaxIter: ChatGraphRoutingPPRMaxIter(ctx, siteCfg),
	}
	var out []string
	var err error
	if ChatGraphRoutingPPRDualNodeEnabled(ctx, siteCfg) {
		out, err = store.PPRPassages(ctx, kbID, seeds, maxChunks, cfg)
	} else {
		topEntities := ChatGraphRoutingPPRTopEntities(ctx, siteCfg)
		out, err = store.PPRChunks(ctx, kbID, seeds, topEntities, maxChunks, cfg)
	}
	if err != nil {
		logctx.From(ctx).Warn("graph_routing: ppr chunk lookup failed; falling back to neighbours",
			"error", err, "kb_id", kbID, "seeds", len(seeds))
		return nil
	}
	return out
}

// bridgeChunkTally enumerates unordered pairs of the matched entities and
// tallies, per chunk id, how many inter-entity KG paths it lies on. Shared by
// the `paths` injection mode (resolveGraphChunksPaths) and bridge-evidence
// reranking. Returns nil for <2 entities or an empty tally; per-pair PathChunks
// errors are logged and skipped (fail-open).
func bridgeChunkTally(ctx context.Context, store kg.Store, siteCfg SiteConfigReader, kbID string, ents []kg.Entity, maxChunks int) map[string]int {
	if store == nil || len(ents) < 2 {
		return nil
	}
	cfg := kg.PathConfig{
		MaxLen:   ChatGraphRoutingPathsMaxLen(ctx, siteCfg),
		MaxPaths: ChatGraphRoutingPathsMaxPaths(ctx, siteCfg),
	}
	tally := make(map[string]int)
	for i := 0; i < len(ents); i++ {
		for j := i + 1; j < len(ents); j++ {
			chunks, err := store.PathChunks(ctx, kbID, ents[i].ID, ents[j].ID, maxChunks, cfg)
			if err != nil {
				logctx.From(ctx).Warn("graph_routing: path chunk lookup failed; continuing with other pairs",
					"error", err, "kb_id", kbID,
					"src", ents[i].CanonicalName, "dst", ents[j].CanonicalName)
				continue
			}
			for _, c := range chunks {
				tally[c]++
			}
		}
	}
	if len(tally) == 0 {
		return nil
	}
	return tally
}

// resolveGraphChunksPaths is the T1-5 PathRAG injection strategy.
// Enumerates relational paths between every distinct pair of
// matched entities and unions their chunks (deduped, capped at
// maxChunks). Single-entity queries can't form a pair; that case
// returns nil so the caller falls back to neighbours.
//
// The pair enumeration is O(N²) over the matched-entity list,
// which the alias router caps at 10 — worst case 45 pairs, but in
// practice 1-3 entities match per query and the inner store call
// is short-circuited for disconnected pairs.
func resolveGraphChunksPaths(ctx context.Context, store kg.Store, siteCfg SiteConfigReader, kbID string, dec GraphTraversalDecision, maxChunks int) []string {
	tally := bridgeChunkTally(ctx, store, siteCfg, kbID, dec.MatchedEntities, maxChunks)
	if len(tally) == 0 {
		return nil
	}
	type chunkScore struct {
		id    string
		score int // simple frequency tally — same dedup logic as neighbours mode
	}

	// Order by frequency desc (chunks that appear on more
	// inter-entity paths are stronger evidence), ties by id for
	// determinism.
	ranked := make([]chunkScore, 0, len(tally))
	for id, n := range tally {
		ranked = append(ranked, chunkScore{id, n})
	}
	// Inline sort to keep imports minimal — slices.SortFunc would
	// also work but adds an import for a one-shot use.
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0; j-- {
			a, b := ranked[j-1], ranked[j]
			if a.score < b.score || (a.score == b.score && a.id > b.id) {
				ranked[j-1], ranked[j] = b, a
			} else {
				break
			}
		}
	}
	if len(ranked) > maxChunks {
		ranked = ranked[:maxChunks]
	}
	out := make([]string, len(ranked))
	for i, r := range ranked {
		out[i] = r.id
	}
	return out
}

// ResolveGraphChunks turns a fired AP-C4 routing decision into the
// list of chunk IDs the search pipeline should fold into RRF
// (vector.SearchOptions.GraphChunkIDs).
//
// Strategy:
//   - Top-1 matched entity only. The router's MatchAliasesInTokens
//     already returns entities ordered by alias-overlap-count desc,
//     so the first match is the most-anchored one. Multi-entity
//     expansion is a follow-up; the v1 cut keeps the RRF input
//     bounded and avoids dilution from weakly-related entities.
//   - depth=1. Two-hop subgraphs grow combinatorially and the kg
//     module's own LookupSubgraph documents that the relevance
//     signal degrades sharply past depth 2. Depth=1 is the safest
//     default; a `chat_graph_routing_depth` knob is a follow-up if
//     ops data justifies it.
//   - Cap at maxChunks. Subgraph traversal can return many chunks
//     for densely-connected entities; the cap protects the RRF pool
//     from being swamped (recommended 15, see
//     ChatGraphRoutingMaxChunks).
//   - Fail-open. A nil store, an empty MatchedEntities slice, a
//     LookupSubgraph error, or an empty subgraph all yield nil so
//     the chat pipeline proceeds without graph injection rather
//     than failing the turn.
//   - Defensive dedup. kg.Subgraph.ChunkIDs is already deduplicated
//     during construction, but a future store change shouldn't
//     silently inflate the RRF input.
func ResolveGraphChunks(ctx context.Context, store kg.Store, kbID string, dec GraphTraversalDecision, maxChunks int) []string {
	if !dec.Fired || len(dec.MatchedEntities) == 0 || store == nil {
		return nil
	}
	if maxChunks < 1 {
		return nil
	}
	top := dec.MatchedEntities[0]
	subg, err := store.LookupSubgraph(ctx, kbID, top.ID, 1)
	if err != nil {
		logctx.From(ctx).Warn("graph_routing: subgraph lookup failed",
			"error", err, "kb_id", kbID, "entity_id", top.ID, "entity", top.CanonicalName)
		return nil
	}
	if len(subg.ChunkIDs) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(subg.ChunkIDs), maxChunks))
	seen := make(map[string]struct{}, len(subg.ChunkIDs))
	for _, id := range subg.ChunkIDs {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= maxChunks {
			break
		}
	}
	return out
}

// tokeniseForGraph splits a query into candidate alias-match
// tokens. Drops anything <3 chars (common tokens like "der", "the",
// "and" never carry entity signal and would just inflate the alias
// lookup payload). Tokens are CASE-PRESERVED — the kg.Store does
// the lower-case match itself so multi-word aliases like "PPM-Team"
// stay intact.
//
// We split on Unicode-aware non-letters/digits (handles "PPM-Team"
// → ["PPM", "Team"]) which is right for German hyphenated names
// and acronym-prefixed identifiers. The aliases stored from the
// extractor's normalisation also carry hyphenated variants, so the
// tokeniser doesn't need to reproduce them.
func tokeniseForGraph(query string) []string {
	return kg.TokenizeForGraph(query)
}
