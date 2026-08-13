// Package cascade provides a CascadeDeleter that cleans up all assets
// (vector chunks, storage files, and database rows) associated with a
// knowledge base or user before removing the root record.
package cascade

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/vector"
)

// fileRecord holds the minimal file info needed for cleanup.
type fileRecord struct {
	ID          string
	StoragePath *string
}

// QueryCacheInvalidator nukes cached SearchResults for a KB. Wired through
// *vector.SearchService in production. Optional — a nil invalidator skips
// the call.
type QueryCacheInvalidator interface {
	InvalidateQueryCache(ctx context.Context, kbID string) error
}

// Deleter encapsulates the dependencies needed for cascade deletion.
type Deleter struct {
	mainDB       *pgxpool.Pool
	chunkService *vector.ChunkService
	hype         *vector.HyPEStore
	storage      storage.Storage
	queryCache   QueryCacheInvalidator
}

// New creates a Deleter backed by the given pools and storage.
func New(mainDB *pgxpool.Pool, vectorDB *pgxpool.Pool, stor storage.Storage) *Deleter {
	return &Deleter{
		mainDB:       mainDB,
		chunkService: vector.NewChunkService(vectorDB),
		hype:         vector.NewHyPEStore(vectorDB),
		storage:      stor,
	}
}

// SetQueryCacheInvalidator injects the search service's query-cache
// invalidator so cascade deletes nuke cached SearchResults for the KB
// being torn down. Optional — when nil, the cache is left to its TTL
// fallback.
func (d *Deleter) SetQueryCacheInvalidator(qc QueryCacheInvalidator) { d.queryCache = qc }

// invalidateQueryCache fires the optional KB query-cache invalidation
// hook. Fail-safe: nil invalidator is a no-op; errors are logged but
// never propagated.
func (d *Deleter) invalidateQueryCache(ctx context.Context, kbID, reason string) {
	if d.queryCache == nil || kbID == "" {
		return
	}
	if err := d.queryCache.InvalidateQueryCache(ctx, kbID); err != nil {
		slog.WarnContext(ctx, "query_cache: invalidate failed",
			"kb_id", kbID, "reason", reason, "error", err)
	}
}

// ---------------------------------------------------------------------------
// DeleteKB removes a single knowledge base and all its associated assets.
//
// Flow:
//  1. Fetch all files for the KB.
//  2. For each file: delete vector chunks (best-effort), delete from storage (best-effort).
//  3. In a single transaction: delete files, chats (cascades messages via FK),
//     generated content, shares, global KB editor entries, KB record.
//
// Returns an error only if the DB transaction fails.
// ---------------------------------------------------------------------------
func (d *Deleter) DeleteKB(ctx context.Context, kbID string) error {
	files, err := d.getFilesByKBID(ctx, kbID)
	if err != nil {
		return fmt.Errorf("cascade DeleteKB: fetch files: %w", err)
	}

	d.deleteVectorChunksForFiles(ctx, files)
	d.dropTabularTablesForFiles(ctx, files)
	d.deleteStorageForFiles(ctx, files)

	if err := d.deleteKBTransaction(ctx, kbID); err != nil {
		return err
	}
	d.invalidateQueryCache(ctx, kbID, "kb_deleted")
	return nil
}

// ---------------------------------------------------------------------------
// DeleteUser removes a user and all KBs they own, including all assets.
//
// Flow:
//  1. Fetch all KBs owned by the user.
//  2. For each KB: fetch its files, delete vector chunks + storage (best-effort).
//  3. In a single transaction:
//     - Delete files, chats, generated content, shares for all owned KBs.
//     - Delete the KB records.
//     - Delete the user's share entries in other KBs.
//     - Delete the user record.
//
// Returns an error only if the DB transaction fails.
// ---------------------------------------------------------------------------
func (d *Deleter) DeleteUser(ctx context.Context, userID string) error {
	kbIDs, err := d.getKBIDsByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("cascade DeleteUser: fetch KBs: %w", err)
	}

	for _, kbID := range kbIDs {
		files, err := d.getFilesByKBID(ctx, kbID)
		if err != nil {
			slog.WarnContext(ctx, "cascade DeleteUser: fetch files for KB (skipping)",
				"kb_id", kbID, "error", err)
			continue
		}
		d.deleteVectorChunksForFiles(ctx, files)
		d.dropTabularTablesForFiles(ctx, files)
		d.deleteStorageForFiles(ctx, files)
	}

	if err := d.deleteUserTransaction(ctx, userID, kbIDs); err != nil {
		return err
	}
	for _, kbID := range kbIDs {
		d.invalidateQueryCache(ctx, kbID, "user_deleted")
	}
	return nil
}

// ---------------------------------------------------------------------------
// DeleteGlobalKB removes a global knowledge base and all its assets.
//
// Flow:
//  1. Fetch all files for the KB.
//  2. Delete vector chunks + storage (best-effort).
//  3. In a single transaction: delete editors, shares, files, chats,
//     generated content, KB record.
//
// Returns an error only if the DB transaction fails.
// ---------------------------------------------------------------------------
func (d *Deleter) DeleteGlobalKB(ctx context.Context, kbID string) error {
	files, err := d.getFilesByKBID(ctx, kbID)
	if err != nil {
		return fmt.Errorf("cascade DeleteGlobalKB: fetch files: %w", err)
	}

	d.deleteVectorChunksForFiles(ctx, files)
	d.dropTabularTablesForFiles(ctx, files)
	d.deleteStorageForFiles(ctx, files)

	if err := d.deleteGlobalKBTransaction(ctx, kbID); err != nil {
		return err
	}
	d.invalidateQueryCache(ctx, kbID, "global_kb_deleted")
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers — DB queries
// ---------------------------------------------------------------------------

func (d *Deleter) getFilesByKBID(ctx context.Context, kbID string) ([]fileRecord, error) {
	rows, err := d.mainDB.Query(ctx,
		`SELECT id, storage_path FROM files WHERE kb_id = $1`, kbID)
	if err != nil {
		return nil, fmt.Errorf("query files for kb %s: %w", kbID, err)
	}
	defer rows.Close()

	var files []fileRecord
	for rows.Next() {
		var f fileRecord
		if err := rows.Scan(&f.ID, &f.StoragePath); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (d *Deleter) getKBIDsByUserID(ctx context.Context, userID string) ([]string, error) {
	rows, err := d.mainDB.Query(ctx,
		`SELECT id FROM knowledge_bases WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("query KBs for user %s: %w", userID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan kb id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---------------------------------------------------------------------------
// Internal helpers — best-effort cleanup
// ---------------------------------------------------------------------------

func (d *Deleter) deleteVectorChunksForFiles(ctx context.Context, files []fileRecord) {
	if len(files) == 0 {
		return
	}
	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	if err := d.chunkService.DeleteChunksByFileIDsAllDims(ctx, ids); err != nil {
		observability.RecordCascadeDeletionError(observability.CascadeResourceVector)
		slog.WarnContext(ctx, "cascade: delete vector chunks (best-effort) — orphan chunks possible",
			"file_count", len(ids), "error", err)
	}
	if d.hype != nil {
		dims, derr := d.chunkService.ListChunkTableDimensions(ctx)
		if derr != nil {
			slog.WarnContext(ctx, "cascade: list dims for hype cleanup failed (best-effort)", "error", derr)
		} else if err := d.hype.DeleteByFileIDsAllDims(ctx, ids, dims); err != nil {
			slog.WarnContext(ctx, "cascade: delete hype rows (best-effort) — orphan rows possible",
				"file_count", len(ids), "error", err)
		}
	}
}

// dropTabularTablesForFiles drops the per-sheet tables + catalog rows a file
// owns. Best-effort: a failure is logged, not fatal (mirrors the vector-chunk
// cleanup). tabular_catalog rows also cascade via the file FK, but the
// physical tables must be dropped explicitly (DDL is not FK-driven).
func (d *Deleter) dropTabularTablesForFiles(ctx context.Context, files []fileRecord) {
	m := tabular.NewMaterializer(d.mainDB)
	for _, f := range files {
		if err := m.DropTablesForFile(ctx, f.ID); err != nil {
			slog.WarnContext(ctx, "cascade: drop tabular tables (best-effort)",
				"file_id", f.ID, "error", err)
		}
	}
}

func (d *Deleter) deleteStorageForFiles(ctx context.Context, files []fileRecord) {
	for _, f := range files {
		if f.StoragePath == nil || *f.StoragePath == "" {
			continue
		}
		if err := d.storage.DeleteFile(ctx, *f.StoragePath); err != nil {
			observability.RecordCascadeDeletionError(observability.CascadeResourceStorage)
			slog.WarnContext(ctx, "cascade: delete storage file (best-effort) — orphan object possible",
				"path", *f.StoragePath, "error", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Internal helpers — DB transactions
// ---------------------------------------------------------------------------

// txStep is one parameterised statement run inside a cascade transaction.
type txStep struct {
	sql  string
	args []any
}

// runSteps executes each step inside a single transaction, rolling back on
// the first error. Steps must be ordered so FK dependencies are satisfied.
func (d *Deleter) runSteps(ctx context.Context, label string, steps []txStep) error {
	return pgxutil.WithTx(ctx, d.mainDB, func(tx pgx.Tx) error {
		for _, step := range steps {
			if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
				return fmt.Errorf("%s exec %q: %w", label, step.sql, err)
			}
		}
		return nil
	})
}

func (d *Deleter) deleteKBTransaction(ctx context.Context, kbID string) error {
	return d.runSteps(ctx, "deleteKBTransaction", []txStep{
		{`DELETE FROM files WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM chats WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM generated_content WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM knowledge_base_shares WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM global_kb_editors WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_members WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM knowledge_bases WHERE id = $1`, []any{kbID}},
	})
}

func (d *Deleter) deleteUserTransaction(ctx context.Context, userID string, kbIDs []string) error {
	steps := make([]txStep, 0, 10)
	if len(kbIDs) > 0 {
		steps = append(steps,
			txStep{`DELETE FROM files WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM chats WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM generated_content WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM knowledge_base_shares WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM global_kb_editors WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM kb_members WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM knowledge_bases WHERE user_id = $1`, []any{userID}},
		)
	}
	// Remove user's share/membership entries in KBs they don't own, then the
	// user itself.
	steps = append(steps,
		txStep{`DELETE FROM knowledge_base_shares WHERE user_id = $1`, []any{userID}},
		txStep{`DELETE FROM kb_members WHERE user_id = $1`, []any{userID}},
		txStep{`DELETE FROM users WHERE id = $1`, []any{userID}},
	)
	return d.runSteps(ctx, "deleteUserTransaction", steps)
}

func (d *Deleter) deleteGlobalKBTransaction(ctx context.Context, kbID string) error {
	return d.runSteps(ctx, "deleteGlobalKBTransaction", []txStep{
		{`DELETE FROM global_kb_editors WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM knowledge_base_shares WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_members WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM files WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM chats WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM generated_content WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM knowledge_bases WHERE id = $1 AND visibility = 'public'`, []any{kbID}},
	})
}
