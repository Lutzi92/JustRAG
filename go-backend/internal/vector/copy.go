package vector

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CopyResult maps donor chunk ids to the new target chunk ids (leaves and
// RAPTOR summaries), for KG replay and tests.
type CopyResult struct {
	ChunkIDMap map[string]string
	// Dimensions is the dim table the copy wrote to (0 if the donor had no
	// chunks). For a (abnormal) multi-dim donor it is the last dim written.
	Dimensions int
}

// copyAfterChunkMapHook is a test seam: when set, CopyFileIndex calls it
// inside the transaction after a dim's chunk_map is built and before its
// chunks are inserted (integration tests simulate a donor re-ingest there).
// Always nil in production.
var copyAfterChunkMapHook func(ctx context.Context, tx pgx.Tx, chunkTable string) error

// CopyFileIndex copies every vector-DB row of srcFileID (chunks incl. RAPTOR
// summaries, parent chunks, HyPE questions) into dstFileID/dstKbID inside ONE
// transaction on the vector pool, remapping every internal reference
// (parent_chunk_id, raptor_parent_id, HyPE parent_chunk_id) to the new ids so
// no target row points at a donor row. Existing rows of dstFileID are deleted
// first, which makes a retry idempotent. The donor's rows are never modified.
//
// vector_index is recomputed with pgConfig (the TARGET KB's text-search
// config) and vector_index_simple with 'simple', using the same expressions as
// insertRowSQLTemplate (an empty pgConfig falls back to 'simple', like
// AddDocumentChunks); copies to the same target file serialise on an advisory
// transaction lock; embeddings and created_at are copied verbatim.
func (s *ChunkService) CopyFileIndex(ctx context.Context, srcFileID, dstFileID, dstKbID, pgConfig string) (CopyResult, error) {
	res := CopyResult{ChunkIDMap: map[string]string{}}
	if pgConfig == "" {
		pgConfig = "simple"
	}
	if srcFileID == "" || dstFileID == "" || dstKbID == "" {
		return res, fmt.Errorf("CopyFileIndex: source file, target file and target kb are required")
	}
	if srcFileID == dstFileID {
		return res, fmt.Errorf("CopyFileIndex: source and target file must differ")
	}

	dims, err := s.ListChunkTableDimensions(ctx)
	if err != nil {
		return res, err
	}

	tx, err := s.vectorDB.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("CopyFileIndex: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialise concurrent copies to one target (READ COMMITTED would let two
	// delete-then-insert sequences interleave and double the rows).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('justrag.copy_file_index:' || $1::text, 0))`, dstFileID); err != nil {
		return res, fmt.Errorf("CopyFileIndex: lock: %w", err)
	}

	// Mapping tables live for this transaction only. chunk_map spans all
	// dims (ids are UUIDs, so a join from one dim table can only match its
	// own rows).
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE chunk_map (old_id uuid PRIMARY KEY, new_id uuid NOT NULL) ON COMMIT DROP`); err != nil {
		return res, fmt.Errorf("CopyFileIndex: create chunk_map: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE parent_map (old_id uuid PRIMARY KEY, new_id uuid NOT NULL) ON COMMIT DROP`); err != nil {
		return res, fmt.Errorf("CopyFileIndex: create parent_map: %w", err)
	}

	// 1. Clear any previous attempt at the target. Table names come only
	// from GetVectorTableName / GetHyPETableName (int-derived); do not copy
	// this Sprintf shape with request data.
	hypeExists := map[int]bool{}
	for _, d := range dims {
		chunkT := GetVectorTableName(d)
		hypeT := GetHyPETableName(d)
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM "%s" WHERE file_id = $1::uuid`, chunkT), dstFileID); err != nil {
			return res, fmt.Errorf("CopyFileIndex: clear target %s: %w", chunkT, err)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, hypeT).Scan(&exists); err != nil {
			return res, fmt.Errorf("CopyFileIndex: probe %s: %w", hypeT, err)
		}
		hypeExists[d] = exists
		if exists {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM "%s" WHERE file_id = $1::uuid`, hypeT), dstFileID); err != nil {
				return res, fmt.Errorf("CopyFileIndex: clear target %s: %w", hypeT, err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM document_chunk_parents WHERE file_id = $1::uuid`, dstFileID); err != nil {
		return res, fmt.Errorf("CopyFileIndex: clear target parents: %w", err)
	}

	// 2. Parents.
	if _, err := tx.Exec(ctx,
		`INSERT INTO parent_map (old_id, new_id)
		 SELECT id, gen_random_uuid() FROM document_chunk_parents WHERE file_id = $1::uuid`, srcFileID); err != nil {
		return res, fmt.Errorf("CopyFileIndex: map parents: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO document_chunk_parents (id, kb_id, file_id, content, contextual_prefix, metadata, created_at)
		 SELECT pm.new_id, $1::uuid, $2::uuid, p.content, p.contextual_prefix, p.metadata, p.created_at
		   FROM document_chunk_parents p
		   JOIN parent_map pm ON pm.old_id = p.id`, dstKbID, dstFileID); err != nil {
		return res, fmt.Errorf("CopyFileIndex: copy parents: %w", err)
	}

	// 3. Chunks (+ 4. HyPE), per dim table that holds donor rows.
	for _, d := range dims {
		chunkT := GetVectorTableName(d)
		tag, err := tx.Exec(ctx, fmt.Sprintf(
			`INSERT INTO chunk_map (old_id, new_id)
			 SELECT id, gen_random_uuid() FROM "%s" WHERE file_id = $1::uuid`, chunkT), srcFileID)
		if err != nil {
			return res, fmt.Errorf("CopyFileIndex: map chunks in %s: %w", chunkT, err)
		}
		mapped := tag.RowsAffected()
		if mapped == 0 {
			continue
		}
		res.Dimensions = d
		if copyAfterChunkMapHook != nil {
			if err := copyAfterChunkMapHook(ctx, tx, chunkT); err != nil {
				return res, err
			}
		}

		insertChunks := fmt.Sprintf(`
			INSERT INTO "%[1]s" (id, kb_id, file_id, content, contextual_prefix, content_hash, embedding, embedding_low,
			                     metadata, parent_chunk_id, node_kind, tree_level, raptor_parent_id, created_at,
			                     vector_index, vector_index_simple)
			SELECT cm.new_id, $2::uuid, $3::uuid, c.content, c.contextual_prefix, c.content_hash, c.embedding, c.embedding_low,
			       c.metadata, pm.new_id, c.node_kind, c.tree_level, rm.new_id, c.created_at,
			       to_tsvector($4::regconfig, COALESCE(c.contextual_prefix, '') || ' ' || c.content),
			       to_tsvector('simple',      COALESCE(c.contextual_prefix, '') || ' ' || c.content)
			  FROM "%[1]s" c
			  JOIN chunk_map cm ON cm.old_id = c.id
			  LEFT JOIN parent_map pm ON pm.old_id = c.parent_chunk_id
			  LEFT JOIN chunk_map rm ON rm.old_id = c.raptor_parent_id
			 WHERE c.file_id = $1::uuid`, chunkT)
		ins, err := tx.Exec(ctx, insertChunks, srcFileID, dstKbID, dstFileID, pgConfig)
		if err != nil {
			return res, fmt.Errorf("CopyFileIndex: copy chunks in %s: %w", chunkT, err)
		}
		// READ COMMITTED: each statement sees a fresh snapshot, so donor
		// rows deleted by a concurrent re-ingest after chunk_map was built
		// silently drop out of the insert. Refuse a partial copy (rollback);
		// the worker cleans the target and ingests instead.
		if ins.RowsAffected() != mapped {
			return res, fmt.Errorf("CopyFileIndex: donor changed during copy in %s: mapped %d chunks, copied %d",
				chunkT, mapped, ins.RowsAffected())
		}

		if !hypeExists[d] {
			continue
		}
		hypeT := GetHyPETableName(d)
		insertHype := fmt.Sprintf(`
			INSERT INTO "%[1]s" (id, kb_id, file_id, parent_chunk_id, question, embedding, created_at)
			SELECT gen_random_uuid(), $2::uuid, $3::uuid, cm.new_id, h.question, h.embedding, h.created_at
			  FROM "%[1]s" h
			  JOIN chunk_map cm ON cm.old_id = h.parent_chunk_id
			 WHERE h.file_id = $1::uuid`, hypeT)
		if _, err := tx.Exec(ctx, insertHype, srcFileID, dstKbID, dstFileID); err != nil {
			return res, fmt.Errorf("CopyFileIndex: copy hype rows in %s: %w", hypeT, err)
		}
	}

	rows, err := tx.Query(ctx, `SELECT old_id::text, new_id::text FROM chunk_map`)
	if err != nil {
		return res, fmt.Errorf("CopyFileIndex: read chunk_map: %w", err)
	}
	for rows.Next() {
		var o, n string
		if err := rows.Scan(&o, &n); err != nil {
			rows.Close()
			return res, fmt.Errorf("CopyFileIndex: scan chunk_map: %w", err)
		}
		res.ChunkIDMap[o] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("CopyFileIndex: read chunk_map: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return CopyResult{ChunkIDMap: map[string]string{}}, fmt.Errorf("CopyFileIndex: commit: %w", err)
	}
	return res, nil
}
