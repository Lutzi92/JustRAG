package files

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// enqueueRetry builds the re-embedding payload for an errored file and
// enqueues it. The re-embedding pipeline deletes old chunks before
// re-processing, so retrying a partially-ingested file is idempotent.
// Quick queue + same retry/timeout budget as a fresh upload: a retry is a
// user-facing action, not a background sweep.
func (h *Handler) enqueueRetry(file *FileInfo) error {
	if h.asynqClient == nil {
		return fmt.Errorf("task queue not configured")
	}
	if file.StoragePath == nil || *file.StoragePath == "" {
		return fmt.Errorf("file %s has no storage path", file.ID)
	}
	payload, err := json.Marshal(jobs.FileProcessingPayload{
		FileID:       file.ID,
		KbID:         file.KbID,
		FilePath:     *file.StoragePath,
		OriginalName: file.Name,
		MimeType:     file.Type,
	})
	if err != nil {
		return fmt.Errorf("marshal retry payload: %w", err)
	}
	_, err = h.asynqClient.Enqueue(
		asynq.NewTask(jobs.TypeReEmbedding, payload),
		asynq.Queue(jobs.QueueQuick),
		asynq.MaxRetry(3),
		asynq.Timeout(jobs.TimeoutFor(jobs.TypeReEmbedding)),
	)
	return err
}

// Retry handles POST /api/files/{id}/retry.
// Requires authentication; the user must have edit access to the file's KB.
// Only files in status='error' can be retried — the conditional reset is
// also the guard against double-clicks and concurrent retries (one wins,
// the rest get 409).
func (h *Handler) Retry(w http.ResponseWriter, r *http.Request) {
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

	if file.StoragePath == nil || *file.StoragePath == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnprocessableEntity, "File has no stored content to reprocess")
		return
	}

	reset, err := h.store.ResetFileForRetry(r.Context(), fileID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !reset {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusConflict, "File is not in error state")
		return
	}

	if err := h.enqueueRetry(file); err != nil {
		slog.Error("retry: enqueue failed", "fileId", fileID, "error", err)
		// Revert so the file doesn't sit in 'pending' forever; the queue
		// stage tells the user (and us) the failure happened pre-worker.
		_ = h.store.MarkFileError(r.Context(), fileID, "queue", "Could not be queued for processing")
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to queue file for processing")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]string{"status": "pending"})
}

// RetryFailed handles POST /api/kb/{id}/files/retry-failed.
// KB edit permission is enforced by the kbEditChain middleware. Resets and
// re-enqueues every status='error' file in the KB; files without a storage
// path (nothing to reprocess) and rows that lose the conditional-reset race
// are skipped. Response: {"retried": N}.
func (h *Handler) RetryFailed(w http.ResponseWriter, r *http.Request) {
	kbID := r.PathValue("id")
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		kbID = access.KB.ID
	}

	errFiles, err := h.store.ListErrorFiles(r.Context(), kbID)
	if err != nil {
		slog.Error("retry-failed: list error files failed", "kbId", kbID, "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	retried := 0
	for _, f := range errFiles {
		if f.StoragePath == nil || *f.StoragePath == "" {
			continue
		}
		reset, err := h.store.ResetFileForRetry(r.Context(), f.ID)
		if err != nil {
			slog.Warn("retry-failed: reset failed", "fileId", f.ID, "error", err)
			continue
		}
		if !reset {
			continue // lost the race to a concurrent retry — fine
		}
		if err := h.enqueueRetry(f); err != nil {
			slog.Error("retry-failed: enqueue failed", "fileId", f.ID, "error", err)
			_ = h.store.MarkFileError(r.Context(), f.ID, "queue", "Could not be queued for processing")
			continue
		}
		retried++
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]int{"retried": retried})
}
