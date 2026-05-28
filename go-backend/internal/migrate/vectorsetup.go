package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/vector"
)

// sqlDBExec adapts *sql.DB to vector.ChunkTableExec so the boot-time and
// lazy insertion-time paths share the same DDL.
type sqlDBExec struct{ db *sql.DB }

func (e sqlDBExec) Begin(ctx context.Context) (vector.ChunkTableTx, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return sqlTxAdapter{tx: tx}, nil
}

type sqlTxAdapter struct{ tx *sql.Tx }

func (a sqlTxAdapter) Exec(ctx context.Context, query string) error {
	_, err := a.tx.ExecContext(ctx, query)
	return err
}

func (a sqlTxAdapter) Commit(_ context.Context) error   { return a.tx.Commit() }
func (a sqlTxAdapter) Rollback(_ context.Context) error { return a.tx.Rollback() }

// EnsureVectorTables queries the main DB for active embedding dimensions,
// then creates the corresponding vector tables and indexes in the vector DB.
// Tables that already exist are left untouched (IF NOT EXISTS).
func EnsureVectorTables(ctx context.Context, mainCfg, vectorCfg config.DBConfig) error {
	mainDB, err := openSQL(ctx, mainCfg)
	if err != nil {
		return fmt.Errorf("vector setup: open main db: %w", err)
	}
	defer mainDB.Close()

	vectorDB, err := openSQL(ctx, vectorCfg)
	if err != nil {
		return fmt.Errorf("vector setup: open vector db: %w", err)
	}
	defer vectorDB.Close()

	// Query active dimensions from ai_models.
	dims, err := queryActiveDimensions(ctx, mainDB)
	if err != nil {
		slog.Warn("could not query ai_models for dimensions (table might not exist yet)", "error", err)
		dims = nil
	}

	// Merge with defaults. 1536 is always required.
	dimSet := map[int]struct{}{1536: {}}
	for _, d := range dims {
		dimSet[d] = struct{}{}
	}

	// Also include dimensions for any existing document_chunks_<dim> table in
	// the vector DB. Catches the case where an admin removed/changed an
	// ai_models row but the chunk table from earlier ingestion still exists
	// (and would otherwise miss schema-extending ALTERs).
	existingDims, err := queryExistingChunkTableDimensions(ctx, vectorDB)
	if err != nil {
		slog.Warn("could not list existing chunk tables", "error", err)
	}
	for _, d := range existingDims {
		dimSet[d] = struct{}{}
	}

	exec := sqlDBExec{db: vectorDB}

	// Create the required default table first — fail hard if it cannot be created.
	if err := vector.EnsureChunkTable(ctx, exec, 1536); err != nil {
		return fmt.Errorf("create required default vector table: %w", err)
	}
	delete(dimSet, 1536)

	// Create remaining tables — log errors but continue.
	for d := range dimSet {
		if err := vector.EnsureChunkTable(ctx, exec, d); err != nil {
			slog.Error("failed to create vector table", "dimensions", d, "error", err)
		}
	}

	// user_memory lives in the MAIN db; align its embedding column with
	// the active embedder dim using the already-open mainDB connection.
	EnsureUserMemoryEmbedding(ctx, mainDB)

	return nil
}

// queryExistingChunkTableDimensions returns the dimension parsed from any
// existing document_chunks_<dim> table in the vector DB. The legacy
// "document_chunks" (1536) table is intentionally excluded — callers seed
// 1536 separately.
func queryExistingChunkTableDimensions(ctx context.Context, db *sql.DB) ([]int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT tablename FROM pg_tables
		 WHERE schemaname = 'public' AND tablename ~ '^document_chunks_[0-9]+$'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dims []int
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		// Parse the suffix.
		var d int
		if _, err := fmt.Sscanf(name, "document_chunks_%d", &d); err == nil && d > 0 {
			dims = append(dims, d)
		}
	}
	return dims, rows.Err()
}

func queryActiveDimensions(ctx context.Context, db *sql.DB) ([]int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT dimensions FROM ai_models WHERE dimensions IS NOT NULL AND dimensions > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dims []int
	for rows.Next() {
		var d int
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		dims = append(dims, d)
	}
	return dims, rows.Err()
}
