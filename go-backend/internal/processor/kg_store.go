package processor

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/pgxutil"
)

// kgEntityID is the BIGSERIAL id assigned by Postgres on insert.
type kgEntityID int64

// kgStore is the AP-C1 persistence layer for kg_entities + kg_edges.
// Lives in the processor package because the ingestion path is the
// only producer; query-time consumers (graph_search tool, AP-C4
// router) read directly via pgxpool.
//
// Idempotency: persistKGExtraction is safe to call repeatedly with
// the same chunk's extraction. Entity inserts use ON CONFLICT
// (kb_id, canonical_name, type) with an aliases-merge that
// preserves any aliases the previous chunk(s) saw. Edges are
// always appended — multiple edges between the same (src, dst)
// are allowed because evidence is per-chunk; the graph_search
// tool dedupes downstream when it traverses.
type kgStore struct {
	pool *pgxpool.Pool
}

func newKGStore(pool *pgxpool.Pool) *kgStore {
	return &kgStore{pool: pool}
}

// persistKGExtraction writes one chunk's extraction into kg_entities
// + kg_edges atomically. Errors are returned to the caller so the
// ingestion processor can log and proceed (the chunk is stored
// regardless — KG is a side-channel, never blocks ingestion).
//
// Returns the count of (created entities, dedup-merged entities,
// inserted edges) for the per-stage log.
func (s *kgStore) persistKGExtraction(ctx context.Context, kbID, chunkID string, ext ai.KGExtraction) (created, deduped, edges int, err error) {
	if s == nil || s.pool == nil {
		return 0, 0, 0, errors.New("kg_store: no DB pool configured")
	}
	if len(ext.Entities) == 0 && len(ext.Relations) == 0 {
		return 0, 0, 0, nil
	}

	err = pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Upsert entities and remember (canonical_name → id) for the
		// edge phase. ON CONFLICT keeps the existing id; aliases get
		// merged via array_cat + dedupe.
		idByName := make(map[string]kgEntityID, len(ext.Entities))
		for _, e := range ext.Entities {
			var id kgEntityID
			var inserted bool
			// xmax=0 in the RETURNING reveals whether ON CONFLICT
			// took the insert or the update branch — see
			// https://stackoverflow.com/a/39204667. Used to count
			// "create" vs "dedupe" outcomes for the metric.
			if err := tx.QueryRow(ctx, `
				INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
				VALUES ($1::uuid, $2, $3, $4::text[])
				ON CONFLICT (kb_id, canonical_name, type)
				DO UPDATE SET
					aliases = (
						SELECT ARRAY(
							SELECT DISTINCT unnest(kg_entities.aliases || EXCLUDED.aliases)
						)
					),
					updated_at = NOW()
				RETURNING id, (xmax = 0) AS inserted
			`, kbID, e.Name, e.Type, e.Aliases).Scan(&id, &inserted); err != nil {
				return fmt.Errorf("kg_store: upsert entity %q: %w", e.Name, err)
			}
			idByName[e.Name] = id
			if inserted {
				created++
				observability.RecordKGDisambiguation("create")
			} else {
				deduped++
				observability.RecordKGDisambiguation("dedupe")
			}
		}

		// Insert edges. Relations whose src/dst weren't in the entity
		// list got dropped by the AI layer already, but defense-in-
		// depth: re-check here so a corrupted KGExtraction doesn't
		// blow up with a NULL FK.
		for _, r := range ext.Relations {
			srcID, srcOK := idByName[r.Src]
			dstID, dstOK := idByName[r.Dst]
			if !srcOK || !dstOK {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, evidence, chunk_id)
				VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, '')::uuid)
			`, kbID, srcID, dstID, r.Rel, r.Evidence, chunkID); err != nil {
				return fmt.Errorf("kg_store: insert edge %q→%q: %w", r.Src, r.Dst, err)
			}
			edges++
		}
		return nil
	})
	if err != nil {
		return created, deduped, edges, err
	}
	return created, deduped, edges, nil
}

// deleteKGForKB removes all kg_entities and kg_edges for a KB. The
// FK cascade from kg_entities → kg_edges on DELETE handles the
// edge side; we only have to delete the entity rows. Called when a
// KB is deleted (cascade-deleter wires this in alongside the chunk
// table cleanup) and when the operator opts to nuke the graph after
// a re-ingestion that materially changed the corpus.
//
//nolint:unused // kept for KB-delete cascade cleanup and operator-initiated graph nuke (see doc comment); not wired in yet
func (s *kgStore) deleteKGForKB(ctx context.Context, kbID string) error {
	if s == nil || s.pool == nil {
		return errors.New("kg_store: no DB pool configured")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM kg_entities WHERE kb_id = $1::uuid`, kbID)
	if err != nil {
		return fmt.Errorf("kg_store: delete by kb: %w", err)
	}
	return nil
}
