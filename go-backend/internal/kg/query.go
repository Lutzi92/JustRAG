// Package kg is the read-side of the AP-C1 knowledge-graph
// storage. Ingestion writes via internal/processor/kg_store.go; this
// package owns the SELECT path used by the AP-C3 graph_search tool
// and the AP-C4 router heuristic.
//
// Why a separate package: keeping read and write apart prevents the
// ingestion processor from accidentally importing query helpers
// that pull pgxpool dependencies into a code path that doesn't
// need them, AND lets the chat / mcp packages depend on this
// package without inheriting the ingestion processor's parser
// dependencies.
package kg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/observability"
)

// Entity is one row of kg_entities surfaced to the AP-C3 / AP-C4
// callers. Aliases is the surface-form list the ingestion stage
// observed across chunks; the GIN index on aliases is what makes
// the alias-based router heuristic cheap.
type Entity struct {
	ID            int64
	CanonicalName string
	Type          string
	Aliases       []string
}

// Edge is one row of kg_edges with the related entity already
// joined. Direction is "out" when the queried entity is the src
// (this row points away from it) or "in" when the queried entity
// is the dst (this row points toward it). The graph_search tool
// returns both directions because relations are inherently
// bidirectional from the LLM's reasoning perspective — "X works in
// Y" and "Y employs X" are the same fact.
type Edge struct {
	Direction string // "out" | "in"
	Rel       string
	Other     Entity
	Evidence  string
	ChunkID   string
}

// Subgraph is the AP-C3 graph_search return shape. Entities is the
// queried entity plus everything reachable within the requested
// depth. Edges is every relation the traversal walked. ChunkIDs is
// the deduplicated set of chunk_id values across those edges —
// the chat layer feeds these into the existing chunk lookup so
// graph_search results carry citation-ready evidence.
type Subgraph struct {
	Root     Entity
	Entities []Entity
	Edges    []Edge
	ChunkIDs []string
}

// Store is the read-only KG query surface. The MCP tool and the
// router heuristic talk to it directly; production wires *PgStore
// (pgxpool-backed) in routes.go, tests inject fakes.
type Store interface {
	LookupEntityByName(ctx context.Context, kbID, name string) ([]Entity, error)
	LookupSubgraph(ctx context.Context, kbID string, entityID int64, depth int) (Subgraph, error)
	MatchAliasesInTokens(ctx context.Context, kbID string, tokens []string) ([]Entity, error)
	// PPRChunks runs Personalized PageRank (T1-4) seeded from
	// seedIDs over the KB's edge set, picks the top-topEntities
	// most relevant nodes (excluding seeds — those already feed
	// the chat layer's primary path), and returns the deduped
	// chunk_ids of edges incident to that pool, capped at
	// maxChunks. Implementation in pagerank.go.
	PPRChunks(ctx context.Context, kbID string, seedIDs []int64, topEntities, maxChunks int, cfg PPRConfig) ([]string, error)
	// PathChunks runs PathRAG-style path enumeration (T1-5)
	// between two entities and returns the deduped chunk_ids
	// across all returned paths, capped at maxChunks.
	// Implementation in paths.go.
	PathChunks(ctx context.Context, kbID string, srcID, dstID int64, maxChunks int, cfg PathConfig) ([]string, error)
}

// PgStore is the pgxpool-backed implementation.
type PgStore struct {
	pool *pgxpool.Pool
}

// NewPgStore wraps a Postgres pool. The pool must point at the
// main DB where kg_entities + kg_edges live.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool}
}

// Compile-time assertion that PgStore satisfies the read-side Store surface.
var _ Store = (*PgStore)(nil)

// LookupEntityByName resolves a user-supplied entity name to one or
// more kg_entities rows. Match priority:
//  1. exact canonical_name (case-insensitive) match
//  2. alias array containment (case-insensitive)
//
// Returns at most 5 candidates ordered by canonical-match-first.
// More than 5 indicates an over-broad query and the caller (LLM /
// router) should refine; the cap is also a defence against an
// adversarial entity name like "Team" matching every project KB.
//
// Cost is dominated by the alias GIN index — the candidate cap of
// 5 keeps the row scan bounded even on a KB with thousands of
// entities. Total ~1-5ms typical.
func (s *PgStore) LookupEntityByName(ctx context.Context, kbID, name string) ([]Entity, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("kg: no DB pool configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("kg: empty entity name")
	}
	start := time.Now()
	defer func() {
		observability.ObserveKGLookupSeconds(time.Since(start).Seconds())
	}()

	// Case-insensitive canonical OR alias containment. The CASE
	// expression in ORDER BY puts canonical matches first so the
	// LLM sees the most-likely entity before disambiguation
	// candidates.
	rows, err := s.pool.Query(ctx, `
		SELECT id, canonical_name, type, aliases
		  FROM kg_entities
		 WHERE kb_id = $1::uuid
		   AND (LOWER(canonical_name) = LOWER($2)
		        OR LOWER($2) = ANY (
		            SELECT LOWER(unnest(aliases))
		        ))
		 ORDER BY (CASE WHEN LOWER(canonical_name) = LOWER($2) THEN 0 ELSE 1 END), id
		 LIMIT 5
	`, kbID, name)
	if err != nil {
		return nil, fmt.Errorf("kg: lookup entity: %w", err)
	}
	defer rows.Close()
	out := make([]Entity, 0, 5)
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.CanonicalName, &e.Type, &e.Aliases); err != nil {
			return nil, fmt.Errorf("kg: scan entity: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kg: rows: %w", err)
	}
	return out, nil
}

// LookupSubgraph walks the graph from `entityID` outward up to
// `depth` hops and returns every entity + edge it touched. depth
// is clamped to [1, 2] — beyond two hops the relevance signal
// degrades sharply (any KB-scale graph is fully connected at
// depth 4-5) and the LLM context budget can't carry more than
// ~50 chunks anyway.
//
// Implementation: a single recursive CTE walks the edges. We
// query the edges twice per hop (forward + reverse) because the
// "direction" of a relation in extracted text is rarely
// canonical — "PPM-Team gehört zu HRZ" and "HRZ enthält PPM-Team"
// would be two relations from the same fact, going opposite
// directions; the traversal must follow both.
func (s *PgStore) LookupSubgraph(ctx context.Context, kbID string, entityID int64, depth int) (Subgraph, error) {
	if s == nil || s.pool == nil {
		return Subgraph{}, errors.New("kg: no DB pool configured")
	}
	if depth < 1 {
		depth = 1
	}
	if depth > 2 {
		depth = 2
	}

	// Root entity load.
	var root Entity
	err := s.pool.QueryRow(ctx, `
		SELECT id, canonical_name, type, aliases
		  FROM kg_entities
		 WHERE kb_id = $1::uuid AND id = $2
	`, kbID, entityID).Scan(&root.ID, &root.CanonicalName, &root.Type, &root.Aliases)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Subgraph{}, nil
		}
		return Subgraph{}, fmt.Errorf("kg: load root entity: %w", err)
	}

	// Recursive CTE: walks both directions, depth-bounded. The main
	// SELECT keeps only edges whose BOTH endpoints were reached and
	// returns both endpoint records plus their hop distances. The Go
	// side anchors each edge at the endpoint nearer the root, so
	// Edge.Other is the endpoint farther from the root — for depth-1
	// edges that reproduces the old root-relative semantics exactly,
	// and for second-hop edges (e.g. C→D when walking from B) it
	// surfaces the newly discovered entity instead of mislabeling the
	// already-known one (the old projection compared endpoints against
	// the root only, so depth-2 walks silently degenerated to depth-1
	// stars).
	const subgraphQuery = `
		WITH RECURSIVE walk(entity_id, depth) AS (
			SELECT $2::bigint, 0
			UNION
			SELECT CASE WHEN e.src_entity_id = w.entity_id THEN e.dst_entity_id
			            ELSE e.src_entity_id END,
			       w.depth + 1
			  FROM kg_edges e
			  JOIN walk w
			    ON (e.src_entity_id = w.entity_id OR e.dst_entity_id = w.entity_id)
			 WHERE e.kb_id = $1::uuid
			   AND w.depth < $3
		),
		reach AS (
			SELECT entity_id, MIN(depth) AS depth FROM walk GROUP BY entity_id
		)
		SELECT DISTINCT
		       e.rel,
		       se.id, se.canonical_name, se.type, se.aliases, rs.depth,
		       de.id, de.canonical_name, de.type, de.aliases, rd.depth,
		       e.evidence,
		       COALESCE(e.chunk_id::text, '')
		  FROM kg_edges e
		  JOIN reach rs ON rs.entity_id = e.src_entity_id
		  JOIN reach rd ON rd.entity_id = e.dst_entity_id
		  JOIN kg_entities se ON se.id = e.src_entity_id
		  JOIN kg_entities de ON de.id = e.dst_entity_id
		 WHERE e.kb_id = $1::uuid
	`
	rows, err := s.pool.Query(ctx, subgraphQuery, kbID, entityID, depth)
	if err != nil {
		return Subgraph{}, fmt.Errorf("kg: subgraph walk: %w", err)
	}
	defer rows.Close()

	subg := Subgraph{Root: root, Entities: []Entity{root}}
	seenEntity := map[int64]bool{root.ID: true}
	seenChunk := map[string]bool{}
	addEntity := func(e Entity) {
		if !seenEntity[e.ID] {
			seenEntity[e.ID] = true
			subg.Entities = append(subg.Entities, e)
		}
	}
	for rows.Next() {
		var (
			ed                 Edge
			src, dst           Entity
			srcDepth, dstDepth int
		)
		if err := rows.Scan(&ed.Rel,
			&src.ID, &src.CanonicalName, &src.Type, &src.Aliases, &srcDepth,
			&dst.ID, &dst.CanonicalName, &dst.Type, &dst.Aliases, &dstDepth,
			&ed.Evidence, &ed.ChunkID); err != nil {
			return Subgraph{}, fmt.Errorf("kg: scan edge: %w", err)
		}
		// Anchor at the endpoint nearer the root; "out" means the edge
		// leaves the anchor. Equal depths anchor at src, matching how a
		// root-incident edge with the root as src reads as "out".
		if srcDepth <= dstDepth {
			ed.Direction, ed.Other = "out", dst
		} else {
			ed.Direction, ed.Other = "in", src
		}
		subg.Edges = append(subg.Edges, ed)
		addEntity(src)
		addEntity(dst)
		if ed.ChunkID != "" && !seenChunk[ed.ChunkID] {
			seenChunk[ed.ChunkID] = true
			subg.ChunkIDs = append(subg.ChunkIDs, ed.ChunkID)
		}
	}
	if err := rows.Err(); err != nil {
		return Subgraph{}, fmt.Errorf("kg: subgraph rows: %w", err)
	}
	return subg, nil
}

// MatchAliasesInTokens is the AP-C4 router heuristic. Given a
// pre-tokenized query (the caller splits on whitespace + punctuation
// — kg-package doesn't take a stand on tokenisation), find every
// kg_entity whose `aliases` array overlaps with the token set.
//
// Returns up to 10 entities ordered by alias-overlap-count desc.
// 10 is a soft cap — a query mentioning more than 10 distinct
// entities is almost certainly an enumeration in disguise, which
// the existing enumeration pre-pass handles better.
//
// The query uses `aliases && $tokens::text[]` which the GIN index
// on aliases serves directly. Cost: typically <1ms even on a
// 100k-entity KB.
func (s *PgStore) MatchAliasesInTokens(ctx context.Context, kbID string, tokens []string) ([]Entity, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("kg: no DB pool configured")
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	// Lower-case + dedupe the token list. Aliases are stored as
	// the LLM emitted them (case-preserving — "PPM" not "ppm"),
	// but the router's match should be case-insensitive so we
	// build a lowercase token list AND a comparison column.
	seen := make(map[string]bool, len(tokens))
	cleaned := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		cleaned = append(cleaned, t)
	}
	if len(cleaned) == 0 {
		return nil, nil
	}

	// The CARDINALITY count picks the most-relevant entity when a
	// query mentions many — "Eberhard Kurz arbeitet im PPM-Team"
	// has two entity hits ("Kurz" + "PPM-Team"); both ranked.
	//
	// CROSS JOIN the single-row `lowered` CTE so the lowered-token
	// array is in scope as a column reference (`l.tokens`). Using
	// the array directly in `ANY(l.tokens)` keeps PG in array-expression
	// mode; the alternative `ANY((SELECT tokens FROM lowered))`
	// is parsed as the ANY-subquery form, which compares the LHS
	// against each subquery *row* — each row being a text[], not
	// a text, which is why it fails with "operator does not exist:
	// text = text[]".
	rows, err := s.pool.Query(ctx, `
		WITH lowered AS (
		    SELECT array_agg(LOWER(t)) AS tokens FROM unnest($2::text[]) t
		)
		SELECT e.id, e.canonical_name, e.type, e.aliases,
		       CARDINALITY(ARRAY(
		           SELECT a FROM unnest(e.aliases) a WHERE LOWER(a) = ANY(l.tokens)
		       )) AS overlap
		  FROM kg_entities e CROSS JOIN lowered l
		 WHERE e.kb_id = $1::uuid
		   AND e.aliases && l.tokens
		 ORDER BY overlap DESC, e.id
		 LIMIT 10
	`, kbID, cleaned)
	if err != nil {
		return nil, fmt.Errorf("kg: alias match: %w", err)
	}
	defer rows.Close()
	out := make([]Entity, 0, 10)
	for rows.Next() {
		var e Entity
		var overlap int
		if err := rows.Scan(&e.ID, &e.CanonicalName, &e.Type, &e.Aliases, &overlap); err != nil {
			return nil, fmt.Errorf("kg: alias scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kg: alias rows: %w", err)
	}
	return out, nil
}
