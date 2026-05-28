package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/mcp"
)

// GraphSearchArgs documents the AP-C3 graph_search tool input. The
// LLM picks `entity` from its question (e.g. "PPM-Team", "Eberhard
// Kurz"); the resolver maps it to a kg_entities row via canonical
// name / alias / lower-case match.
//
// depth is bounded to {1, 2}: depth 1 is direct neighbours, depth 2
// is "friends of friends". Beyond that the relevance signal degrades
// because most KB graphs are fully connected at depth 4-5.
type GraphSearchArgs struct {
	Entity         string `json:"entity"`
	Depth          int    `json:"depth,omitempty"`
	RelationFilter string `json:"relation_filter,omitempty"`
	KbID           string `json:"kb_id,omitempty"` // injected by the orchestrator
}

const graphSearchInputSchema = `{
  "type": "object",
  "required": ["entity"],
  "properties": {
    "entity":          { "type": "string",  "description": "Entity name (canonical or alias) to traverse from. Case-insensitive." },
    "depth":           { "type": "integer", "description": "Hops from the root entity. 1 (direct neighbours) or 2 (friends-of-friends). Default 1." },
    "relation_filter": { "type": "string",  "description": "Optional substring filter on relation names (e.g. 'works_in')." }
  }
}`

// NewGraphSearch builds the AP-C3 graph_search tool.
//
// The result shape is intentionally not chunk-shaped — graph_search
// returns entities + edges + chunk_ids, not raw chunk content. The
// caller (chat orchestrator or downstream tool) uses chunk_ids to
// pull citation-ready content via kb_search if needed. This keeps
// the tool's wire payload compact even when the subgraph touches
// many chunks.
func NewGraphSearch(store kg.Store) mcp.Tool {
	return mcp.Tool{
		Name:        "graph_search",
		Description: "Traverse the knowledge graph from an entity (depth 1 or 2). Returns entities, relations, and chunk IDs that backed the relations. Use when the question names a specific entity and asks about its connections (e.g. 'projects of person X', 'documents that reference team Y').",
		InputSchema: json.RawMessage(graphSearchInputSchema),
		Handler:     mcp.ToolHandlerFunc(graphSearchHandler(store)),
	}
}

func graphSearchHandler(store kg.Store) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args GraphSearchArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("graph_search: parse args: %w", err)
		}
		if args.Entity == "" {
			return mcp.ToolResult{}, fmt.Errorf("graph_search: entity is required")
		}
		if args.KbID == "" {
			return mcp.ToolResult{}, fmt.Errorf("graph_search: kb_id was not injected by the orchestrator")
		}
		if store == nil {
			return mcp.ToolResult{}, fmt.Errorf("graph_search: kg store is not configured (kg_extraction_enabled=false?)")
		}
		if args.Depth < 1 {
			args.Depth = 1
		}
		if args.Depth > 2 {
			args.Depth = 2
		}

		candidates, err := store.LookupEntityByName(ctx, args.KbID, args.Entity)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("graph_search: entity lookup: %w", err)
		}
		if len(candidates) == 0 {
			// No matching entity — return a clean empty result with
			// a Meta hint so the LLM knows this is "no graph data
			// for this entity" rather than "tool errored".
			return mcp.ToolResult{
				Meta: map[string]any{
					"entity":          args.Entity,
					"resolved_count":  0,
					"resolution_note": "no entity matched canonical name or alias",
					"depth":           args.Depth,
					"chunk_count":     0,
					"entity_count":    0,
					"edge_count":      0,
				},
			}, nil
		}

		// Pick the top candidate (canonical match preferred — the
		// store's ORDER BY puts those first). Disambiguation between
		// multiple candidates is the LLM's job: it sees them in the
		// resolved_candidates array and can call again with a more
		// specific entity name if needed.
		root := candidates[0]
		subg, err := store.LookupSubgraph(ctx, args.KbID, root.ID, args.Depth)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("graph_search: subgraph: %w", err)
		}

		// Apply the optional relation filter. Pure substring match
		// (case-insensitive) — operators rarely use the strict-form
		// because the LLM emits relations in snake_case but the
		// query might phrase them naturally.
		edges := subg.Edges
		if args.RelationFilter != "" {
			needle := strings.ToLower(strings.TrimSpace(args.RelationFilter))
			filtered := edges[:0]
			for _, e := range edges {
				if strings.Contains(strings.ToLower(e.Rel), needle) {
					filtered = append(filtered, e)
				}
			}
			edges = filtered
		}

		// Build the structured payload. Don't return chunk content
		// here — that's kb_search / chunk_read territory. Just
		// expose entity + edge tuples + the chunk_ids so the LLM
		// can pull evidence selectively.
		entitiesOut := make([]map[string]any, 0, len(subg.Entities))
		for _, e := range subg.Entities {
			entitiesOut = append(entitiesOut, map[string]any{
				"id":             e.ID,
				"canonical_name": e.CanonicalName,
				"type":           e.Type,
				"aliases":        e.Aliases,
			})
		}
		edgesOut := make([]map[string]any, 0, len(edges))
		for _, ed := range edges {
			edgesOut = append(edgesOut, map[string]any{
				"direction": ed.Direction,
				"rel":       ed.Rel,
				"other_id":  ed.Other.ID,
				"other":     ed.Other.CanonicalName,
				"type":      ed.Other.Type,
				"evidence":  ed.Evidence,
				"chunk_id":  ed.ChunkID,
			})
		}
		structured, _ := json.Marshal(map[string]any{
			"root":      root.CanonicalName,
			"root_id":   root.ID,
			"entities":  entitiesOut,
			"edges":     edgesOut,
			"chunk_ids": subg.ChunkIDs,
		})

		// Resolved candidates the disambiguation discarded — make
		// them visible to the LLM so it can re-call with a more
		// specific entity name when the wrong root was picked.
		resolvedCandidates := make([]map[string]any, 0, len(candidates))
		for _, c := range candidates {
			resolvedCandidates = append(resolvedCandidates, map[string]any{
				"id":             c.ID,
				"canonical_name": c.CanonicalName,
				"type":           c.Type,
			})
		}

		return mcp.ToolResult{
			Structured: structured,
			Meta: map[string]any{
				"entity":              args.Entity,
				"resolved_root":       root.CanonicalName,
				"resolved_count":      len(candidates),
				"resolved_candidates": resolvedCandidates,
				"depth":               args.Depth,
				"entity_count":        len(subg.Entities),
				"edge_count":          len(edges),
				"chunk_count":         len(subg.ChunkIDs),
			},
		}, nil
	}
}
