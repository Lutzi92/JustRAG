// Package cascade provides a CascadeDeleter that cleans up all assets
// (vector chunks, storage files, and database rows) associated with a
// knowledge base or user before removing the root record.
package cascade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/libpaths"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/vector"
)

// fileRecord holds the minimal file info needed for cleanup.
type fileRecord struct {
	ID          string
	KbID        string
	StoragePath *string
	// UserFileID is non-nil for a KB copy of a user-library file. Such a row
	// shares the library blob, which only DeleteUserFile may remove.
	UserFileID *string
}

// KGFileHook removes a file's KG contribution and announces the graph change.
// kgevents.FileHook satisfies it.
type KGFileHook interface {
	OnFileDeleted(ctx context.Context, kbID, fileID string)
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
	vectorDB     *pgxpool.Pool
	chunkService *vector.ChunkService
	hype         *vector.HyPEStore
	storage      storage.Storage
	queryCache   QueryCacheInvalidator
	kgHook       KGFileHook
}

// New creates a Deleter backed by the given pools and storage.
func New(mainDB *pgxpool.Pool, vectorDB *pgxpool.Pool, stor storage.Storage) *Deleter {
	return &Deleter{
		mainDB:       mainDB,
		vectorDB:     vectorDB,
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

// SetKGFileHook injects the per-file KG cleanup + graph_changed hook used by
// DeleteFiles. Optional — when nil, the KG step is skipped.
func (d *Deleter) SetKGFileHook(h KGFileHook) { d.kgHook = h }

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
	d.deleteParentChunksForFiles(ctx, files)
	d.dropTabularTablesForFiles(ctx, files)
	d.deleteStorageForFiles(ctx, files)
	d.deleteBM25StatsForKB(ctx, kbID)

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
	// Library files first: user_files.owner_user_id is RESTRICT, and their
	// copies live in KBs this user may not own.
	ufIDs, err := d.userFileIDsByOwner(ctx, userID)
	if err != nil {
		return fmt.Errorf("cascade DeleteUser: fetch library files: %w", err)
	}
	for _, ufID := range ufIDs {
		if err := d.DeleteUserFile(ctx, userID, ufID); err != nil {
			return fmt.Errorf("cascade DeleteUser: library file %s: %w", ufID, err)
		}
	}

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
		d.deleteParentChunksForFiles(ctx, files)
		d.dropTabularTablesForFiles(ctx, files)
		d.deleteStorageForFiles(ctx, files)
		d.deleteBM25StatsForKB(ctx, kbID)
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
	d.deleteParentChunksForFiles(ctx, files)
	d.dropTabularTablesForFiles(ctx, files)
	d.deleteStorageForFiles(ctx, files)
	d.deleteBM25StatsForKB(ctx, kbID)

	if err := d.deleteGlobalKBTransaction(ctx, kbID); err != nil {
		return err
	}
	d.invalidateQueryCache(ctx, kbID, "global_kb_deleted")
	return nil
}

// ---------------------------------------------------------------------------
// DeleteFiles removes the given files rows and everything indexed for them:
// vector chunks (all dims, incl. RAPTOR nodes), parent chunks, HyPE rows,
// tabular tables, the files rows, KG contribution and — only for rows with
// user_file_id IS NULL — the blob. Query caches of every affected KB are
// invalidated. Best-effort on the non-DB steps; returns an error only if the
// record load or the row delete fails. Unknown ids are skipped silently.
// ---------------------------------------------------------------------------
func (d *Deleter) DeleteFiles(ctx context.Context, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	files, err := d.loadFileRecords(ctx, fileIDs)
	if err != nil {
		return fmt.Errorf("cascade DeleteFiles: load files: %w", err)
	}
	if len(files) == 0 {
		return nil
	}

	d.deleteVectorChunksForFiles(ctx, files)
	d.deleteParentChunksForFiles(ctx, files)
	d.dropTabularTablesForFiles(ctx, files)
	d.deleteStorageForFiles(ctx, files)

	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	if _, err := d.mainDB.Exec(ctx, `DELETE FROM files WHERE id = ANY($1::uuid[])`, ids); err != nil {
		return fmt.Errorf("cascade DeleteFiles: delete rows: %w", err)
	}

	seen := make(map[string]struct{}, 1)
	for _, f := range files {
		if d.kgHook != nil {
			d.kgHook.OnFileDeleted(ctx, f.KbID, f.ID)
		}
		if _, ok := seen[f.KbID]; !ok {
			seen[f.KbID] = struct{}{}
			d.invalidateQueryCache(ctx, f.KbID, "file_deleted")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers — DB queries
// ---------------------------------------------------------------------------

func (d *Deleter) getFilesByKBID(ctx context.Context, kbID string) ([]fileRecord, error) {
	rows, err := d.mainDB.Query(ctx,
		`SELECT id, kb_id, storage_path, user_file_id FROM files WHERE kb_id = $1`, kbID)
	if err != nil {
		return nil, fmt.Errorf("query files for kb %s: %w", kbID, err)
	}
	defer rows.Close()

	var files []fileRecord
	for rows.Next() {
		var f fileRecord
		if err := rows.Scan(&f.ID, &f.KbID, &f.StoragePath, &f.UserFileID); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (d *Deleter) loadFileRecords(ctx context.Context, fileIDs []string) ([]fileRecord, error) {
	rows, err := d.mainDB.Query(ctx,
		`SELECT id, kb_id, storage_path, user_file_id FROM files WHERE id = ANY($1::uuid[])`, fileIDs)
	if err != nil {
		return nil, fmt.Errorf("query files: %w", err)
	}
	defer rows.Close()

	var files []fileRecord
	for rows.Next() {
		var f fileRecord
		if err := rows.Scan(&f.ID, &f.KbID, &f.StoragePath, &f.UserFileID); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// ErrUserFileNotFound means the library file does not exist for this owner
// (missing, malformed id, or another owner's file).
var ErrUserFileNotFound = errors.New("user file not found")

// DeleteUserFile removes a library file and every KB copy of it: each copy
// gets the full per-file cleanup (DeleteFiles), then the user_files row and
// the blob go. ownerID scopes the lookup — another owner's id is
// ErrUserFileNotFound. Returns an error if any KB copy's row delete failed
// (the user_files row is then left in place, so a retry can finish).
func (d *Deleter) DeleteUserFile(ctx context.Context, ownerID, userFileID string) error {
	if _, err := uuid.Parse(userFileID); err != nil {
		return ErrUserFileNotFound
	}
	if _, err := uuid.Parse(ownerID); err != nil {
		return ErrUserFileNotFound
	}
	var storagePath string
	err := d.mainDB.QueryRow(ctx,
		`SELECT storage_path FROM user_files WHERE id = $1::uuid AND owner_user_id = $2::uuid`,
		userFileID, ownerID).Scan(&storagePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserFileNotFound
	}
	if err != nil {
		return fmt.Errorf("cascade DeleteUserFile: load: %w", err)
	}

	rows, err := d.mainDB.Query(ctx, `SELECT id::text FROM files WHERE user_file_id = $1::uuid`, userFileID)
	if err != nil {
		return fmt.Errorf("cascade DeleteUserFile: list copies: %w", err)
	}
	copyIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("cascade DeleteUserFile: scan copies: %w", err)
	}
	if len(copyIDs) > 0 {
		if err := d.DeleteFiles(ctx, copyIDs); err != nil {
			return fmt.Errorf("cascade DeleteUserFile: delete copies: %w", err)
		}
	}

	if _, err := d.mainDB.Exec(ctx,
		`DELETE FROM user_files WHERE id = $1::uuid AND owner_user_id = $2::uuid`, userFileID, ownerID); err != nil {
		return fmt.Errorf("cascade DeleteUserFile: delete row: %w", err)
	}

	// Best-effort and after the row: an orphan blob is cheaper than a row
	// pointing at nothing. Detached so a client disconnect cannot skip it.
	bctx := context.WithoutCancel(ctx)
	if storagePath != "" {
		if err := d.storage.DeleteFile(bctx, storagePath); err != nil {
			observability.RecordCascadeDeletionError(observability.CascadeResourceStorage)
			slog.WarnContext(bctx, "cascade: delete library blob (best-effort) — orphan object possible",
				"path", storagePath, "user_file_id", userFileID, "error", err)
		}
	}
	// The parse cache (P2-R1) lives next to the blob and dies with it.
	if err := d.storage.DeleteDirectory(bctx, libpaths.ParseCacheDir(ownerID, userFileID)); err != nil {
		observability.RecordCascadeDeletionError(observability.CascadeResourceStorage)
		slog.WarnContext(bctx, "cascade: delete library parse cache (best-effort) — orphan objects possible",
			"user_file_id", userFileID, "error", err)
	}
	return nil
}

func (d *Deleter) userFileIDsByOwner(ctx context.Context, ownerID string) ([]string, error) {
	rows, err := d.mainDB.Query(ctx, `SELECT id::text FROM user_files WHERE owner_user_id = $1::uuid`, ownerID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
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

// beforeVectorDeleteHook is a test seam, called by deleteVectorChunksForFiles
// after the donors are retired and before any vector row is deleted. Always
// nil in production.
var beforeVectorDeleteHook func(ctx context.Context, fileIDs []string)

// retireCopyDonors clears index_fingerprint and bumps progress_updated_at
// (the copy-mode donor generation token) on rows about to lose their index.
// Copy mode (internal/worker) picks donors by fingerprint and re-checks the
// token after copying, so without this a copy racing the delete could map a
// half- or fully-deleted donor index and stamp it completed. Best-effort and
// before every vector delete: the rows themselves go later, in the caller's
// main-DB delete.
func (d *Deleter) retireCopyDonors(ctx context.Context, ids []string) {
	if d.mainDB == nil || len(ids) == 0 {
		return
	}
	if _, err := d.mainDB.Exec(ctx,
		`UPDATE files SET index_fingerprint = NULL, progress_updated_at = NOW() WHERE id = ANY($1::uuid[])`, ids); err != nil {
		slog.WarnContext(ctx, "cascade: retire copy donors (best-effort) — a racing copy may read a deleted index",
			"file_count", len(ids), "error", err)
	}
}

// deleteVectorChunksForFiles is the first index-destroying step of every
// delete path (DeleteFiles, DeleteKB, DeleteUser, DeleteGlobalKB), so it
// retires the files as copy donors first.
func (d *Deleter) deleteVectorChunksForFiles(ctx context.Context, files []fileRecord) {
	if len(files) == 0 {
		return
	}
	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	d.retireCopyDonors(ctx, ids)
	if beforeVectorDeleteHook != nil {
		beforeVectorDeleteHook(ctx, ids)
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

// deleteParentChunksForFiles removes parent-chunk rows (no bulk API, so one
// call per file). Best-effort.
func (d *Deleter) deleteParentChunksForFiles(ctx context.Context, files []fileRecord) {
	for _, f := range files {
		if err := d.chunkService.DeleteParentChunksByFileID(ctx, f.ID); err != nil {
			observability.RecordCascadeDeletionError(observability.CascadeResourceVector)
			slog.WarnContext(ctx, "cascade: delete parent chunks (best-effort) — orphan rows possible",
				"file_id", f.ID, "error", err)
		}
	}
}

// deleteBM25StatsForKB removes the BM25 stats rows (both tables, both arms,
// every dim) for a KB that is being deleted outright. Best-effort like
// deleteVectorChunksForFiles's HyPE cleanup: a bad kbID or a listing/delete
// failure is logged, not fatal — orphaned stats rows are harmless (they key
// on kb_id but carry no FK back to knowledge_bases) and the next stale-KB
// maintenance sweep would just refresh them again if the id were reused,
// which cannot happen since kbID is a UUID.
func (d *Deleter) deleteBM25StatsForKB(ctx context.Context, kbID string) {
	if d.vectorDB == nil {
		return
	}
	id, err := uuid.Parse(kbID)
	if err != nil {
		slog.WarnContext(ctx, "cascade: skip bm25 stats cleanup (invalid kb id)", "kb_id", kbID, "error", err)
		return
	}
	dims, err := d.chunkService.ListChunkTableDimensions(ctx)
	if err != nil {
		slog.WarnContext(ctx, "cascade: list dims for bm25 stats cleanup failed (best-effort)", "error", err)
		return
	}
	if err := vector.DeleteBM25StatsForKB(ctx, d.vectorDB, id, dims); err != nil {
		slog.WarnContext(ctx, "cascade: delete bm25 stats rows (best-effort) — orphan rows possible",
			"kb_id", kbID, "error", err)
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
		if f.UserFileID != nil {
			// Shared library blob: only DeleteUserFile may remove it.
			continue
		}
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

// The agent chat's ADK state lives under app name 'agentchat' (chat's
// agentChatApp) with session id / run thread_id = the chat id, without an
// FK to chats; these delete it for the chats a transaction is about to
// delete. The chat store and kbmembers.LeaveKB carry their own variants.
const (
	adkSessionsOfKBChats  = `DELETE FROM adk_sessions WHERE app_name = 'agentchat' AND id IN (SELECT id::text FROM chats WHERE kb_id = $1)`
	agentRunsOfKBChats    = `DELETE FROM agent_runs WHERE app_name = 'agentchat' AND thread_id IN (SELECT id::text FROM chats WHERE kb_id = $1)`
	adkSessionsOfKBsChats = `DELETE FROM adk_sessions WHERE app_name = 'agentchat' AND id IN (SELECT id::text FROM chats WHERE kb_id = ANY($1::uuid[]))`
	agentRunsOfKBsChats   = `DELETE FROM agent_runs WHERE app_name = 'agentchat' AND thread_id IN (SELECT id::text FROM chats WHERE kb_id = ANY($1::uuid[]))`
)

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
	return d.runSteps(ctx, "deleteKBTransaction", kbDeleteSteps(kbID))
}

// kbDeleteSteps lists the statements of the private-KB delete transaction,
// in execution order.
func kbDeleteSteps(kbID string) []txStep {
	return []txStep{
		{`DELETE FROM files WHERE kb_id = $1`, []any{kbID}},
		// Agent chat (app 'agentchat'): its ADK session id and its runs'
		// thread_id are the chat id; adk_events cascade from adk_sessions.
		// Neither has a chats FK, so they go before the chats.
		{adkSessionsOfKBChats, []any{kbID}},
		{agentRunsOfKBChats, []any{kbID}},
		{`DELETE FROM chats WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM generated_content WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM knowledge_base_shares WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM global_kb_editors WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_members WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_invite_links WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_subscriptions WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_category_links WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_favorites WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_user_category_links WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM knowledge_bases WHERE id = $1`, []any{kbID}},
	}
}

func (d *Deleter) deleteUserTransaction(ctx context.Context, userID string, kbIDs []string) error {
	return d.runSteps(ctx, "deleteUserTransaction", userDeleteSteps(userID, kbIDs))
}

// userDeleteSteps lists the statements of the user-delete transaction, in
// execution order.
func userDeleteSteps(userID string, kbIDs []string) []txStep {
	steps := make([]txStep, 0, 23)
	if len(kbIDs) > 0 {
		steps = append(steps,
			txStep{`DELETE FROM files WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{adkSessionsOfKBsChats, []any{kbIDs}},
			txStep{agentRunsOfKBsChats, []any{kbIDs}},
			txStep{`DELETE FROM chats WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM generated_content WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM knowledge_base_shares WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM global_kb_editors WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM kb_members WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM kb_invite_links WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM kb_subscriptions WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM kb_category_links WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM kb_favorites WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM kb_user_category_links WHERE kb_id = ANY($1::uuid[])`, []any{kbIDs}},
			txStep{`DELETE FROM knowledge_bases WHERE user_id = $1`, []any{userID}},
		)
	}
	// Remove user's share/membership entries in KBs they don't own, then the
	// user itself.
	steps = append(steps,
		txStep{`DELETE FROM knowledge_base_shares WHERE user_id = $1`, []any{userID}},
		txStep{`DELETE FROM kb_members WHERE user_id = $1`, []any{userID}},
		txStep{`DELETE FROM kb_subscriptions WHERE user_id = $1`, []any{userID}},
		// Per-user topic filters (migration 0086). Links before categories,
		// so no step leans on the composite FK's ON DELETE CASCADE.
		txStep{`DELETE FROM kb_favorites WHERE user_id = $1`, []any{userID}},
		txStep{`DELETE FROM kb_user_category_links WHERE user_id = $1`, []any{userID}},
		txStep{`DELETE FROM kb_user_categories WHERE user_id = $1`, []any{userID}},
		// ADK sessions (user_id is TEXT, migration 0082): adk_events cascade
		// from adk_sessions via FK, agent_runs cascade via their users FK.
		// adk_app_states is app-wide and untouched.
		txStep{`DELETE FROM adk_sessions WHERE user_id = $1`, []any{userID}},
		txStep{`DELETE FROM adk_user_states WHERE user_id = $1`, []any{userID}},
		txStep{`DELETE FROM users WHERE id = $1`, []any{userID}},
	)
	return steps
}

func (d *Deleter) deleteGlobalKBTransaction(ctx context.Context, kbID string) error {
	return d.runSteps(ctx, "deleteGlobalKBTransaction", globalKBDeleteSteps(kbID))
}

// globalKBDeleteSteps lists the statements of the public-KB delete
// transaction, in execution order.
func globalKBDeleteSteps(kbID string) []txStep {
	return []txStep{
		{`DELETE FROM global_kb_editors WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM knowledge_base_shares WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_members WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_invite_links WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_subscriptions WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_category_links WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_favorites WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM kb_user_category_links WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM files WHERE kb_id = $1`, []any{kbID}},
		// Agent chat (app 'agentchat'): its ADK session id and its runs'
		// thread_id are the chat id; adk_events cascade from adk_sessions.
		// Neither has a chats FK, so they go before the chats.
		{adkSessionsOfKBChats, []any{kbID}},
		{agentRunsOfKBChats, []any{kbID}},
		{`DELETE FROM chats WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM generated_content WHERE kb_id = $1`, []any{kbID}},
		{`DELETE FROM knowledge_bases WHERE id = $1 AND visibility = 'public'`, []any{kbID}},
	}
}
