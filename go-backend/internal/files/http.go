// Package files provides HTTP handlers for file download, deletion, and upload.
package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/uploadcheck"
	"github.com/justrag/go-backend/internal/userfiles"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// FileInfo holds the fields needed for download and delete operations.
type FileInfo struct {
	ID          string
	KbID        string
	Name        string
	Type        string
	StoragePath *string
}

// CreateFileData holds the parameters for inserting a new file record.
type CreateFileData struct {
	KbID        string
	Name        string
	Type        string // mime type
	Size        int
	Origin      string // e.g. "upload"
	StoragePath string
	RSSFeedID   string // optional: links file to an RSS feed
	// PublishedAt is the document's OWN publication date, as opposed to
	// created_at (the ingest timestamp). Only origins that carry one set
	// it — the RSS poller, from the feed item's PublishedParsed (W3-R9);
	// everything else leaves it nil and the column stays NULL.
	PublishedAt *time.Time
	// UploadedBy is the users.id of the person who added the file (user file
	// library, phase 0, migration 0075). Set by every user-initiated ingest
	// path (upload, text, url, crawl, research/academic import); empty for
	// source-owned origins (rss, confluence, git), which the store writes
	// as NULL.
	UploadedBy string
	// UserFileID links the row to the uploader's library file
	// (files.user_file_id, migration 0076); empty writes NULL. A linked row
	// shares the library's blob, so its storage_path is never deleted by a
	// files-row cleanup.
	UserFileID string
}

// FileRecord is the full file row returned after creation.
type FileRecord struct {
	ID          string    `json:"id"`
	KbID        string    `json:"kbId"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Size        *int      `json:"size"`
	Status      string    `json:"status"`
	Progress    int       `json:"progress"`
	Origin      string    `json:"origin"`
	StoragePath *string   `json:"-"` // not exposed to clients
	UserFileID  string    `json:"userFileId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// KBFileLimits holds per-KB file count and total size constraints.
type KBFileLimits struct {
	FileCount int
	TotalSize int64
}

// Store is the persistence interface required by Handler.
type Store interface {
	GetFileByID(ctx context.Context, id string) (*FileInfo, error)
	DeleteFileRecord(ctx context.Context, id string) error
	GetKBByID(ctx context.Context, id string) (*kbaccess.KnowledgeBase, error)
	GetKBRole(ctx context.Context, kbID, userID string) (string, error)
	CreateFile(ctx context.Context, data CreateFileData) (*FileRecord, error)
	GetKBFileLimits(ctx context.Context, kbID string) (*KBFileLimits, error)
	// GetKBCopy returns the id of kbID's files row backed by library file
	// userFileID, or "" when there is none.
	GetKBCopy(ctx context.Context, kbID, userFileID string) (string, error)
	// Retry support (see http_retry.go).
	ResetFileForRetry(ctx context.Context, fileID string) (bool, error)
	ListErrorFiles(ctx context.Context, kbID string) ([]*FileInfo, error)
	MarkFileError(ctx context.Context, fileID, stage, message string) error
}

// ChunkDeleter abstracts deleting vector chunks for a file across every
// existing chunk-table dimension.
type ChunkDeleter interface {
	DeleteChunksByFileIDAllDims(ctx context.Context, fileID string) error
}

// QueryCacheInvalidator is the minimum surface the file handlers need
// from the search service to nuke cached SearchResults whose chunks may
// reference newly-deleted files. Wired through *vector.SearchService in
// production; a nil invalidator is safe (the file handlers guard the call).
type QueryCacheInvalidator interface {
	InvalidateQueryCache(ctx context.Context, kbID string) error
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// taskEnqueuer is the subset of *asynq.Client the ingest handlers use,
// extracted so tests can inject an enqueuer that fails. *asynq.Client satisfies it.
type taskEnqueuer interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// Handler holds the dependencies for the file endpoints.
type Handler struct {
	store        Store
	storage      storage.Storage
	chunkService ChunkDeleter
	asynqClient  taskEnqueuer
	fetcher      *fetcher.Fetcher
	queryCache   QueryCacheInvalidator
	kgEvents     kgFileEventer
	tableDropper TableDropper
	fileDeleter  FileDeleter
	uploadLimits UploadLimits
	library      Library
	adopter      FileAdopter
}

// Library is the userfiles surface the files handler needs.
type Library interface {
	Ingest(ctx context.Context, ownerID string, up *uploadcheck.Upload) (*userfiles.UserFile, bool, error)
	Get(ctx context.Context, ownerID, id string) (*userfiles.UserFile, error)
}

// SetLibrary routes KB uploads through the user's file library and enables
// AddFromLibrary. When nil, Upload keeps its pre-library behaviour (blob
// stored per KB).
func (h *Handler) SetLibrary(lib Library) { h.library = lib }

// UploadLimits resolves ingest/upload sizing knobs for the upload handler.
// Narrow by design: internal/files must not import internal/chat (the
// SiteConfigReader's home package) merely to read one int, so production
// code wires a tiny adapter in internal/app/routes.go that closes over the
// shared chat.SiteConfigReader and calls chat.TabularMaxFileBytes. Optional
// — when nil (or when TabularMaxFileBytes returns <= 0), Upload skips the
// spreadsheet-specific size check entirely; the transport-wide
// http.MaxBytesReader cap (uploadcheck.MaxUploadSize) still applies to every
// file. The interface itself lives in internal/uploadcheck.
type UploadLimits = uploadcheck.Limits

// SetUploadLimits injects the upload/ingest sizing-knob resolver so Upload
// can reject an oversize spreadsheet with a 413 naming the configured
// limit. Optional — see UploadLimits.
func (h *Handler) SetUploadLimits(l UploadLimits) { h.uploadLimits = l }

// SetFetcher injects the shared Fetcher used by FetchURL to retrieve web
// pages with browser fallback and readability extraction. Optional — when
// nil, FetchURL falls back to a basic streaming HTTP fetch.
func (h *Handler) SetFetcher(f *fetcher.Fetcher) { h.fetcher = f }

// SetQueryCacheInvalidator injects the search service's query-cache
// invalidator so file mutation handlers (delete, ingest enqueue) can
// nuke cached SearchResults whose chunks reference the mutated KB.
// Optional — when nil, the cache is left to its TTL fallback.
func (h *Handler) SetQueryCacheInvalidator(qc QueryCacheInvalidator) { h.queryCache = qc }

// TableDropper drops a file's materialised spreadsheet tables (the
// `tabular.sheet_*` tables), its tabular_column_values rows and its
// tabular_catalog rows. Satisfied by *tabular.Materializer.
//
// C1/R20: the catalog row is the ONLY index from a file to its physical
// tables, and nothing else in the schema references them (they live in the
// `tabular` schema, outside `files`' foreign keys). Deleting the files row
// first therefore does not cascade the tables away — it makes them
// unreachable: orphaned tables that no catalog lookup, no re-ingest and no
// KB delete can ever find again. Every site that removes a files row must
// drop them FIRST.
//
// Optional: a nil dropper leaves the tables alone (the pre-Phase-2
// behaviour), which is what the unit tests and any deployment without a
// main pool get.
type TableDropper interface {
	DropTablesForFile(ctx context.Context, fileID string) error
}

// kgFileEventer removes a deleted file's knowledge-graph contribution and
// notifies mindmap subscribers. Satisfied by *kgevents.FileHook. Optional —
// nil leaves the KG graph untouched on delete (pre-0055 behaviour).
type kgFileEventer interface {
	OnFileDeleted(ctx context.Context, kbID, fileID string)
}

// SetKGFileEventer injects the KG cleanup + mindmap-notify hook for file
// deletes. Optional.
func (h *Handler) SetKGFileEventer(e kgFileEventer) { h.kgEvents = e }

// FileDeleter removes files rows and everything indexed for them.
// *cascade.Deleter satisfies it.
type FileDeleter interface {
	DeleteFiles(ctx context.Context, fileIDs []string) error
}

// SetFileDeleter injects the shared per-file cleanup. When set, Delete
// delegates to it after its auth checks; when nil the inline path is used.
func (h *Handler) SetFileDeleter(d FileDeleter) { h.fileDeleter = d }

// SetTableDropper injects the spreadsheet table cleanup hook for file
// deletes. Optional — nil leaves materialised tables in place.
func (h *Handler) SetTableDropper(d TableDropper) { h.tableDropper = d }

// dropTabularTables removes a file's materialised spreadsheet tables. It
// MUST run before the files row is deleted (see TableDropper). Best effort:
// a failure is logged and the delete continues, because leaving the files
// row behind for the sake of a `tabular` cleanup would strand the file in
// the UI with its chunks and blob already gone.
func (h *Handler) dropTabularTables(ctx context.Context, fileID string) {
	if h.tableDropper == nil || fileID == "" {
		return
	}
	if err := h.tableDropper.DropTablesForFile(ctx, fileID); err != nil {
		logctx.From(ctx).Warn("tabular: drop tables for deleted file failed",
			"fileId", fileID, "error", err)
	}
}

// invalidateQueryCache fires the optional KB query-cache invalidation
// hook. Fail-safe: nil invalidator is a no-op; errors are logged but
// never propagated, since cache invalidation is an optimization (the
// TTL fallback corrects any missed invalidation within ~24h).
func (h *Handler) invalidateQueryCache(ctx context.Context, kbID, reason string) {
	if h.queryCache == nil || kbID == "" {
		return
	}
	if err := h.queryCache.InvalidateQueryCache(ctx, kbID); err != nil {
		logctx.From(ctx).Warn("query_cache: invalidate failed",
			"kb_id", kbID, "reason", reason, "error", err)
	}
}

// NewHandler creates a Handler backed by the given store, storage, chunk service,
// and an optional Asynq client for enqueuing upload jobs (may be nil).
func NewHandler(store Store, stor storage.Storage, chunkSvc ChunkDeleter, asynqClient ...*asynq.Client) *Handler {
	h := &Handler{
		store:        store,
		storage:      stor,
		chunkService: chunkSvc,
	}
	// Guard the nil-interface trap: a nil *asynq.Client boxed into taskEnqueuer
	// becomes a non-nil interface value, which would bypass the `if h.asynqClient != nil`
	// guards in the ingest handlers. Only assign when the pointer is non-nil.
	if len(asynqClient) > 0 && asynqClient[0] != nil {
		h.asynqClient = asynqClient[0]
	}
	return h
}

// NewHandlerWithEnqueuer is like NewHandler but accepts any taskEnqueuer.
// Intended for tests that need to inject a failing enqueuer; production code
// uses NewHandler with a *asynq.Client.
func NewHandlerWithEnqueuer(store Store, stor storage.Storage, chunkSvc ChunkDeleter, enq taskEnqueuer) *Handler {
	h := &Handler{
		store:        store,
		storage:      stor,
		chunkService: chunkSvc,
	}
	// Guard the nil-interface trap the same way as NewHandler.
	if enq != nil {
		h.asynqClient = enq
	}
	return h
}

// ---------------------------------------------------------------------------
// Upload helpers
// ---------------------------------------------------------------------------

// maxFileNameBytes bounds the user-supplied file name / text-source title.
// Matches the files.name varchar(255) column so over-long values are rejected
// with a 400 rather than failing as a DB constraint violation (500).
const maxFileNameBytes = uploadcheck.MaxFileNameBytes

// sanitizeContentDispositionFilename strips double-quotes and control
// characters from a stored file name before it is interpolated into the
// quoted filename token of a Content-Disposition header.
func sanitizeContentDispositionFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
}

// Per-KB limits matching the Node.js backend.
const (
	maxFilesPerUserKB   = 500
	maxFilesPerGlobalKB = 1000
	maxTotalSizePerKB   = 500 << 20 // 500 MB
)

// safeFilenameRe matches characters that are allowed in a sanitized filename.
var safeFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9 .\-_]`)

// SafeNameSegment replaces every character outside the allowed set
// ([A-Za-z0-9], space, dot, dash, underscore) with an underscore. Use it
// for filename segments derived from titles, URLs, or feed-item names that
// the caller has already split into a single path component.
//
// It does NOT strip directory separators on its own — those are also outside
// the allowed set so they get replaced, but callers that handle full paths
// should prefer SanitizeFilename which does filepath.Base first.
func SafeNameSegment(s string) string {
	return safeFilenameRe.ReplaceAllString(s, "_")
}

// SanitizeFilename strips any directory component from name and replaces
// disallowed characters with underscores.
func SanitizeFilename(name string) string {
	return SafeNameSegment(filepath.Base(name))
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// hasViewAccess returns true when the user has at least view access to the
// given KB, per the same role ladder the kbaccess middleware enforces
// (superadmin, kb_members row, or view on a published global KB).
func (h *Handler) hasViewAccess(ctx context.Context, kb *kbaccess.KnowledgeBase, user *auth.Claims) (bool, error) {
	memberRole, err := h.store.GetKBRole(ctx, kb.ID, user.ID)
	if err != nil {
		return false, fmt.Errorf("get kb role: %w", err)
	}
	role := kbaccess.EffectiveRole(kb, user.Role, memberRole)
	return kbaccess.AtLeast(role, kbaccess.RoleView), nil
}

// hasEditAccess returns true when the user has at least edit access
// (can delete files from) the given KB, per the same role ladder the
// kbaccess middleware enforces.
func (h *Handler) hasEditAccess(ctx context.Context, kb *kbaccess.KnowledgeBase, user *auth.Claims) (bool, error) {
	memberRole, err := h.store.GetKBRole(ctx, kb.ID, user.ID)
	if err != nil {
		return false, fmt.Errorf("get kb role: %w", err)
	}
	role := kbaccess.EffectiveRole(kb, user.Role, memberRole)
	return kbaccess.AtLeast(role, kbaccess.RoleEdit), nil
}

// ---------------------------------------------------------------------------
// Endpoint handlers
// ---------------------------------------------------------------------------

// Download handles GET /api/files/{id}/download.
// Requires authentication; the user must have at least view access to the file's KB.
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "Authentication required")
		return
	}

	fileID := r.PathValue("id")
	if fileID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Missing file ID")
		return
	}

	file, err := h.store.GetFileByID(r.Context(), fileID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if file == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "File not found")
		return
	}

	kb, err := h.store.GetKBByID(r.Context(), file.KbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if kb == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Knowledge base not found")
		return
	}

	ok, err := h.hasViewAccess(r.Context(), kb, user)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !ok {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "Access denied")
		return
	}

	if file.StoragePath == nil || *file.StoragePath == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "File has no storage path")
		return
	}

	stream, err := h.storage.ReadFileStream(r.Context(), *file.StoragePath)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to read file")
		return
	}
	defer stream.Close()

	// Determine Content-Type from file name extension.
	ext := filepath.Ext(file.Name)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Build the Content-Disposition from a sanitized copy of the stored name.
	// Go 1.26 rejects CR/LF in header values, but a stray double-quote would
	// still let an attacker break out of the quoted filename token, and control
	// characters confuse some clients. Strip both.
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitizeContentDispositionFilename(file.Name)))
	w.Header().Set("Content-Type", contentType)
	// Restrictive CSP on user-uploaded files to prevent XSS if opened in browser.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; object-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, stream) //nolint:errcheck
}

// Delete handles DELETE /api/files/{id}.
// Requires authentication; the user must have edit access to the file's KB.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "Authentication required")
		return
	}

	fileID := r.PathValue("id")
	if fileID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Missing file ID")
		return
	}

	file, err := h.store.GetFileByID(r.Context(), fileID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if file == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "File not found")
		return
	}

	kb, err := h.store.GetKBByID(r.Context(), file.KbID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if kb == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Knowledge base not found")
		return
	}

	ok, err := h.hasEditAccess(r.Context(), kb, user)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !ok {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "Access denied")
		return
	}

	// Production path: the shared cascade cleanup (chunks, parents, HyPE,
	// tabular, guarded blob delete, row, KG hook, query cache).
	if h.fileDeleter != nil {
		if err := h.fileDeleter.DeleteFiles(r.Context(), []string{fileID}); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to delete file record")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Delete vector chunks across every existing chunk table (best effort).
	_ = h.chunkService.DeleteChunksByFileIDAllDims(r.Context(), fileID)

	// Delete from storage (best effort — file may already be absent).
	if file.StoragePath != nil && *file.StoragePath != "" {
		_ = h.storage.DeleteFile(r.Context(), *file.StoragePath)
	}

	// Drop the file's materialised spreadsheet tables BEFORE the files row
	// goes: the tabular_catalog row keyed on this file id is the only way
	// to find them again (C1/R20).
	h.dropTabularTables(r.Context(), fileID)

	// Delete the DB record.
	if err := h.store.DeleteFileRecord(r.Context(), fileID); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to delete file record")
		return
	}

	// Nuke any cached SearchResults that may reference this file's chunks.
	// Fire-and-forget — the cache is best-effort and the TTL fallback
	// corrects any missed invalidation within ~24h.
	h.invalidateQueryCache(r.Context(), file.KbID, "file_deleted")

	// Remove this file's KG contribution (precise per-file GC) and push a
	// graph_changed event so any open mindmap re-fetches. Best-effort.
	if h.kgEvents != nil {
		h.kgEvents.OnFileDeleted(r.Context(), file.KbID, fileID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// Upload handles POST /api/kb/{id}/files.
// Requires authentication and KB edit permission (enforced by kbaccess middleware).
// Accepts a multipart/form-data request with a "file" field; max 500 MB.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "Authentication required")
		return
	}

	// 1-3d. Parse the multipart form and run every content check (size cap,
	// empty, extension, filename length, spreadsheet gate).
	up, err := uploadcheck.Parse(w, r, h.uploadLimits)
	if err != nil {
		var ve *uploadcheck.Error
		if errors.As(err, &ve) {
			httputil.WriteErrorCtx(r.Context(), w, ve.Status, ve.Message)
			return
		}
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	defer up.File.Close()
	uploadedFile, header, mimeType := up.File, up.Header, up.MimeType

	// 4. Get KB ID from kbaccess context.
	kbID := r.PathValue("id")
	var kbIsGlobal bool
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		kbID = access.KB.ID
		kbIsGlobal = access.KB.IsGlobal
	}

	// 4b. Enforce per-KB file count and total size limits.
	limits, err := h.store.GetKBFileLimits(r.Context(), kbID)
	if err != nil {
		logctx.From(r.Context()).Error("upload: failed to check KB limits", "kbId", kbID, "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to check KB limits")
		return
	}
	if limits == nil {
		logctx.From(r.Context()).Error("upload: GetKBFileLimits returned nil", "kbId", kbID)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to check KB limits")
		return
	}
	maxFiles := maxFilesPerUserKB
	if kbIsGlobal {
		maxFiles = maxFilesPerGlobalKB
	}
	if limits.FileCount >= maxFiles {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, fmt.Sprintf("Knowledge base has reached the maximum of %d files", maxFiles))
		return
	}
	if limits.TotalSize+int64(header.Size) > maxTotalSizePerKB {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Knowledge base has reached the 500 MB storage limit")
		return
	}

	if h.library != nil {
		h.uploadViaLibrary(w, r, user, kbID, up)
		return
	}

	// 5. Get username from auth context.
	username := user.Username

	// 6. Build storage path.
	sanitizedFilename := SanitizeFilename(header.Filename)
	storagePath := storage.GetStoragePath(username, kbID, sanitizedFilename)

	// 7. Store file to storage.
	if err := h.storage.StoreFileFromReader(r.Context(), storagePath, uploadedFile, mimeType); err != nil {
		logctx.From(r.Context()).Error("upload: store file failed",
			"kbId", kbID, "storagePath", storagePath, "size", header.Size, "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to store file")
		return
	}

	// 8. Create file record in DB (status: 'pending', origin: 'upload').
	fileSize := int(header.Size)
	fileRecord, err := h.store.CreateFile(r.Context(), CreateFileData{
		KbID:        kbID,
		UploadedBy:  user.ID,
		Name:        header.Filename,
		Type:        mimeType,
		Size:        fileSize,
		Origin:      "upload",
		StoragePath: storagePath,
	})
	if err != nil {
		logctx.From(r.Context()).Error("upload: create file record failed",
			"kbId", kbID, "storagePath", storagePath, "error", err)
		// Best-effort cleanup of stored file.
		_ = h.storage.DeleteFile(r.Context(), storagePath)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to create file record")
		return
	}

	// 9. Enqueue Asynq file-processing job.
	if h.asynqClient != nil {
		payload, marshalErr := json.Marshal(jobs.FileProcessingPayload{
			FileID:       fileRecord.ID,
			KbID:         kbID,
			FilePath:     storagePath,
			OriginalName: header.Filename,
			MimeType:     mimeType,
		})
		if marshalErr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to prepare processing job")
			return
		}
		if _, enqErr := h.asynqClient.Enqueue(
			asynq.NewTask(jobs.TypeFileProcessing, payload),
			asynq.Queue(jobs.QueueQuick),
			asynq.MaxRetry(3),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeFileProcessing)),
		); enqErr != nil {
			logctx.From(r.Context()).Error("failed to enqueue file processing job", "fileId", fileRecord.ID, "error", enqErr)
			// Clean up the orphaned file and DB record so retries don't create duplicates.
			h.dropTabularTables(r.Context(), fileRecord.ID)
			_ = h.store.DeleteFileRecord(r.Context(), fileRecord.ID)
			_ = h.storage.DeleteFile(r.Context(), storagePath)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to queue file for processing")
			return
		}
	}

	// 10. Return 201 with file record.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, fileRecord)
}
