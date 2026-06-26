package kg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrMessageNotInKB means the messageID does not exist or its chat belongs to a
// different KB. The handler maps it to 404 (no cross-KB graph leak).
var ErrMessageNotInKB = errors.New("kg: message not found in KB")

// NodeSource is one source document/chunk backing a scoped node.
type NodeSource struct {
	FileID   string `json:"fileId"`
	FileName string `json:"fileName"`
	ChunkID  string `json:"chunkId"`
}

// ScopedGraphNode is an entity in a query-scoped subgraph. Degree is the node's
// degree WITHIN the scoped set. Sources are the chunks/files whose edges touch
// this node within scope (deduped). Sources is never nil.
type ScopedGraphNode struct {
	ID      int64        `json:"id"`
	Name    string       `json:"name"`
	Type    string       `json:"type"`
	Degree  int          `json:"degree"`
	Sources []NodeSource `json:"sources"`
}

// ScopedGraph is the per-answer mindmap payload. Nodes/Edges are never nil.
type ScopedGraph struct {
	Nodes []ScopedGraphNode `json:"nodes"`
	Edges []GraphEdge       `json:"edges"`
}

// ScopedGraphForMessage returns the subgraph relevant to one AI answer:
// entities of the answer's chunks (kg_edges.chunk_id) unioned with entities
// matched from the paired user query, the edges among that set, and per-node
// sources. Returns ErrMessageNotInKB if messageID is absent or belongs to a
// different KB.
func (s *PgStore) ScopedGraphForMessage(ctx context.Context, kbID, messageID string) (ScopedGraph, error) {
	out := ScopedGraph{Nodes: []ScopedGraphNode{}, Edges: []GraphEdge{}}

	// 1. Ownership + paired user query. Verify the message's chat is in this KB.
	var parentID *string
	err := s.pool.QueryRow(ctx, `
		SELECT m.parent_message_id::text
		  FROM messages m
		  JOIN chats c ON c.id = m.chat_id
		 WHERE m.id = $1::uuid AND c.kb_id = $2::uuid
	`, messageID, kbID).Scan(&parentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrMessageNotInKB
		}
		return out, fmt.Errorf("ScopedGraphForMessage ownership: %w", err)
	}

	queryText := ""
	if parentID != nil {
		_ = s.pool.QueryRow(ctx, `
			SELECT content FROM messages WHERE id = $1::uuid AND role = 'user'
		`, *parentID).Scan(&queryText)
	}

	// 2. Chunk-derived entity IDs.
	seed := map[int64]bool{}
	crows, err := s.pool.Query(ctx, `
		SELECT DISTINCT e.src_entity_id, e.dst_entity_id
		  FROM kg_edges e
		 WHERE e.kb_id = $1::uuid
		   AND e.chunk_id IN (
		       SELECT chunk_id FROM message_chunks WHERE message_id = $2::uuid AND kb_id = $1::uuid
		   )
	`, kbID, messageID)
	if err != nil {
		return out, fmt.Errorf("ScopedGraphForMessage chunk entities: %w", err)
	}
	for crows.Next() {
		var a, b int64
		if scanErr := crows.Scan(&a, &b); scanErr != nil {
			crows.Close()
			return out, fmt.Errorf("ScopedGraphForMessage scan chunk entity: %w", scanErr)
		}
		seed[a] = true
		seed[b] = true
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return out, fmt.Errorf("ScopedGraphForMessage chunk entities rows: %w", err)
	}

	// 3. Matched-entity IDs from the recomputed query tokens.
	if tokens := TokenizeForGraph(queryText); len(tokens) > 0 {
		matched, mErr := s.MatchAliasesInTokens(ctx, kbID, tokens)
		if mErr == nil {
			for _, e := range matched {
				seed[e.ID] = true
			}
		}
	}

	if len(seed) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(seed))
	for id := range seed {
		ids = append(ids, id)
	}

	// 4. Nodes.
	nodeByID := map[int64]*ScopedGraphNode{}
	nrows, err := s.pool.Query(ctx, `
		SELECT id, canonical_name, type FROM kg_entities WHERE kb_id = $1::uuid AND id = ANY($2)
	`, kbID, ids)
	if err != nil {
		return out, fmt.Errorf("ScopedGraphForMessage nodes: %w", err)
	}
	for nrows.Next() {
		n := ScopedGraphNode{Sources: []NodeSource{}}
		if scanErr := nrows.Scan(&n.ID, &n.Name, &n.Type); scanErr != nil {
			nrows.Close()
			return out, fmt.Errorf("ScopedGraphForMessage scan node: %w", scanErr)
		}
		out.Nodes = append(out.Nodes, n)
	}
	nrows.Close()
	if err := nrows.Err(); err != nil {
		return out, fmt.Errorf("ScopedGraphForMessage nodes rows: %w", err)
	}
	for i := range out.Nodes {
		nodeByID[out.Nodes[i].ID] = &out.Nodes[i]
	}

	// 5. Intra-seed edges + degree.
	erows, err := s.pool.Query(ctx, `
		SELECT DISTINCT src_entity_id, dst_entity_id, rel
		  FROM kg_edges
		 WHERE kb_id = $1::uuid
		   AND src_entity_id = ANY($2) AND dst_entity_id = ANY($2)
		   AND src_entity_id <> dst_entity_id
	`, kbID, ids)
	if err != nil {
		return out, fmt.Errorf("ScopedGraphForMessage edges: %w", err)
	}
	for erows.Next() {
		var e GraphEdge
		if scanErr := erows.Scan(&e.Source, &e.Target, &e.Rel); scanErr != nil {
			erows.Close()
			return out, fmt.Errorf("ScopedGraphForMessage scan edge: %w", scanErr)
		}
		out.Edges = append(out.Edges, e)
		if n := nodeByID[e.Source]; n != nil {
			n.Degree++
		}
		if n := nodeByID[e.Target]; n != nil {
			n.Degree++
		}
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return out, fmt.Errorf("ScopedGraphForMessage edges rows: %w", err)
	}

	// 6. Per-node sources.
	srows, err := s.pool.Query(ctx, `
		SELECT e.src_entity_id, e.dst_entity_id, e.chunk_id::text, COALESCE(e.file_id::text,''), COALESCE(f.name,'')
		  FROM kg_edges e
		  LEFT JOIN files f ON f.id = e.file_id
		 WHERE e.kb_id = $1::uuid
		   AND (e.src_entity_id = ANY($2) OR e.dst_entity_id = ANY($2))
		   AND e.chunk_id IS NOT NULL
		 LIMIT 5000
	`, kbID, ids) // LIMIT is a safety cap: sources are a UI hint; a hub entity could otherwise return thousands of rows.
	if err != nil {
		return out, fmt.Errorf("ScopedGraphForMessage sources: %w", err)
	}
	seenSrc := map[int64]map[string]bool{}
	addSrc := func(nodeID int64, src NodeSource) {
		n := nodeByID[nodeID]
		if n == nil {
			return
		}
		if seenSrc[nodeID] == nil {
			seenSrc[nodeID] = map[string]bool{}
		}
		key := src.FileID + "|" + src.ChunkID
		if seenSrc[nodeID][key] {
			return
		}
		seenSrc[nodeID][key] = true
		n.Sources = append(n.Sources, src)
	}
	for srows.Next() {
		var src, dst int64
		var chunkID, fileID, fileName string
		if scanErr := srows.Scan(&src, &dst, &chunkID, &fileID, &fileName); scanErr != nil {
			srows.Close()
			return out, fmt.Errorf("ScopedGraphForMessage scan source: %w", scanErr)
		}
		ns := NodeSource{FileID: fileID, FileName: fileName, ChunkID: chunkID}
		addSrc(src, ns)
		addSrc(dst, ns)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return out, fmt.Errorf("ScopedGraphForMessage sources rows: %w", err)
	}

	return out, nil
}
