package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/uploadcheck"
	"github.com/justrag/go-backend/internal/userfiles"
)

const (
	maxFromLibraryIDs = 100
	// filesTypeMaxBytes is the files.type column width (varchar(50)).
	filesTypeMaxBytes = 50
)

// Skip reasons of POST /api/kb/{id}/files/from-library (frontend contract).
const (
	skipNotFound    = "not_found"
	skipAlreadyInKB = "already_in_kb"
	skipKBFull      = "kb_full"
)

type fromLibraryRequest struct {
	UserFileIDs []string `json:"userFileIds"`
}

type fromLibraryAdded struct {
	FileID     string `json:"fileId"`
	UserFileID string `json:"userFileId"`
}

type fromLibrarySkipped struct {
	UserFileID string `json:"userFileId"`
	Reason     string `json:"reason"`
}

// enqueueFileProcessing queues the ingest job for a freshly created row.
// A nil asynq client (unit tests) is a no-op, as in the other ingest paths.
func (h *Handler) enqueueFileProcessing(rec *FileRecord, kbID, path, name, mimeType, userFileID string) error {
	if h.asynqClient == nil {
		return nil
	}
	payload, err := json.Marshal(jobs.FileProcessingPayload{
		FileID:       rec.ID,
		KbID:         kbID,
		FilePath:     path,
		OriginalName: name,
		MimeType:     mimeType,
		UserFileID:   userFileID,
	})
	if err != nil {
		return fmt.Errorf("marshal file processing payload: %w", err)
	}
	_, err = h.asynqClient.Enqueue(
		asynq.NewTask(jobs.TypeFileProcessing, payload),
		asynq.Queue(jobs.QueueQuick),
		asynq.MaxRetry(3),
		asynq.Timeout(jobs.TimeoutFor(jobs.TypeFileProcessing)),
	)
	return err
}

// removeLibraryBackedRow removes a files row whose blob belongs to the user's
// library. It NEVER touches the blob: the shared FileDeleter is blob-safe for
// library-backed rows; the fallback (no deleter wired) drops the tabular
// tables and the row only.
func (h *Handler) removeLibraryBackedRow(ctx context.Context, fileID string) {
	if h.fileDeleter != nil {
		if err := h.fileDeleter.DeleteFiles(ctx, []string{fileID}); err != nil {
			logctx.From(ctx).Error("library-backed row cleanup failed", "fileId", fileID, "error", err)
		}
		return
	}
	h.dropTabularTables(ctx, fileID)
	if err := h.store.DeleteFileRecord(ctx, fileID); err != nil {
		logctx.From(ctx).Error("library-backed row cleanup failed", "fileId", fileID, "error", err)
	}
}

// kbFileLimitReached applies the same per-KB count/size caps Upload enforces.
func kbFileLimitReached(limits *KBFileLimits, isGlobal bool, addSize int64) bool {
	maxFiles := maxFilesPerUserKB
	if isGlobal {
		maxFiles = maxFilesPerGlobalKB
	}
	return limits.FileCount >= maxFiles || limits.TotalSize+addSize > maxTotalSizePerKB
}

// uploadViaLibrary is the library-backed tail of Upload: the bytes land in the
// uploader's library (deduplicated, quota-checked) and the KB row points at
// that blob.
func (h *Handler) uploadViaLibrary(w http.ResponseWriter, r *http.Request, user *auth.Claims, kbID string, up *uploadcheck.Upload) {
	ctx := r.Context()
	uf, _, err := h.library.Ingest(ctx, user.ID, up)
	if err != nil {
		var qe *userfiles.QuotaError
		if errors.As(err, &qe) {
			userfiles.WriteQuotaExceeded(ctx, w, qe)
			return
		}
		logctx.From(ctx).Error("upload: library ingest failed", "kbId", kbID, "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "Failed to store file")
		return
	}

	existing, err := h.store.GetKBCopy(ctx, kbID, uf.ID)
	if err != nil {
		logctx.From(ctx).Error("upload: GetKBCopy failed", "kbId", kbID, "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "Failed to create file record")
		return
	}
	if existing != "" {
		httputil.WriteJSONCtx(ctx, w, http.StatusConflict, map[string]any{"error": "already_in_kb", "fileId": existing})
		return
	}

	rec, err := h.store.CreateFile(ctx, CreateFileData{
		KbID:        kbID,
		UploadedBy:  user.ID,
		Name:        up.Header.Filename,
		Type:        up.MimeType,
		Size:        int(up.Header.Size),
		Origin:      "upload",
		StoragePath: uf.StoragePath,
		UserFileID:  uf.ID,
	})
	if err != nil {
		if pgxutil.IsUniqueViolation(err) {
			// Lost a race with a concurrent add of the same library file.
			httputil.WriteJSONCtx(ctx, w, http.StatusConflict, map[string]any{"error": "already_in_kb"})
			return
		}
		logctx.From(ctx).Error("upload: create file record failed", "kbId", kbID, "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "Failed to create file record")
		return
	}

	if err := h.enqueueFileProcessing(rec, kbID, uf.StoragePath, up.Header.Filename, up.MimeType, uf.ID); err != nil {
		logctx.From(ctx).Error("failed to enqueue file processing job", "fileId", rec.ID, "error", err)
		h.removeLibraryBackedRow(ctx, rec.ID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to queue file for processing")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusCreated, rec)
}

// AddFromLibrary handles POST /api/kb/{id}/files/from-library: it adds the
// caller's library files to the KB without re-uploading any bytes.
// Per-id problems are reported in "skipped", never as an HTTP error.
func (h *Handler) AddFromLibrary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "Authentication required")
		return
	}
	if h.library == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "Not found")
		return
	}

	var req fromLibraryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil ||
		len(req.UserFileIDs) < 1 || len(req.UserFileIDs) > maxFromLibraryIDs {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "userFileIds must contain 1-100 ids")
		return
	}

	kbID := r.PathValue("id")
	var kbIsGlobal bool
	if access := kbaccess.AccessFromContext(ctx); access != nil && access.KB != nil {
		kbID = access.KB.ID
		kbIsGlobal = access.KB.IsGlobal
	}

	results, err := h.AddLibraryFiles(ctx, user.ID, kbID, kbIsGlobal, req.UserFileIDs)
	if err != nil {
		msg := "Internal Server Error"
		var ae *libraryAddError
		if errors.As(err, &ae) {
			msg = ae.public
		}
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, msg)
		return
	}

	added := []fromLibraryAdded{}
	skipped := []fromLibrarySkipped{}
	for _, res := range results {
		switch res.Status {
		case AddStatusAdded:
			added = append(added, fromLibraryAdded{FileID: res.FileID, UserFileID: res.UserFileID})
		case AddStatusDuplicate:
			skipped = append(skipped, fromLibrarySkipped{UserFileID: res.UserFileID, Reason: skipAlreadyInKB})
		default:
			skipped = append(skipped, fromLibrarySkipped{UserFileID: res.UserFileID, Reason: res.Error})
		}
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]any{"added": added, "skipped": skipped})
}
