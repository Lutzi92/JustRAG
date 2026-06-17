package kg

import (
	"context"
	"fmt"
)

// GraphNode is one entity in the KB-overview graph: an entity plus its total
// (in+out) degree, used by the frontend mind map to size/prioritise nodes.
type GraphNode struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Degree int    `json:"degree"`
}

// GraphEdge is a distinct relation between two nodes in the overview graph.
// Multiple evidence rows for the same (src,dst,rel) collapse to one edge.
type GraphEdge struct {
	Source int64  `json:"source"`
	Target int64  `json:"target"`
	Rel    string `json:"rel"`
}

// GraphOverview is the KB-wide mind-map payload: the highest-degree entities
// and the distinct relations among that set. Nodes/Edges are never nil so the
// JSON response is always a well-formed object (empty arrays, not null) — the
// common case for KBs where KG extraction is disabled.
type GraphOverview struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// defaultGraphMaxNodes bounds the overview so a large KG stays renderable; the
// top-degree slice keeps the most connected (most informative) entities.
const defaultGraphMaxNodes = 120

// GraphOverview returns the top-degree entities of a KB and the distinct edges
// among them, capped at maxNodes (<=0 or >500 ⇒ defaultGraphMaxNodes). It is the
// read backing for the frontend mind map; self-loops are excluded.
func (s *PgStore) GraphOverview(ctx context.Context, kbID string, maxNodes int) (GraphOverview, error) {
	if maxNodes <= 0 || maxNodes > 500 {
		maxNodes = defaultGraphMaxNodes
	}
	out := GraphOverview{Nodes: []GraphNode{}, Edges: []GraphEdge{}}

	// Top entities by total degree. Degree is computed once over the edge set
	// (UNION ALL of both endpoints) rather than a correlated subquery per row,
	// so this stays a single index-backed scan even on large KGs.
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.canonical_name, e.type, COALESCE(d.deg, 0) AS degree
		  FROM kg_entities e
		  LEFT JOIN (
		      SELECT entity_id, count(*) AS deg
		        FROM (
		            SELECT src_entity_id AS entity_id FROM kg_edges WHERE kb_id = $1
		            UNION ALL
		            SELECT dst_entity_id FROM kg_edges WHERE kb_id = $1
		        ) t
		       GROUP BY entity_id
		  ) d ON d.entity_id = e.id
		 WHERE e.kb_id = $1
		 ORDER BY degree DESC, e.id
		 LIMIT $2
	`, kbID, maxNodes)
	if err != nil {
		return out, fmt.Errorf("GraphOverview entities: %w", err)
	}
	ids := make([]int64, 0, maxNodes)
	for rows.Next() {
		var n GraphNode
		if scanErr := rows.Scan(&n.ID, &n.Name, &n.Type, &n.Degree); scanErr != nil {
			rows.Close()
			return out, fmt.Errorf("GraphOverview scan entity: %w", scanErr)
		}
		out.Nodes = append(out.Nodes, n)
		ids = append(ids, n.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("GraphOverview entities rows: %w", err)
	}
	if len(ids) == 0 {
		return out, nil
	}

	// Distinct relations whose BOTH endpoints are in the selected node set.
	erows, err := s.pool.Query(ctx, `
		SELECT DISTINCT src_entity_id, dst_entity_id, rel
		  FROM kg_edges
		 WHERE kb_id = $1
		   AND src_entity_id = ANY($2)
		   AND dst_entity_id = ANY($2)
		   AND src_entity_id <> dst_entity_id
	`, kbID, ids)
	if err != nil {
		return out, fmt.Errorf("GraphOverview edges: %w", err)
	}
	defer erows.Close()
	for erows.Next() {
		var e GraphEdge
		if scanErr := erows.Scan(&e.Source, &e.Target, &e.Rel); scanErr != nil {
			return out, fmt.Errorf("GraphOverview scan edge: %w", scanErr)
		}
		out.Edges = append(out.Edges, e)
	}
	if err := erows.Err(); err != nil {
		return out, fmt.Errorf("GraphOverview edges rows: %w", err)
	}
	return out, nil
}
