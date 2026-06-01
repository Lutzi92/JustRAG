package vector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GetHyPETableName returns the dim-keyed HyPE question table name,
// mirroring GetVectorTableName: bare for the 1536 default, suffixed
// otherwise.
func GetHyPETableName(dimensions int) string {
	if dimensions == 1536 {
		return "chunk_hype_questions"
	}
	return fmt.Sprintf("chunk_hype_questions_%d", dimensions)
}

// EnsureHyPETable creates the dim-keyed HyPE question table and its
// indexes if absent. Same advisory-lock + statement_timeout discipline
// as EnsureChunkTable; HyPE is vector-only (no tsvector / content).
func EnsureHyPETable(ctx context.Context, exec ChunkTableExec, dimensions int) error {
	tableName := GetHyPETableName(dimensions)
	slog.Info("ensuring HyPE table", "table", tableName, "dimensions", dimensions)

	tx, err := exec.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", tableName, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := tx.Exec(ctx, `SET LOCAL statement_timeout = 0`); err != nil {
		return fmt.Errorf("relax statement_timeout for %s: %w", tableName, err)
	}
	lockSQL := fmt.Sprintf(
		`SELECT pg_advisory_xact_lock(hashtext('justrag.ensure_hype_table:%s')::bigint)`,
		tableName,
	)
	if err := tx.Exec(ctx, lockSQL); err != nil {
		return fmt.Errorf("acquire advisory lock for %s: %w", tableName, err)
	}
	if err := tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return fmt.Errorf("enable vector extension: %w", err)
	}

	createSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS "%s" (
			"id"              uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
			"kb_id"           uuid NOT NULL,
			"file_id"         uuid NOT NULL,
			"parent_chunk_id" uuid NOT NULL,
			"question"        text NOT NULL,
			"embedding"       vector(%d),
			"created_at"      timestamp DEFAULT now() NOT NULL
		)`, tableName, dimensions)
	if err := tx.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("create table %s: %w", tableName, err)
	}
	embDimSQL := fmt.Sprintf(
		`ALTER TABLE "%s" ALTER COLUMN "embedding" SET DATA TYPE vector(%d)`,
		tableName, dimensions)
	if err := tx.Exec(ctx, embDimSQL); err != nil {
		return fmt.Errorf("set embedding dimensions on %s: %w", tableName, err)
	}

	for _, idx := range []struct{ name, cols string }{
		{"kb_id_idx", "kb_id"},
		{"file_id_idx", "file_id"},
		{"parent_chunk_id_idx", "parent_chunk_id"},
		{"kb_file_idx", "kb_id, file_id"},
	} {
		idxSQL := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS "%s_%s" ON "%s" (%s)`,
			tableName, idx.name, tableName, idx.cols)
		if err := tx.Exec(ctx, idxSQL); err != nil {
			return fmt.Errorf("create index %s on %s: %w", idx.name, tableName, err)
		}
	}

	if dimensions <= hnswMaxDimensions {
		hnswSQL := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS "%s_embedding_idx" ON "%s" USING hnsw ("embedding" vector_cosine_ops) WITH (m = %d, ef_construction = %d)`,
			tableName, tableName, hnswM, hnswEfConstruction)
		if err := tx.Exec(ctx, hnswSQL); err != nil {
			return fmt.Errorf("create hnsw index for %s: %w", tableName, err)
		}
	} else if dimensions <= halfvecMaxDimensions {
		hnswSQL := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS "%s_embedding_halfvec_idx" ON "%s" USING hnsw ((embedding::halfvec(%d)) halfvec_cosine_ops) WITH (m = %d, ef_construction = %d)`,
			tableName, tableName, dimensions, hnswM, hnswEfConstruction)
		if err := tx.Exec(ctx, hnswSQL); err != nil {
			return fmt.Errorf("create halfvec hnsw index for %s: %w", tableName, err)
		}
	} else {
		slog.Warn("skipping HyPE HNSW index (dimensions exceed halfvec limit)", "table", tableName, "dimensions", dimensions)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ensure hype table %s: %w", tableName, err)
	}
	slog.Info("HyPE table ready", "table", tableName)
	return nil
}

// ---------------------------------------------------------------------------
// HyPEStore — vector-pool I/O for the HyPE question tables
// ---------------------------------------------------------------------------

// HyPEStore owns HyPE-question DB I/O against the vector pool.
type HyPEStore struct {
	pool *pgxpool.Pool
}

// NewHyPEStore creates a HyPEStore backed by the given vector connection pool.
func NewHyPEStore(pool *pgxpool.Pool) *HyPEStore { return &HyPEStore{pool: pool} }

// Insert writes the questions + their embeddings for one parent chunk.
// len(questions) must equal len(embeddings); mismatched or empty input
// is a no-op. dimensions selects the dim-keyed table.
func (h *HyPEStore) Insert(ctx context.Context, kbID, fileID, parentChunkID string, questions []string, embeddings [][]float64, dimensions int) error {
	if len(questions) == 0 || len(questions) != len(embeddings) {
		return nil
	}
	table := GetHyPETableName(dimensions)
	if !validVectorTable.MatchString(table) {
		return fmt.Errorf("hype insert: invalid table %q", table)
	}
	args := make([]any, 0, len(questions)*5)
	var vb strings.Builder
	for i := range questions {
		if i > 0 {
			vb.WriteByte(',')
		}
		base := i * 5
		fmt.Fprintf(&vb, "($%d::uuid,$%d::uuid,$%d::uuid,$%d,$%d::vector)",
			base+1, base+2, base+3, base+4, base+5)
		args = append(args, kbID, fileID, parentChunkID, questions[i], formatEmbedding(embeddings[i]))
	}
	sql := fmt.Sprintf(
		`INSERT INTO "%s" (kb_id, file_id, parent_chunk_id, question, embedding) VALUES %s`,
		table, vb.String())
	if _, err := h.pool.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("hype insert into %s: %w", table, err)
	}
	return nil
}

// Search matches the (already-formatted) query embedding against the
// HyPE question table and returns up to `limit` DISTINCT parent chunk
// IDs ranked by question-vector similarity. embStr is a pgvector
// literal (formatEmbedding output). A missing table returns (nil, nil)
// so the caller fails open.
func (h *HyPEStore) Search(ctx context.Context, embStr, kbID string, fileIDs []string, limit, dimensions int) ([]string, error) {
	table := GetHyPETableName(dimensions)
	if !validVectorTable.MatchString(table) {
		return nil, fmt.Errorf("hype search: invalid table %q", table)
	}
	opExpr := `embedding <=> $1::vector`
	if dimensions > hnswMaxDimensions && dimensions <= halfvecMaxDimensions {
		opExpr = fmt.Sprintf(`embedding::halfvec(%d) <=> $1::halfvec(%d)`, dimensions, dimensions)
	}
	args := []any{embStr, kbID}
	fileFilter := ""
	if len(fileIDs) > 0 {
		fileFilter = " AND file_id = ANY($3::uuid[])"
		args = append(args, fileIDs)
	}
	sql := fmt.Sprintf(`
		SELECT parent_chunk_id::text
		FROM (
			SELECT DISTINCT ON (parent_chunk_id) parent_chunk_id, %s AS dist
			FROM "%s"
			WHERE kb_id = $2::uuid%s
			ORDER BY parent_chunk_id, dist
		) t
		ORDER BY t.dist
		LIMIT %d`, opExpr, table, fileFilter, limit)
	rows, err := h.pool.Query(ctx, sql, args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, nil
		}
		return nil, fmt.Errorf("hype search %s: %w", table, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("hype search scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteByFileIDsAllDims removes HyPE rows for the given files across
// every dim-keyed HyPE table named by dims. Best-effort per table: a
// missing table (relation does not exist) is ignored.
func (h *HyPEStore) DeleteByFileIDsAllDims(ctx context.Context, fileIDs []string, dims []int) error {
	if len(fileIDs) == 0 {
		return nil
	}
	for _, d := range dims {
		table := GetHyPETableName(d)
		if !validVectorTable.MatchString(table) {
			continue
		}
		sql := fmt.Sprintf(`DELETE FROM "%s" WHERE file_id = ANY($1::uuid[])`, table)
		if _, err := h.pool.Exec(ctx, sql, fileIDs); err != nil {
			if !strings.Contains(err.Error(), "does not exist") {
				return fmt.Errorf("hype delete from %s: %w", table, err)
			}
		}
	}
	return nil
}
