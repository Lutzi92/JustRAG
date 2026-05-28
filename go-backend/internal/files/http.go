// Package files provides HTTP handlers for file download, deletion, and upload.
package files

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/justrag/go-backend/internal/storage"
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
	GetKBShare(ctx context.Context, kbID, userID string) (*kbaccess.KBShare, error)
	CreateFile(ctx context.Context, data CreateFileData) (*FileRecord, error)
	GetKBFileLimits(ctx context.Context, kbID string) (*KBFileLimits, error)
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

// Handler holds the dependencies for the file endpoints.
type Handler struct {
	store        Store
	storage      storage.Storage
	chunkService ChunkDeleter
	asynqClient  *asynq.Client
	fetcher      *fetcher.Fetcher
	queryCache   QueryCacheInvalidator
}

// SetFetcher injects the shared Fetcher used by FetchURL to retrieve web
// pages with browser fallback and readability extraction. Optional — when
// nil, FetchURL falls back to a basic streaming HTTP fetch.
func (h *Handler) SetFetcher(f *fetcher.Fetcher) { h.fetcher = f }

// SetQueryCacheInvalidator injects the search service's query-cache
// invalidator so file mutation handlers (delete, ingest enqueue) can
// nuke cached SearchResults whose chunks reference the mutated KB.
// Optional — when nil, the cache is left to its TTL fallback.
func (h *Handler) SetQueryCacheInvalidator(qc QueryCacheInvalidator) { h.queryCache = qc }

// invalidateQueryCache fires the optional KB query-cache invalidation
// hook. Fail-safe: nil invalidator is a no-op; errors are logged but
// never propagated, since cache invalidation is an optimization (the
// TTL fallback corrects any missed invalidation within ~24h).
func (h *Handler) invalidateQueryCache(ctx context.Context, kbID, reason string) {
	if h.queryCache == nil || kbID == "" {
		return
	}
	if err := h.queryCache.InvalidateQueryCache(ctx, kbID); err != nil {
		slog.Warn("query_cache: invalidate failed",
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
	if len(asynqClient) > 0 {
		h.asynqClient = asynqClient[0]
	}
	return h
}

// ---------------------------------------------------------------------------
// Upload helpers
// ---------------------------------------------------------------------------

const maxUploadSize = 500 << 20 // 500 MB

// maxFileNameBytes bounds the user-supplied file name / text-source title.
// Matches the files.name varchar(255) column so over-long values are rejected
// with a 400 rather than failing as a DB constraint violation (500).
const maxFileNameBytes = 255

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

// dangerousExtensions is the set of file extensions that are rejected on upload.
// Matches the Node.js blocklist to prevent XSS and code execution via uploaded files.
var dangerousExtensions = map[string]bool{
	// Executables & scripts
	".exe": true, ".bat": true, ".cmd": true, ".com": true,
	".sh": true, ".bash": true, ".ps1": true, ".psm1": true, ".psd1": true,
	".vbs": true, ".msi": true, ".dll": true, ".scr": true, ".hta": true,
	".wsf": true, ".wsh": true, ".jar": true,
	// Web / scripting languages
	".js": true, ".mjs": true, ".cjs": true, ".ts": true,
	".html": true, ".htm": true, ".xhtml": true, ".phtml": true,
	".xml": true, ".svg": true,
	".php": true, ".pl": true, ".py": true, ".rb": true,
	".asp": true, ".aspx": true, ".jsp": true, ".jspx": true,
}

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

// hasViewAccess returns true when the user can view the given KB.
// Grants access if: superadmin, KB owner, global KB, or has a share.
func (h *Handler) hasViewAccess(ctx context.Context, kb *kbaccess.KnowledgeBase, user *auth.Claims) (bool, error) {
	if user.Role == "superadmin" {
		return true, nil
	}
	if kb.UserID != nil && *kb.UserID == user.ID {
		return true, nil
	}
	if kb.IsGlobal {
		return true, nil
	}
	share, err := h.store.GetKBShare(ctx, kb.ID, user.ID)
	if err != nil {
		return false, fmt.Errorf("check share: %w", err)
	}
	return share != nil, nil
}

// hasEditAccess returns true when the user can edit (delete files from) the given KB.
// Grants access if: superadmin, KB owner, or has an "edit" share.
func (h *Handler) hasEditAccess(ctx context.Context, kb *kbaccess.KnowledgeBase, user *auth.Claims) (bool, error) {
	if user.Role == "superadmin" {
		return true, nil
	}
	if kb.UserID != nil && *kb.UserID == user.ID {
		return true, nil
	}
	share, err := h.store.GetKBShare(ctx, kb.ID, user.ID)
	if err != nil {
		return false, fmt.Errorf("check share: %w", err)
	}
	return share != nil && share.Permission == "edit", nil
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

	// Delete vector chunks across every existing chunk table (best effort).
	_ = h.chunkService.DeleteChunksByFileIDAllDims(r.Context(), fileID)

	// Delete from storage (best effort — file may already be absent).
	if file.StoragePath != nil && *file.StoragePath != "" {
		_ = h.storage.DeleteFile(r.Context(), *file.StoragePath)
	}

	// Delete the DB record.
	if err := h.store.DeleteFileRecord(r.Context(), fileID); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to delete file record")
		return
	}

	// Nuke any cached SearchResults that may reference this file's chunks.
	// Fire-and-forget — the cache is best-effort and the TTL fallback
	// corrects any missed invalidation within ~24h.
	h.invalidateQueryCache(r.Context(), file.KbID, "file_deleted")

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

	// 1. Enforce hard 500 MB request body limit, then parse multipart form.
	// MaxBytesReader rejects bodies exceeding the cap (ParseMultipartForm's
	// argument only controls the in-memory buffering threshold, not the total size).
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MB in-memory threshold
		slog.Warn("upload: parse multipart form failed", "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "File too large or invalid multipart form")
		return
	}

	// 2. Get the file from the form.
	uploadedFile, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "File field is required")
		return
	}
	defer uploadedFile.Close()

	// 3a. Reject empty files.
	if header.Size == 0 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "File must not be empty")
		return
	}

	// 3b. Reject dangerous extensions.
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if dangerousExtensions[ext] {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "File type not allowed")
		return
	}

	// 3c. Reject over-long names up front. files.name is varchar(255); without
	// this check an over-long filename surfaces as a DB constraint violation
	// (HTTP 500) instead of a clean validation error.
	if len(header.Filename) > maxFileNameBytes {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, fmt.Sprintf("Filename must not exceed %d bytes", maxFileNameBytes))
		return
	}

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
		slog.Error("upload: failed to check KB limits", "kbId", kbID, "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to check KB limits")
		return
	}
	if limits == nil {
		slog.Error("upload: GetKBFileLimits returned nil", "kbId", kbID)
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

	// 5. Get username from auth context.
	username := user.Username

	// 6. Build storage path.
	sanitizedFilename := SanitizeFilename(header.Filename)
	storagePath := storage.GetStoragePath(username, kbID, sanitizedFilename)

	// Detect MIME type from extension (fall back to octet-stream).
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// 7. Store file to storage.
	if err := h.storage.StoreFileFromReader(r.Context(), storagePath, uploadedFile, mimeType); err != nil {
		slog.Error("upload: store file failed",
			"kbId", kbID, "storagePath", storagePath, "size", header.Size, "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to store file")
		return
	}

	// 8. Create file record in DB (status: 'pending', origin: 'upload').
	fileSize := int(header.Size)
	fileRecord, err := h.store.CreateFile(r.Context(), CreateFileData{
		KbID:        kbID,
		Name:        header.Filename,
		Type:        mimeType,
		Size:        fileSize,
		Origin:      "upload",
		StoragePath: storagePath,
	})
	if err != nil {
		slog.Error("upload: create file record failed",
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
		); enqErr != nil {
			slog.Error("failed to enqueue file processing job", "fileId", fileRecord.ID, "error", enqErr)
			// Clean up the orphaned file and DB record so retries don't create duplicates.
			_ = h.store.DeleteFileRecord(r.Context(), fileRecord.ID)
			_ = h.storage.DeleteFile(r.Context(), storagePath)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to queue file for processing")
			return
		}
	}

	// 10. Return 201 with file record.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, fileRecord)
}
