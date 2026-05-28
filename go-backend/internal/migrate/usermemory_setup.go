package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

const (
	userMemoryHNSWM              = 16
	userMemoryHNSWEfConstruction = 64
)

// activeEmbeddingDimSQL resolves the dimension of the embedding model that
// ai.ConfigResolver.Resolve(ctx, "") would pick: the active provider's
// first embedding-capable model, ordered by creation time.
const activeEmbeddingDimSQL = `
	SELECT am.dimensions
	  FROM ai_models am
	  JOIN ai_providers ap ON am.provider_id = ap.id
	 WHERE ap.is_active = true
	   AND am.is_embedding = true
	   AND am.dimensions > 0
	 ORDER BY am.created_at ASC
	 LIMIT 1`

// parseVectorDim extracts N from a "vector(N)" format_type string. Returns
// 0 for dimensionless or non-vector types.
func parseVectorDim(formatType string) int {
	var dim int
	if _, err := fmt.Sscanf(formatType, "vector(%d)", &dim); err != nil || dim < 1 {
		return 0
	}
	return dim
}

// userMemoryHNSWSQL returns the CREATE INDEX statement for the user_memory
// embedding column at the given dim, mirroring internal/vector/schema.go's
// three dim bands (pgvector HNSW caps: 2000 for vector, 4000 for halfvec):
//
//	dim <= 2000        -> plain hnsw (embedding vector_cosine_ops)
//	2000 < dim <= 4000 -> hnsw on embedding::halfvec(dim) (halfvec_cosine_ops)
//	dim > 4000         -> "" (no HNSW index — exceeds pgvector's halfvec
//	                      limit; like schema.go, we skip it. Semantic recall
//	                      then runs a filtered seq scan, which is cheap for
//	                      the small per-user row set the candidate CTE scans.)
//
// The partial-index predicate (non-superseded rows only) is preserved.
func userMemoryHNSWSQL(dim int) string {
	switch {
	case dim <= 2000:
		return fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS "user_memory_embedding_idx" ON "user_memory" `+
				`USING hnsw ("embedding" vector_cosine_ops) `+
				`WITH (m = %d, ef_construction = %d) WHERE "superseded_by" IS NULL`,
			userMemoryHNSWM, userMemoryHNSWEfConstruction)
	case dim <= 4000:
		return fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS "user_memory_embedding_idx" ON "user_memory" `+
				`USING hnsw ((embedding::halfvec(%d)) halfvec_cosine_ops) `+
				`WITH (m = %d, ef_construction = %d) WHERE "superseded_by" IS NULL`,
			dim, userMemoryHNSWM, userMemoryHNSWEfConstruction)
	default:
		return ""
	}
}

// EnsureUserMemoryEmbedding widens user_memory.embedding to the active
// embedder's dimension when they differ. DROP INDEX → ALTER → CREATE INDEX
// (old vectors discarded; the re-embed-user-memory job repopulates).
// Fails safe: any resolution/DDL problem logs a warning and leaves the
// column as-is.
func EnsureUserMemoryEmbedding(ctx context.Context, mainDB *sql.DB) {
	var target int
	if err := mainDB.QueryRowContext(ctx, activeEmbeddingDimSQL).Scan(&target); err != nil {
		if err == sql.ErrNoRows {
			slog.Info("user_memory ensure: no active embedding model; leaving column as-is")
			return
		}
		slog.Warn("user_memory ensure: could not resolve active embedding dim; skipping", "error", err)
		return
	}

	var currentFT string
	err := mainDB.QueryRowContext(ctx, `
		SELECT format_type(a.atttypid, a.atttypmod)
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		 WHERE c.relname = 'user_memory' AND a.attname = 'embedding' AND NOT a.attisdropped
	`).Scan(&currentFT)
	if err != nil {
		slog.Warn("user_memory ensure: could not read current column type (table missing?); skipping", "error", err)
		return
	}
	current := parseVectorDim(currentFT)
	if current == target {
		return
	}

	slog.Info("user_memory ensure: re-dimensioning embedding column", "from", current, "to", target)

	tx, err := mainDB.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("user_memory ensure: begin tx failed; skipping", "error", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('justrag.ensure_user_memory'))`); err != nil {
		slog.Warn("user_memory ensure: advisory lock failed; skipping", "error", err)
		return
	}
	stmts := []string{
		`DROP INDEX IF EXISTS "user_memory_embedding_idx"`,
		fmt.Sprintf(`ALTER TABLE "user_memory" ALTER COLUMN "embedding" SET DATA TYPE vector(%d)`, target),
	}
	if idxSQL := userMemoryHNSWSQL(target); idxSQL != "" {
		stmts = append(stmts, idxSQL)
	} else {
		// >4000 dims: no HNSW index (mirrors schema.go). Recall falls back
		// to a filtered seq scan over the small per-user row set.
		slog.Warn("user_memory ensure: skipping HNSW index (dimensions exceed halfvec limit); semantic recall uses a filtered seq scan",
			"dimensions", target)
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			slog.Warn("user_memory ensure: DDL failed; rolling back", "stmt", s, "error", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("user_memory ensure: commit failed", "error", err)
		return
	}
	slog.Info("user_memory ensure: embedding column re-dimensioned; run the re-embed-user-memory maintenance job to repopulate", "dim", target)
}
