package vector

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	hnswM                = 24
	hnswEfConstruction   = 128
	hnswMaxDimensions    = 2000
	halfvecMaxDimensions = 4000
)

// chunkTableBackfillColumns is the single source of truth for columns added
// to dim-keyed chunk tables (document_chunks_<dim>) after their initial
// CREATE TABLE. The goose migrations under migrations/main/ only patch the
// default 1536-dim `document_chunks` table; this slice keeps every other
// dim-keyed table in sync by running ADD COLUMN IF NOT EXISTS at boot.
//
// When adding a new column to document_chunks via a goose migration, append
// the matching entry here so non-default dimension tables get the same
// column on the next worker startup.
var chunkTableBackfillColumns = []struct {
	name string
	ddl  string
}{
	// Goose 0007 — cross-file dedup hash.
	{"content_hash", "text"},
	// Goose 0008 — Anthropic-style contextual prefix.
	{"contextual_prefix", "text"},
	// Goose 0009 — MRL low-dim projection (two-pass vector search).
	{"embedding_low", "vector(256)"},
	// Goose 0010 — parent_child chunking.
	{"parent_chunk_id", "uuid"},
	// Goose 0012 — dual-arm BM25 (simple-config tsvector).
	{"vector_index_simple", "tsvector"},
	// Goose 0046 — RAPTOR hierarchical indexing.
	{"node_kind", "text NOT NULL DEFAULT 'leaf'"},
	{"tree_level", "smallint NOT NULL DEFAULT 0"},
	{"raptor_parent_id", "uuid"},
}

// chunkTableBackfillIndexes is the single source of truth for indexes
// created on dim-keyed chunk tables after their CREATE TABLE. Same
// rationale as chunkTableBackfillColumns: the goose migrations only patch
// the default 1536-dim table; non-default tables get the same indexes on
// every worker startup.
var chunkTableBackfillIndexes = []struct {
	name string
	cols string
}{
	{"kb_id_idx", "kb_id"},
	{"file_id_idx", "file_id"},
	{"kb_id_created_at_idx", "kb_id, created_at"},
	{"kb_content_hash_idx", "kb_id, content_hash"},
	{"parent_chunk_id_idx", "parent_chunk_id"},
	{"raptor_parent_idx", "raptor_parent_id"},
	{"kind_kb_file_idx", "kb_id, file_id, node_kind"},
}

// ChunkTableExec is the minimal capability EnsureChunkTable needs from a
// connection pool: the ability to start a transaction. All DDL runs inside
// that transaction so a per-table advisory lock can serialise concurrent
// callers (multiple worker replicas booting at once race on
// `CREATE INDEX IF NOT EXISTS` — Postgres's pg_class existence check is not
// atomic with index creation, and concurrent creators get a duplicate-key
// error on pg_class_relname_nsp_index).
type ChunkTableExec interface {
	Begin(ctx context.Context) (ChunkTableTx, error)
}

// ChunkTableTx is the per-tx exec surface used by EnsureChunkTable.
type ChunkTableTx interface {
	Exec(ctx context.Context, query string) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// PgxpoolExec adapts *pgxpool.Pool to ChunkTableExec.
type PgxpoolExec struct{ Pool *pgxpool.Pool }

func (e PgxpoolExec) Begin(ctx context.Context) (ChunkTableTx, error) {
	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgxTxAdapter{tx: tx}, nil
}

type pgxTxAdapter struct{ tx pgx.Tx }

func (a pgxTxAdapter) Exec(ctx context.Context, query string) error {
	_, err := a.tx.Exec(ctx, query)
	return err
}

func (a pgxTxAdapter) Commit(ctx context.Context) error   { return a.tx.Commit(ctx) }
func (a pgxTxAdapter) Rollback(ctx context.Context) error { return a.tx.Rollback(ctx) }

// EnsureChunkTable creates the dimension-versioned chunk table and its
// indexes if they do not already exist. All DDL runs inside a single
// transaction guarded by a per-table advisory lock, so concurrent
// invocations from multiple worker replicas (or a worker racing the
// migrate container) serialise instead of fighting on pg_class.
func EnsureChunkTable(ctx context.Context, exec ChunkTableExec, dimensions int) error {
	tableName := GetVectorTableName(dimensions)
	slog.Info("ensuring vector table", "table", tableName, "dimensions", dimensions)

	tx, err := exec.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", tableName, err)
	}
	// Rollback is a no-op once Commit succeeds.
	defer func() { _ = tx.Rollback(ctx) }()

	// HNSW index builds can exceed the connection's default statement_timeout
	// (120s in this project, see internal/database/database.go) on large
	// pre-existing tables. Boot-time DDL is allowed to take as long as it
	// needs; SET LOCAL is scoped to this tx.
	if err := tx.Exec(ctx, `SET LOCAL statement_timeout = 0`); err != nil {
		return fmt.Errorf("relax statement_timeout for %s: %w", tableName, err)
	}

	// Per-table advisory lock — auto-released on commit/rollback. The hash
	// keys both the namespace and the table name to keep the lock key space
	// distinct from any other application-level lock that might use a bare
	// table-name hash.
	//
	// fmt.Sprintf with tableName is safe here: tableName comes from
	// vector.GetVectorTableName(int) which returns "document_chunks_<int>" —
	// no user input ever reaches this string. The interpolation is inside a
	// SQL string literal that hashtext() consumes, not a quoted identifier.
	lockSQL := fmt.Sprintf(
		`SELECT pg_advisory_xact_lock(hashtext('justrag.ensure_chunk_table:%s')::bigint)`,
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
			"id"                uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
			"kb_id"             uuid NOT NULL,
			"file_id"           uuid NOT NULL,
			"content"           text NOT NULL,
			"contextual_prefix" text,
			"content_hash"      text,
			"embedding"         vector(%d),
			"embedding_low"     vector(256),
			"vector_index"      tsvector,
			"metadata"          jsonb,
			"created_at"        timestamp DEFAULT now() NOT NULL
		)`, tableName, dimensions)
	if err := tx.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("create table %s: %w", tableName, err)
	}

	// Vector migration 0002 historically stripped the (1536) constraint from
	// the legacy document_chunks.embedding column to allow heterogeneous
	// embedding sizes in one table. The current schema gives each dimension
	// its own table (document_chunks_<dim>), so the legacy 1536 table should
	// be dimensioned again — otherwise the HNSW index below fails with
	// "column does not have dimensions". The ALTER is guarded behind a
	// catalog type check (see conditionalRedimEmbeddingSQL): a bare ALTER is
	// NOT a no-op when the type already matches and rewrote the whole table
	// (incl. HNSW rebuild, under ACCESS EXCLUSIVE) on every worker startup.
	if err := tx.Exec(ctx, conditionalRedimEmbeddingSQL(tableName, dimensions)); err != nil {
		return fmt.Errorf("set embedding dimensions on %s: %w", tableName, err)
	}

	// Backfill columns added by goose migrations on dim-keyed tables. The
	// goose track only ALTERs the default 1536-dim `document_chunks`; this
	// loop is what keeps every non-default dim table in sync. The column
	// list is defined once at the top of this file (chunkTableBackfillColumns)
	// — append there when a new goose migration adds a column.
	//
	// SQL injection invariant: tableName comes from GetVectorTableName(int);
	// col.name and col.ddl are package-level constants.
	for _, col := range chunkTableBackfillColumns {
		alterSQL := fmt.Sprintf(`ALTER TABLE "%s" ADD COLUMN IF NOT EXISTS "%s" %s`,
			tableName, col.name, col.ddl)
		if err := tx.Exec(ctx, alterSQL); err != nil {
			return fmt.Errorf("add %s to %s: %w", col.name, tableName, err)
		}
	}

	for _, idx := range chunkTableBackfillIndexes {
		idxSQL := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS "%s_%s" ON "%s" (%s)`,
			tableName, idx.name, tableName, idx.cols)
		if err := tx.Exec(ctx, idxSQL); err != nil {
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}

	ginSQL := fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS "%s_vector_index_idx" ON "%s" USING gin ("vector_index")`,
		tableName, tableName)
	if err := tx.Exec(ctx, ginSQL); err != nil {
		return fmt.Errorf("create gin index: %w", err)
	}

	ginSimpleSQL := fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS "%s_vector_index_simple_idx" ON "%s" USING gin ("vector_index_simple")`,
		tableName, tableName)
	if err := tx.Exec(ctx, ginSimpleSQL); err != nil {
		return fmt.Errorf("create gin index (simple): %w", err)
	}

	// HNSW indexes — race-prone before the advisory lock above; now safe to
	// fail-fast instead of warn-and-continue.
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
		slog.Warn("skipping HNSW index (dimensions exceed halfvec limit)", "table", tableName, "dimensions", dimensions)
	}

	// HNSW index on the MRL-truncated 256-dim projection. Used by the
	// two-pass retrieval path when `mrl_two_pass_enabled` is true. Always
	// regular `vector_cosine_ops` — 256 dims is well under the 2000-dim
	// halfvec threshold, so we don't need the halfvec workaround that the
	// full-dim index uses for dim > 2000.
	embLowHNSW := fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS "%s_embedding_low_idx" ON "%s" USING hnsw ("embedding_low" vector_cosine_ops) WITH (m = %d, ef_construction = %d)`,
		tableName, tableName, hnswM, hnswEfConstruction)
	if err := tx.Exec(ctx, embLowHNSW); err != nil {
		return fmt.Errorf("create embedding_low hnsw index for %s: %w", tableName, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ensure chunk table %s: %w", tableName, err)
	}

	slog.Info("vector table ready", "table", tableName)
	return nil
}
