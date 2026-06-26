package kg

import (
	"context"
	"fmt"
)

// CommunityEdge is one weighted relationship for topology clustering.
type CommunityEdge struct {
	Src, Dst int64
	Weight   float64
}

// CommunityEntity is a member entity (name+type) for the summary input.
type CommunityEntity struct {
	ID   int64
	Name string
	Type string
}

// InternalEdgeRow is one kg_edge for the summary input. Endpoint names are
// resolved from the members map at summary-build time, so they aren't selected here.
type InternalEdgeRow struct {
	Src, Dst      int64
	Rel, Evidence string
}

// EdgesForCommunities loads the KB's kg_edges as a weighted edge list.
func (s *PgStore) EdgesForCommunities(ctx context.Context, kbID string) ([]CommunityEdge, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("kg: EdgesForCommunities: no pool")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT src_entity_id, dst_entity_id, weight FROM kg_edges WHERE kb_id = $1::uuid
	`, kbID)
	if err != nil {
		return nil, fmt.Errorf("kg: edges for communities: %w", err)
	}
	defer rows.Close()
	out := []CommunityEdge{}
	for rows.Next() {
		var e CommunityEdge
		if err := rows.Scan(&e.Src, &e.Dst, &e.Weight); err != nil {
			return nil, fmt.Errorf("kg: scan community edge: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntitiesForCommunities loads names+types for the given entity ids.
func (s *PgStore) EntitiesForCommunities(ctx context.Context, kbID string, ids []int64) ([]CommunityEntity, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("kg: EntitiesForCommunities: no pool")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, canonical_name, type FROM kg_entities WHERE kb_id = $1::uuid AND id = ANY($2)
	`, kbID, ids)
	if err != nil {
		return nil, fmt.Errorf("kg: entities for communities: %w", err)
	}
	defer rows.Close()
	out := []CommunityEntity{}
	for rows.Next() {
		var e CommunityEntity
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, fmt.Errorf("kg: scan community entity: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// InternalEdgesForCommunities loads every kg_edge (rel + evidence) for the KB.
func (s *PgStore) InternalEdgesForCommunities(ctx context.Context, kbID string) ([]InternalEdgeRow, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("kg: InternalEdgesForCommunities: no pool")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT src_entity_id, dst_entity_id, rel, COALESCE(evidence,'')
		  FROM kg_edges WHERE kb_id = $1::uuid
	`, kbID)
	if err != nil {
		return nil, fmt.Errorf("kg: internal edges for communities: %w", err)
	}
	defer rows.Close()
	out := []InternalEdgeRow{}
	for rows.Next() {
		var r InternalEdgeRow
		if err := rows.Scan(&r.Src, &r.Dst, &r.Rel, &r.Evidence); err != nil {
			return nil, fmt.Errorf("kg: scan internal edge: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClearCommunities removes all kg_communities rows for a KB (clear-then-rebuild).
func (s *PgStore) ClearCommunities(ctx context.Context, kbID string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("kg: ClearCommunities: no pool")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM kg_communities WHERE kb_id = $1::uuid`, kbID); err != nil {
		return fmt.Errorf("kg: clear communities: %w", err)
	}
	return nil
}

// StoreCommunity records one detected+summarised community.
func (s *PgStore) StoreCommunity(ctx context.Context, kbID string, level int, memberIDs []int64, summaryChunkID string, size int) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("kg: StoreCommunity: no pool")
	}
	var chunkArg any
	if summaryChunkID != "" {
		chunkArg = summaryChunkID
	}
	// Single INSERT is atomic on its own — no explicit transaction needed.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO kg_communities (kb_id, level, member_entity_ids, summary_chunk_id, size)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5)
	`, kbID, level, memberIDs, chunkArg, size); err != nil {
		return fmt.Errorf("kg: store community: %w", err)
	}
	return nil
}
