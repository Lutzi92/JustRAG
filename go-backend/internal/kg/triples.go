package kg

import (
	"context"
	"errors"
	"fmt"
)

// IncidentTriple is one (src)-[rel]->(dst) fact incident to a seed entity,
// with both endpoint names resolved, for the LLM triple-filter prompt.
type IncidentTriple struct {
	SrcID   int64  `json:"srcId"`
	SrcName string `json:"srcName"`
	Rel     string `json:"rel"`
	DstID   int64  `json:"dstId"`
	DstName string `json:"dstName"`
}

// IncidentTriples returns up to `cap` distinct triples incident to any of
// entityIDs, ordered by edge weight (strongest first). Distinct on
// (src,rel,dst) so multi-evidence duplicates collapse. Empty inputs →
// (nil,nil). Used by the chat triple-filter to bound the LLM prompt.
func (s *PgStore) IncidentTriples(ctx context.Context, kbID string, entityIDs []int64, cap int) ([]IncidentTriple, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("kg: no DB pool configured")
	}
	if len(entityIDs) == 0 || cap < 1 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.src_entity_id, src.canonical_name, e.rel, e.dst_entity_id, dst.canonical_name
		  FROM kg_edges e
		  JOIN kg_entities src ON src.id = e.src_entity_id
		  JOIN kg_entities dst ON dst.id = e.dst_entity_id
		 WHERE e.kb_id = $1::uuid
		   AND (e.src_entity_id = ANY($2) OR e.dst_entity_id = ANY($2))
		 ORDER BY e.weight DESC, e.id
		 LIMIT $3::int
	`, kbID, entityIDs, cap)
	if err != nil {
		return nil, fmt.Errorf("kg: incident triples: %w", err)
	}
	defer rows.Close()

	type key struct {
		s int64
		r string
		d int64
	}
	seen := make(map[key]bool)
	out := make([]IncidentTriple, 0, cap)
	for rows.Next() {
		var tr IncidentTriple
		if err := rows.Scan(&tr.SrcID, &tr.SrcName, &tr.Rel, &tr.DstID, &tr.DstName); err != nil {
			return nil, fmt.Errorf("kg: scan incident triple: %w", err)
		}
		k := key{tr.SrcID, tr.Rel, tr.DstID}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, tr)
		if len(out) >= cap {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kg: incident triple rows: %w", err)
	}
	return out, nil
}
